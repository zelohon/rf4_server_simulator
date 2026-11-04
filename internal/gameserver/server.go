// Package gameserver 实现隧道服务端、会话管理、请求分发和兜底处理。
// 网关负责 RC4、分帧和 ACK；本进程处理明文 RPC 信封并维护严格递增的推送 call_id。
package gameserver

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"rf4server/internal/config"
	"rf4server/internal/metrics"
	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/tunnel"
	"rf4server/internal/wiregen"
)

// 指标。
var (
	gsSessionsActive prometheus.Gauge
	gsRequests       *prometheus.CounterVec
	gsPushes         prometheus.Counter
	gsPushRejected   prometheus.Counter
	gsFallbacks      *prometheus.CounterVec
)

func initMetrics(reg *prometheus.Registry) {
	gsSessionsActive = metrics.NewGauge(reg, "gameserver_sessions_active", "在线会话数")
	gsRequests = metrics.NewLabeledCounter(reg, "gameserver_requests_total", "请求数", "cmd")
	gsPushes = metrics.NewCounter(reg, "gameserver_pushes_total", "服务器推送数")
	gsPushRejected = metrics.NewCounter(reg, "gameserver_push_rejected_total", "被守卫拒发的推送数")
	gsFallbacks = metrics.NewLabeledCounter(reg, "gameserver_fallbacks_total", "兜底响应数", "kind")
}

// Server 主服务器。
type Server struct {
	cfg      config.Game
	log      *slog.Logger
	table    *proto.OpsTable
	dispatch *Dispatch
	DB       *store.DB // 可空：模板兜底降级（生产由 cmd 装配）

	mu       sync.Mutex
	sessions map[uint64]*Session // 网关 sid -> 会话
	byToken  map[string]*Session // 活跃会话（重连接管）

	callMu  sync.Mutex
	callSeq map[string]uint64 // token -> 已分配的最大推送 call_id

	// 钓鱼/业务状态
	fishStates map[int64]*FishState
	fishMu     sync.Mutex
	levelMap   map[int64]string
	levelMu    sync.Mutex

	// 运行参数缓存
	tunMu    sync.Mutex
	tunCache map[string]float64
	tunAt    time.Time

	// 搏鱼曲线池
	curves  map[string][][19]float64
	curveMu sync.Mutex

	// 技能定义缓存
	skillDefs     map[int64][]skillNodeDef
	skillDefsOnce sync.Once
}

// NewServer 构建主服务器。
func NewServer(cfg config.Game, log *slog.Logger, obs *metrics.Server) *Server {
	reg := obsRegistry(obs)
	initMetrics(reg)
	s := &Server{
		cfg:        cfg,
		log:        log,
		table:      wiregen.Table(),
		sessions:   map[uint64]*Session{},
		byToken:    map[string]*Session{},
		callSeq:    map[string]uint64{},
		fishStates: map[int64]*FishState{},
		levelMap:   map[int64]string{},
	}
	s.dispatch = NewDispatch(s)
	registerHandlers(s.dispatch)
	return s
}

// SetDB 接入 PG 并加载缓存（测试/装配用）。
func (s *Server) SetDB(db *store.DB) { s.attachDB(db, s.log) }

// Table 运行时 ops 表。
func (s *Server) Table() *proto.OpsTable { return s.table }

// Serve 隧道服务循环（阻塞）。
func (s *Server) Serve(ln net.Listener) error {
	for {
		nc, err := ln.Accept()
		if err != nil {
			return err
		}
		go func() {
			c, err := tunnel.HandshakeServer(nc, s.cfg.Tunnel.PSK)
			if err != nil {
				s.log.Warn("隧道握手失败", "remote", nc.RemoteAddr(), "err", err)
				_ = nc.Close()
				return
			}
			s.log.Info("隧道连接建立", "remote", c.RemoteAddr())
			s.handleTunnel(c)
		}()
	}
}

// handleTunnel 处理一条隧道连接（读循环）。
func (s *Server) handleTunnel(c *tunnel.Conn) {
	defer c.Close()
	for {
		rt, sid, payload, err := c.ReadRecord()
		if err != nil {
			s.log.Info("隧道断开", "remote", c.RemoteAddr(), "err", err)
			return
		}
		switch rt {
		case tunnel.TypeOpen:
			s.openSession(c, sid, payload)
		case tunnel.TypeData:
			if sess := s.byID(sid); sess != nil {
				s.handleEnvelopePayload(sess, payload)
			} else {
				s.log.Warn("未知会话的数据", "sid", sid)
			}
		case tunnel.TypeClose:
			if sess := s.byID(sid); sess != nil {
				sess.Close("gateway_close")
			}
		case tunnel.TypePing:
			_ = c.WriteRecord(tunnel.TypePong, 0, nil)
		}
	}
}

// openSession 开新会话；同 token 活跃会话被接管（重连）。
func (s *Server) openSession(c *tunnel.Conn, sid uint64, payload []byte) {
	var op tunnel.OpenPayload
	if err := json.Unmarshal(payload, &op); err != nil {
		s.log.Warn("OPEN payload 解析失败", "err", err)
		return
	}
	userid := op.Userid
	if userid <= 0 {
		userid = useridFromToken(op.Token)
	}
	sess := newSession(s, sid, op.Token, userid, c)

	// 同 token 重连时替换原会话。原会话不能在持有 s.mu 时关闭，
	// 因为 Close 会经 removeSession 再次获取该锁；先更新映射，再在锁外关闭。
	s.mu.Lock()
	var superseded *Session
	if old, ok := s.byToken[op.Token]; ok && old != nil {
		s.log.Info("同 token 旧会话被接管", "old_sid", old.ID, "new_sid", sid)
		superseded = old
		delete(s.sessions, old.ID)
	}
	s.sessions[sid] = sess
	s.byToken[op.Token] = sess
	s.mu.Unlock()
	gsSessionsActive.Add(1)
	if superseded != nil {
		superseded.Close("superseded")
	}

	sess.log.Info("会话开启", "reconnect", op.Reconnect, "client_ip", op.ClientIP)
	go s.CheckBanned(sess)

	// 仅新连接发送 boot 0/1；重连沿用既有流程。
	// 世界时钟保活必须对新连接和重连都启动：否则重连会话在约 10 秒
	// 无下行数据后会被客户端判定断线，形成"重连-断开"循环。
	if !op.Reconnect {
		// 新玩家初始化（档案/starter 物品/快捷栏）
		if err := s.EnsurePlayer(context.Background(), userid); err != nil {
			sess.log.Warn("玩家初始化失败", "err", err)
		}
		sess.Push(0, 1, proto.Args524())
	}
	sess.startKeepalive()
}

// byID 取会话。
func (s *Server) byID(sid uint64) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[sid]
}

// removeSession 移除会话注册。
func (s *Server) removeSession(sess *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions[sess.ID] == sess {
		delete(s.sessions, sess.ID)
	}
	if s.byToken[sess.Token] == sess {
		delete(s.byToken, sess.Token)
	}
	gsSessionsActive.Add(-1)
}

// CloseAll 关闭全部会话。
// 先快照并清空映射，再在锁外关闭，避免 Close 回调 removeSession 造成死锁。
func (s *Server) CloseAll() {
	s.mu.Lock()
	all := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		all = append(all, sess)
	}
	s.sessions = map[uint64]*Session{}
	s.byToken = map[string]*Session{}
	s.mu.Unlock()
	for _, sess := range all {
		sess.Close("server_shutdown")
	}
}

// useridFromToken 从四段 token 的第三段读取 userid；解析失败返回 0。
func useridFromToken(token string) int64 {
	parts := splitToken(token)
	if len(parts) >= 3 {
		var uid int64
		for _, ch := range parts[2] {
			if ch < '0' || ch > '9' {
				return 0
			}
			uid = uid*10 + int64(ch-'0')
		}
		return uid
	}
	return 0
}

func splitToken(token string) []string {
	var out []string
	cur := []byte{}
	for i := 0; i < len(token); i++ {
		if token[i] == '|' {
			out = append(out, string(cur))
			cur = cur[:0]
			continue
		}
		cur = append(cur, token[i])
	}
	out = append(out, string(cur))
	return out
}

// SessionCount 在线会话数（观测/管理后台用）。
func (s *Server) SessionCount() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int64(len(s.sessions))
}

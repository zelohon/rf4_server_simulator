package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"rf4server/internal/config"
	"rf4server/internal/metrics"
	"rf4server/internal/proto"
)

// realtime 指标。
var (
	rtConnsActive  prometheus.Gauge
	rtFramesIn     *prometheus.CounterVec
	rtFramesOut    prometheus.Counter
	rtAcksSent     prometheus.Counter
	rtAuthFailures prometheus.Counter
	rtHandoffSaved prometheus.Counter
	rtHandoffUsed  prometheus.Counter
)

func initRealtimeMetrics(reg *prometheus.Registry) {
	rtConnsActive = metrics.NewGauge(reg, "gateway_realtime_active", "当前 realtime 连接数")
	rtFramesIn = metrics.NewLabeledCounter(reg, "gateway_realtime_frames_in_total", "客户端入帧数", "kind")
	rtFramesOut = metrics.NewCounter(reg, "gateway_realtime_frames_out_total", "服务端出帧数")
	rtAcksSent = metrics.NewCounter(reg, "gateway_realtime_acks_total", "ACK 帧数")
	rtAuthFailures = metrics.NewCounter(reg, "gateway_realtime_auth_failures_total", "认证失败数")
	rtHandoffSaved = metrics.NewCounter(reg, "gateway_realtime_handoff_saved_total", "重连状态保存数")
	rtHandoffUsed = metrics.NewCounter(reg, "gateway_realtime_handoff_used_total", "重连状态恢复数")
}

// obsRegistry 观测端口缺省时用本地一次性注册表（测试场景），保证指标永不 nil。
func obsRegistry(obs *metrics.Server) *prometheus.Registry {
	if obs != nil {
		return obs.Registry()
	}
	return prometheus.NewRegistry()
}

// RealtimeServer TCP :9341 realtime 接入服务。
type RealtimeServer struct {
	cfg    config.Gateway
	store  Store
	log    *slog.Logger
	tm     *TunnelManager
	ln     net.Listener
	sem    chan struct{}
	sids   sync.Map // sid -> *clientSession
	sidSeq atomic.Uint64
}

// NewRealtimeServer 构建 realtime 服务。
func NewRealtimeServer(cfg config.Gateway, store Store, log *slog.Logger, tm *TunnelManager, obs *metrics.Server) *RealtimeServer {
	initRealtimeMetrics(obsRegistry(obs))
	return &RealtimeServer{
		cfg:   cfg,
		store: store,
		log:   log,
		tm:    tm,
		sem:   make(chan struct{}, cfg.MaxConns),
	}
}

// Listen 绑定端口。
func (s *RealtimeServer) Listen() error {
	ln, err := net.Listen("tcp", s.cfg.RealtimeAddr)
	if err != nil {
		return fmt.Errorf("realtime 监听 %s: %w", s.cfg.RealtimeAddr, err)
	}
	s.ln = ln
	s.log.Info("realtime TCP 监听", "addr", s.cfg.RealtimeAddr)
	return nil
}

// RealAddr 实际监听地址（Listen 后有效）。
func (s *RealtimeServer) RealAddr() string {
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// Serve 接受循环（阻塞）。
func (s *RealtimeServer) Serve() error {
	for {
		nc, err := s.ln.Accept()
		if err != nil {
			return err
		}
		s.sem <- struct{}{}
		go func() {
			defer func() { <-s.sem }()
			s.handleConn(nc)
		}()
	}
}

// Close 停止接受。
func (s *RealtimeServer) Close() error { return s.ln.Close() }

// byID 按 sid 取会话。
func (s *RealtimeServer) byID(sid uint64) *clientSession {
	v, ok := s.sids.Load(sid)
	if !ok {
		return nil
	}
	return v.(*clientSession)
}

// dropAll 断开全部在线会话（隧道断开时调用；客户端会自动重连并经 handoff 恢复）。
func (s *RealtimeServer) dropAll(reason string) {
	s.sids.Range(func(_, v any) bool {
		v.(*clientSession).Close(reason)
		return true
	})
}

// outbound 写任务。
type outbound struct {
	kind    int // 0=data(明文信封) 1=ack(wireID) 2=finish(保存 handoff 后退出)
	payload []byte
	wireID  uint64
}

const (
	outData   = 0
	outAck    = 1
	outFinish = 2
)

// clientSession 一条客户端 realtime 会话。
type clientSession struct {
	id      uint64
	conn    net.Conn
	token   string
	userid  int64
	rc4In   *proto.RC4Stream
	rc4Out  *proto.RC4Stream
	resumed bool
	// nextWireID 仅由写者 goroutine 消费；重连恢复时从 handoff 注入，
	// 保持 wire_id 严格递增。
	nextWireID uint64
	outCh      chan outbound
	closed     atomic.Bool
	srv        *RealtimeServer
	log        *slog.Logger

	rc4Mu          sync.Mutex   // 保护 Export（读/写协程之外的周期性快照）
	framesSent     atomic.Int64 // 已发 S→C 数据帧数（防污染守卫）
	framesParsedOK atomic.Int64 // 已成功解析的 C→S 信封数
}

func (s *RealtimeServer) handleConn(nc net.Conn) {
	rtConnsActive.Inc()
	defer rtConnsActive.Dec()
	defer nc.Close()
	remote := nc.RemoteAddr().String()
	log := s.log.With("remote", remote)

	// ---- 握手阶段（认证包 → 问候 → hermes）----
	_ = nc.SetReadDeadline(time.Now().Add(30 * time.Second))
	var buf []byte
	token, consumed, ok := proto.TryParseAuthPacket(buf)
	for !ok {
		chunk := make([]byte, 4096)
		n, err := nc.Read(chunk)
		if err != nil {
			log.Info("认证阶段连接关闭", "buffered", len(buf))
			return
		}
		buf = append(buf, chunk[:n]...)
		token, consumed, ok = proto.TryParseAuthPacket(buf)
		if ok {
			break
		}
		if len(buf) > 1<<20 {
			log.Warn("认证包超长，拒绝", "len", len(buf))
			rtAuthFailures.Inc()
			return
		}
	}
	reconnect := proto.AuthPacketReconnect(buf)
	buf = buf[consumed:]

	info, found, err := s.store.LookupToken(context.Background(), token)
	if err != nil {
		rtAuthFailures.Inc()
		log.Warn("token 校验查询异常，拒绝", "err", err, "reconnect", reconnect)
		return
	}
	if !found {
		// 客户端可能回传未登记的 logintoken。只要格式合法（至少 3 个 '|'），
		// 就从第三段解析 uid 并放行。
		uid := useridFromTokenStr(token)
		if uid <= 0 {
			rtAuthFailures.Inc()
			log.Warn("token 无法解析 uid，拒绝", "reconnect", reconnect)
			return
		}
		info = TokenInfo{Userid: uid, IssuedAt: time.Now().Unix()}
		log.Warn("token 未登记，按解析 uid 放行", "uid", uid, "reconnect", reconnect)
	}
	log = log.With("userid", info.Userid)
	log.Info("认证通过",
		"token_len", len(token), "reconnect", reconnect)

	if _, err := nc.Write([]byte(GreetingUUID)); err != nil {
		log.Info("写问候失败", "err", err)
		return
	}

	// hermes 帧（明文，payload 含 <hermes>）
	hermesSeen := false
	for !hermesSeen {
		frames, consumedBytes, err := proto.TakeCompleteFrames(buf)
		if err != nil {
			log.Warn("hermes 阶段帧解析失败", "err", err)
			return
		}
		buf = buf[consumedBytes:]
		for _, f := range frames {
			if f.FrameType == 0 && contains(f.Payload, hermesMarker) {
				hermesSeen = true
			}
		}
		if hermesSeen {
			break
		}
		chunk := make([]byte, 8192)
		n, err := nc.Read(chunk)
		if err != nil {
			log.Info("hermes 阶段连接关闭", "err", err)
			return
		}
		buf = append(buf, chunk[:n]...)
	}
	log.Info("Hermes 握手完成")

	// ---- RC4 初始化（重连接管优先）----
	sess := &clientSession{
		id:         s.sidSeq.Add(1),
		conn:       nc,
		token:      token,
		userid:     info.Userid,
		outCh:      make(chan outbound, 1024),
		nextWireID: 1,
		srv:        s,
		log:        log,
	}
	key := []byte(token)
	if reconnect {
		if h, err := s.store.LoadHandoff(context.Background(), token); err == nil && h != nil {
			sess.rc4In = proto.Restore(h.RC4In)
			sess.rc4Out = proto.Restore(h.RC4Out)
			sess.nextWireID = h.NextWireID
			if sess.nextWireID < 1 {
				sess.nextWireID = 1
			}
			sess.resumed = true
			rtHandoffUsed.Inc()
			log.Info("realtime 重连成功：承接既有 RC4 密码流", "next_wire_id", sess.nextWireID)
			_ = s.store.DeleteHandoff(context.Background(), token)
		}
	}
	if sess.rc4In == nil {
		var err error
		sess.rc4In, err = proto.NewRC4(key)
		if err != nil {
			log.Error("RC4 初始化失败", "err", err)
			return
		}
		sess.rc4Out, _ = proto.NewRC4(key)
	}

	s.sids.Store(sess.id, sess)
	defer s.sids.Delete(sess.id)

	// 通知主服开新会话
	if err := s.tm.sendOpen(sess.id, token, sess.userid, reconnect, remoteIP(remote)); err != nil {
		log.Error("隧道 OPEN 失败，断开客户端", "err", err)
		return
	}

	// ---- 单写者 goroutine ----
	writerDone := make(chan struct{})
	go sess.writer(writerDone)

	// ---- 读循环 ----
	_ = nc.SetReadDeadline(time.Now().Add(120 * time.Second))
	readBuf := buf // hermes 阶段可能残留后续帧
	for {
		frames, consumed, err := proto.TakeCompleteFrames(readBuf)
		if err != nil {
			log.Warn("帧解析失败，断开", "err", err)
			break
		}
		readBuf = readBuf[consumed:]
		done := false
		for _, f := range frames {
			if sess.handleFrame(f) {
				done = true
				break
			}
		}
		if done {
			break
		}
		chunk := make([]byte, 65536)
		n, err := nc.Read(chunk)
		if err != nil {
			log.Info("客户端断开", "err", err)
			break
		}
		readBuf = append(readBuf, chunk[:n]...)
		if len(readBuf) > 64<<20 {
			log.Warn("读缓冲超限，断开")
			break
		}
		_ = nc.SetReadDeadline(time.Now().Add(120 * time.Second))
	}

	sess.closed.Store(true)
	close(sess.outCh)
	<-writerDone // 写者收尾（含 handoff 保存）
	_ = s.tm.sendClose(sess.id, "session_end")
	s.log.Info("会话结束", "sid", sess.id, "resumed", sess.resumed)
}

// useridFromTokenStr 从 4 段 token 第三段解析 uid（解析失败回 0）。
func useridFromTokenStr(token string) int64 {
	parts := strings.Split(token, "|")
	if len(parts) < 3 {
		return 0
	}
	uid, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return 0
	}
	return uid
}

// handleFrame 处理一个客户端帧；返回 true 表示连接应结束。
func (s *clientSession) handleFrame(f *proto.Frame) bool {
	switch {
	case f.BodyLen == 0:
		// 零帧：传输层保活，常由服务端发往客户端；收到时也容忍。
		rtFramesIn.WithLabelValues("zero").Inc()
		return false
	case f.BodyLen == 1:
		rtFramesIn.WithLabelValues("type_only").Inc()
		return false
	case f.FrameType == 1:
		// 客户端 ACK 我们的数据帧：记录即可
		rtFramesIn.WithLabelValues("ack").Inc()
		return false
	case f.FrameType == 0:
		rtFramesIn.WithLabelValues("data").Inc()
		// 协议要求先 ACK 后处理；ACK 无载荷，不消耗 keystream。
		if s.srv.cfg.AckClientFrames {
			s.outCh <- outbound{kind: outAck, wireID: f.WireID}
			rtAcksSent.Inc()
		}
		plain := s.rc4In.Crypt(f.Payload)
		if err := s.srv.tm.sendData(s.id, plain); err != nil {
			s.log.Warn("隧道转发失败，断开", "err", err)
			return true
		}
		s.framesParsedOK.Add(1)
		return false
	default:
		rtFramesIn.WithLabelValues("unknown_type").Inc()
		s.log.Warn("未知帧类型", "type", f.FrameType)
		return false
	}
}

// writer 是会话的唯一写者，依次执行分配 wire_id、RC4 加密和写 socket。
// call_id 分配、RC4 消耗与发送必须保持同一顺序；主服经隧道 FIFO 下发完整信封，
// 写者只负责 wire_id、加密和输出。
func (s *clientSession) writer(done chan<- struct{}) {
	defer close(done)
	keepalive := time.NewTicker(keepaliveInterval(s.srv.cfg.TransportKeepaliveSec))
	defer keepalive.Stop()

	nextWireID := s.nextWireID
	_ = s.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))

	// 周期性保存 handoff，避免进程异常退出时丢失 RC4 流和 wire_id。
	periodicSave := time.NewTicker(2 * time.Second)
	defer periodicSave.Stop()
	go func() {
		for range periodicSave.C {
			if s.closed.Load() {
				return
			}
			s.saveHandoff(nextWireID) // nextWireID 由写者独占，此处取快照即可
		}
	}()

	for task := range s.outCh {
		switch task.kind {
		case outAck:
			if _, err := s.conn.Write(proto.BuildAckFrame(task.wireID)); err != nil {
				s.log.Info("写 ACK 失败", "err", err)
				s.closed.Store(true)
				continue
			}
			_ = s.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))

		case outData:
			enc := s.rc4Out.Crypt(task.payload)
			frame := proto.BuildFrame(0, nextWireID, enc)
			nextWireID++
			if _, err := s.conn.Write(frame); err != nil {
				s.log.Info("写数据帧失败", "err", err)
				s.closed.Store(true)
				continue
			}
			s.framesSent.Add(1)
			rtFramesOut.Inc()
			_ = s.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))

		case outFinish:
			// 不会出现在 channel（close(outCh) 退出）；防御
		}

		if s.closed.Load() {
			break
		}
	}

	// 收尾：保存重连接管状态（RC4 双向流 + 下一 wire_id）
	s.saveHandoff(nextWireID)
}

// saveHandoff 保存重连接管状态。非 resume 的会话如果尚未发送数据帧、
// 也尚未解析出客户端信封，则说明它还没有可恢复进度；此时不落库，
// 防止空状态覆盖已有的有效 handoff。
func (s *clientSession) saveHandoff(nextWireID uint64) {
	if s.rc4In == nil || s.rc4Out == nil {
		return
	}
	if !s.resumed && s.framesSent.Load() == 0 && s.framesParsedOK.Load() == 0 {
		return // 防污染：失步 fresh 会话的状态不落库（见上注）
	}
	s.rc4Mu.Lock()
	h := Handoff{
		RC4In:      s.rc4In.Export(),
		RC4Out:     s.rc4Out.Export(),
		NextWireID: nextWireID,
	}
	s.rc4Mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.srv.store.SaveHandoff(ctx, s.token, h, handoffTTL); err == nil {
		rtHandoffSaved.Inc()
		s.log.Debug("handoff 已保存", "next_wire_id", nextWireID)
	} else {
		s.log.Warn("handoff 保存失败", "err", err)
	}
}

// EnqueueData 主服下行的明文信封入队（隧道读 goroutine 调用）。
func (s *clientSession) EnqueueData(payload []byte) bool {
	if s.closed.Load() {
		return false
	}
	select {
	case s.outCh <- outbound{kind: outData, payload: payload}:
		return true
	default:
		s.log.Warn("下行队列满（慢客户端），断开")
		s.closed.Store(true)
		return false
	}
}

// Close 主动断开（主服 CLOSE 指令）。
func (s *clientSession) Close(reason string) {
	if s.closed.CompareAndSwap(false, true) {
		s.log.Info("主服要求断开", "reason", reason)
		_ = s.conn.Close()
	}
}

// ---- 辅助 ----

var hermesMarker = []byte("<hermes>")

func contains(haystack, needle []byte) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle []byte) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func remoteIP(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

func keepaliveInterval(cfg float64) time.Duration {
	if cfg <= 0 {
		return 5 * time.Second
	}
	return time.Duration(cfg * float64(time.Second))
}

// handoffTTL 是重连接管状态的 TTL；客户端断线后约 2 分钟内会重连。
const handoffTTL = 10 * time.Minute

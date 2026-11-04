package gateway

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"rf4server/internal/metrics"
	"rf4server/internal/tunnel"
)

// 隧道指标。
var (
	tlUp        prometheus.Gauge
	tlSent      *prometheus.CounterVec
	tlRecv      *prometheus.CounterVec
	tlDialTotal prometheus.Counter
)

func initTunnelMetrics(reg *prometheus.Registry) {
	tlUp = metrics.NewGauge(reg, "gateway_tunnel_up", "隧道是否在线（1/0）")
	tlSent = metrics.NewLabeledCounter(reg, "gateway_tunnel_sent_total", "隧道发送记录数", "type")
	tlRecv = metrics.NewLabeledCounter(reg, "gateway_tunnel_recv_total", "隧道接收记录数", "type")
	tlDialTotal = metrics.NewCounter(reg, "gateway_tunnel_dials_total", "隧道拨号次数")
}

// TunnelManager 网关侧隧道客户端：维持到 gameserver 的连接，收发会话记录。
//
// 隧道断开策略：杀掉全部会话的客户端连接（客户端会自动重连并经 handoff 恢复），
// 然后带退避重拨。这是最保守且协议安全的做法——主服侧会话随隧道丢失而丢失，
// 但 RC4/计数器 handoff 保证客户端无感续玩。
type TunnelManager struct {
	addr string
	psk  string
	log  *slog.Logger
	gw   *RealtimeServer

	mu      sync.Mutex
	conn    *tunnel.Conn
	retryN  int
	onState func(up bool)
}

// NewTunnelManager 构建隧道管理器（不拨号；首次 sendOpen 时懒拨）。
func NewTunnelManager(addr, psk string, log *slog.Logger, obs *metrics.Server, onState func(up bool)) *TunnelManager {
	initTunnelMetrics(obsRegistry(obs))
	tlUp.Set(0)
	return &TunnelManager{addr: addr, psk: psk, log: log, onState: onState}
}

// SetGateway 绑定 realtime 服务（收下行记录时派发）。
func (m *TunnelManager) SetGateway(gw *RealtimeServer) { m.gw = gw }

// getConn 取当前隧道连接；不在则拨号（含退避）。
func (m *TunnelManager) getConn() (*tunnel.Conn, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conn != nil {
		select {
		case <-m.conn.Done():
			m.conn = nil
		default:
			return m.conn, nil
		}
	}
	// 指数退避（1s..30s），拨号期间持锁串行化，避免惊群
	backoff := time.Duration(1<<min(m.retryN, 5)) * time.Second
	if m.retryN > 0 {
		m.log.Info("隧道重连退避", "wait", backoff)
		time.Sleep(backoff)
	}
	c, err := tunnel.Dial(m.addr, m.psk)
	if err != nil {
		m.retryN++
		return nil, err
	}
	m.retryN = 0
	m.conn = c
	tlUp.Set(1)
	tlDialTotal.Inc()
	m.log.Info("隧道已连接", "addr", m.addr, "remote", c.RemoteAddr())
	if m.onState != nil {
		m.onState(true)
	}
	go m.readLoop(c)
	return c, nil
}

// readLoop 读隧道下行记录并派发到会话。
func (m *TunnelManager) readLoop(c *tunnel.Conn) {
	defer func() {
		tlUp.Set(0)
		if m.onState != nil {
			m.onState(false)
		}
		m.log.Warn("隧道断开：杀掉全部在线会话（客户端将自动重连并恢复）")
		c.Close()
		m.mu.Lock()
		if m.conn == c {
			m.conn = nil
			m.retryN++
		}
		m.mu.Unlock()
		if m.gw != nil {
			m.gw.dropAll("tunnel_down")
		}
	}()
	for {
		rt, sid, payload, err := c.ReadRecord()
		if err != nil {
			return
		}
		tlRecv.WithLabelValues(rt.String()).Inc()
		switch rt {
		case tunnel.TypeData:
			if m.gw == nil {
				continue
			}
			if s := m.gw.byID(sid); s != nil {
				s.EnqueueData(payload)
			}
		case tunnel.TypeClose:
			if m.gw != nil {
				if s := m.gw.byID(sid); s != nil {
					s.Close(string(payload))
				}
			}
		case tunnel.TypePing:
			_ = c.WriteRecord(tunnel.TypePong, 0, nil)
		case tunnel.TypeOpen, tunnel.TypePong:
			// 网关不收 OPEN；PONG 忽略
		}
	}
}

// sendOpen 发送 OPEN。
func (m *TunnelManager) sendOpen(sid uint64, token string, userid int64, reconnect bool, clientIP string) error {
	op := tunnel.OpenPayload{Token: token, Userid: userid, Reconnect: reconnect, ClientIP: clientIP}
	b, err := json.Marshal(op)
	if err != nil {
		return err
	}
	return m.send(tunnel.TypeOpen, sid, b)
}

// sendData 转发客户端明文信封。
func (m *TunnelManager) sendData(sid uint64, payload []byte) error {
	return m.send(tunnel.TypeData, sid, payload)
}

// sendClose 通知主服会话结束。
func (m *TunnelManager) sendClose(sid uint64, reason string) error {
	return m.send(tunnel.TypeClose, sid, []byte(reason))
}

func (m *TunnelManager) send(rt tunnel.RecordType, sid uint64, payload []byte) error {
	c, err := m.getConn()
	if err != nil {
		return err
	}
	if err := c.WriteRecord(rt, sid, payload); err != nil {
		m.log.Warn("隧道发送失败", "type", rt, "err", err)
		_ = c.Close()
		m.mu.Lock()
		if m.conn == c {
			m.conn = nil
		}
		m.mu.Unlock()
		return err
	}
	tlSent.WithLabelValues(rt.String()).Inc()
	return nil
}

// Close 关闭当前隧道。
func (m *TunnelManager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conn != nil {
		_ = m.conn.Close()
		m.conn = nil
	}
}

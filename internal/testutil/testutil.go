// Package testutil 提供跨包集成测试脚手架：进程内网关装配 + 假客户端。
package testutil

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"rf4server/internal/config"
	"rf4server/internal/gateway"
	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

// TestPSK 测试隧道 PSK。
const TestPSK = "test-psk"

// GatewayHandle 进程内网关句柄。
type GatewayHandle struct {
	LoginURL      string
	LoginPlainURL string
	RealAddr      string
	Store         *gateway.MemStore
}

// StartGateway 在随机端口装配一个进程内网关（MemStore + 指定隧道地址）。
func StartGateway(t *testing.T, tunnelAddr, psk string) *GatewayHandle {
	t.Helper()
	cfg := config.Gateway{
		LoginAddr:             "127.0.0.1:0",
		LoginCompatAddr:       "127.0.0.1:0",
		RealtimeAddr:          "127.0.0.1:0",
		CertFile:              t.TempDir() + "/cert.pem",
		KeyFile:               t.TempDir() + "/key.pem",
		AdvertiseHost:         "127.0.0.1",
		Region:                "CN",
		ServerName:            "RF4_566",
		LoginDomain:           "api.rf4game.ru",
		MaxConns:              32,
		AckClientFrames:       true,
		TransportKeepaliveSec: 3600,
		TokenTTL:              1,
	}
	if err := gateway.EnsureCert(cfg.CertFile, cfg.KeyFile, cfg.LoginDomain); err != nil {
		t.Fatal(err)
	}
	store := gateway.NewMemStore(1000001)
	log := logging.Discard()
	tm := gateway.NewTunnelManager(tunnelAddr, psk, log, nil, nil)
	rt := gateway.NewRealtimeServer(cfg, store, log, tm, nil)
	if err := rt.Listen(); err != nil {
		t.Fatal(err)
	}
	tm.SetGateway(rt)
	go func() { _ = rt.Serve() }()
	login, err := gateway.NewLoginServer(cfg, store, log, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := login.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = rt.Close()
		_ = login.Shutdown(context.Background())
		tm.Close()
	})
	return &GatewayHandle{
		LoginURL:      "https://" + login.Addr() + "/login.php",
		LoginPlainURL: "http://" + login.CompatAddr() + "/login.php",
		RealAddr:      rt.RealAddr(),
		Store:         store,
	}
}

// Login HTTP 登录取 token/userid。
func (g *GatewayHandle) Login(t *testing.T) (token string, userid int64) {
	t.Helper()
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	httpc := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	resp, err := httpc.Get(g.LoginURL)
	if err != nil {
		t.Fatalf("登录请求: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	text := string(body)
	if !strings.Contains(text, `<logon success="true">`) {
		t.Fatalf("登录响应异常: %s", text)
	}
	m := regexp.MustCompile(`<token>([^<]+)</token>`).FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("无 token: %s", text)
	}
	mu := regexp.MustCompile(`<userid>(\d+)</userid>`).FindStringSubmatch(text)
	if mu == nil {
		t.Fatalf("无 userid: %s", text)
	}
	fmt.Sscanf(mu[1], "%d", &userid)
	return m[1], userid
}

// FakeEvent 假客户端收到的一帧事件。
type FakeEvent struct {
	Kind      string // "ack" | "resp" | "push" | "zero" | "other"
	AckWire   uint64
	Env       *proto.Envelope
	PlainText []byte
}

// FakeClient 假 RF4 客户端：全握手 + RC4 + 帧泵。
// 跨重连保留 RC4 流（与客户端行为一致）；跨连接共享状态用锁保护。
type FakeClient struct {
	t      *testing.T
	addr   string
	conn   net.Conn
	rc4In  *proto.RC4Stream // 客户端发送流
	rc4Out *proto.RC4Stream // 客户端接收流
	wire   uint64
	bufMu  sync.Mutex
	buf    []byte

	// wireMonotonic 违规检测（服务端出帧 wire_id 必须严格递增，跨重连续号）
	lastOutWire uint64
	WireBad     uint64

	// respCh/pushCh 由泵填充（非阻塞发送，无关闭语义，重连安全）
	RespCh chan *proto.Envelope
	PushCh chan *proto.Envelope
	AckCh  chan uint64
}

// NewFakeClient 构建假客户端。
func NewFakeClient(t *testing.T, addr string) *FakeClient {
	return &FakeClient{
		t:      t,
		addr:   addr,
		RespCh: make(chan *proto.Envelope, 64),
		PushCh: make(chan *proto.Envelope, 64),
		AckCh:  make(chan uint64, 64),
	}
}

// Dial 建连 + 全握手（认证/问候/hermes），并启动读帧泵。
func (c *FakeClient) Dial(token string, reconnect bool) {
	c.t.Helper()
	conn, err := net.DialTimeout("tcp", c.addr, 3*time.Second)
	if err != nil {
		c.t.Fatal(err)
	}
	c.conn = conn
	flag := byte(0x00)
	if reconnect {
		flag = 0x01
	}
	auth := append([]byte{proto.AuthMagic, flag}, binary.LittleEndian.AppendUint32(nil, uint32(len(token)))...)
	auth = append(auth, token...)
	if _, err := conn.Write(auth); err != nil {
		c.t.Fatal(err)
	}
	hdr := make([]byte, 36)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(conn, hdr); err != nil {
		c.t.Fatalf("读问候: %v", err)
	}
	if string(hdr) != proto.GreetingUUID {
		c.t.Fatalf("问候不符: %q", hdr)
	}
	hermes := []byte("<hermes><token>x</token></hermes>")
	if _, err := conn.Write(proto.BuildFrame(0, 1, hermes)); err != nil {
		c.t.Fatal(err)
	}
	if c.rc4In == nil {
		c.rc4In, _ = proto.NewRC4([]byte(token))
		c.rc4Out, _ = proto.NewRC4([]byte(token))
	}
	go c.pump()
}

func (c *FakeClient) pump() {
	conn := c.conn // 本泵只读自己那一条连接
	for {
		c.bufMu.Lock()
		stale := c.conn != conn
		c.bufMu.Unlock()
		if stale {
			return // 连接已被重连替换，旧泵退出
		}
		c.bufMu.Lock()
		frames, consumed, err := proto.TakeCompleteFrames(c.buf)
		if err != nil {
			c.bufMu.Unlock()
			return
		}
		c.buf = c.buf[consumed:]
		c.bufMu.Unlock()
		for _, f := range frames {
			ev := FakeEvent{Kind: "other"}
			switch {
			case f.BodyLen == 0:
				ev.Kind = "zero"
			case f.FrameType == 1:
				ev.Kind = "ack"
				ev.AckWire = f.WireID
				select {
				case c.AckCh <- f.WireID:
				default:
				}
			case f.FrameType == 0 && f.BodyLen >= 9:
				if f.WireID <= c.lastOutWire {
					c.WireBad++
				}
				c.lastOutWire = f.WireID
				plain := c.rc4Out.Crypt(f.Payload)
				env, ok := proto.ParseEnvelope(plain)
				if !ok {
					ev.Kind = "undecryptable"
					ev.PlainText = plain
				} else if env.Marker == -2 {
					ev.Kind = "resp"
					select {
					case c.RespCh <- env:
					default:
					}
				} else if env.Marker == -1 {
					ev.Kind = "push"
					select {
					case c.PushCh <- env:
					default:
					}
				}
			}
			_ = ev
		}
		chunk := make([]byte, 65536)
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		n, err := conn.Read(chunk)
		if err != nil {
			return
		}
		c.bufMu.Lock()
		c.buf = append(c.buf, chunk[:n]...)
		c.bufMu.Unlock()
	}
}

// SendRequest 发加密请求帧。
func (c *FakeClient) SendRequest(callID uint32, main, sub uint8, body ...byte) {
	c.t.Helper()
	if len(body) == 0 {
		body = []byte{0x03, 0x03}
	}
	payload := proto.BuildRequestEnvelope(callID, main, sub, append(body, 0x00))
	c.wire++
	enc := c.rc4In.Crypt(payload)
	if _, err := c.conn.Write(proto.BuildFrame(0, c.wire, enc)); err != nil {
		c.t.Fatal(err)
	}
}

// WaitResponse 等一个响应信封（带超时）。
func (c *FakeClient) WaitResponse(timeout time.Duration) *proto.Envelope {
	c.t.Helper()
	select {
	case env := <-c.RespCh:
		return env
	case <-time.After(timeout):
		c.t.Fatal("等待响应超时")
		return nil
	}
}

// WaitPush 等一个推送信封（带超时）。
func (c *FakeClient) WaitPush(timeout time.Duration) *proto.Envelope {
	c.t.Helper()
	select {
	case env := <-c.PushCh:
		return env
	case <-time.After(timeout):
		c.t.Fatal("等待推送超时")
		return nil
	}
}

// WaitAck 等指定 wire 的 ACK。
func (c *FakeClient) WaitAck(wire uint64, timeout time.Duration) {
	c.t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case w := <-c.AckCh:
			if w == wire {
				return
			}
		case <-deadline:
			c.t.Fatalf("等待 ACK(wire=%d) 超时", wire)
			return
		}
	}
}

// Close 断开。
func (c *FakeClient) Close() { _ = c.conn.Close() }

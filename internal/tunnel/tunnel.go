// Package tunnel 实现网关↔主服务器的内部二进制隧道。
//
// 线路格式（握手后，小端）：
//
//	[u32 total_len][u8 type][u64 session_id][payload]   total_len = 9 + len(payload)
//
// 记录类型：
//
//	0x01 OPEN   G→M  开新会话（payload = JSON: token/reconnect/client_ip）
//	0x02 DATA   双向 明文 RPC 信封字节（G→M 来自客户端；M→G 发往客户端）
//	0x03 CLOSE  双向 关会话（payload = 原因串）
//	0x04 PING / 0x05 PONG  隧道保活
//
// 握手：双方互发 magic "RF4T1" + HMAC-SHA256(psk, "RF4T1:<role>")，
// 任一方校验失败立即断开。该握手适用于内网/回环部署；公网部署应改用 mTLS。
//
// 同一 session 的 DATA 记录必须按序传递：
// 单条 TCP 连接天然 FIFO；主服对每会话的下行也必须按序写（单写者）。
package tunnel

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

const (
	Magic   = "RF4T1"
	Version = 1

	maxRecordPayload = 32 << 20 // 32MB（1/4 LZMA 大包 ~2.3MB，留余量）
	handshakeTimeout = 5 * time.Second
)

// RecordType 记录类型。
type RecordType uint8

const (
	TypeOpen  RecordType = 0x01
	TypeData  RecordType = 0x02
	TypeClose RecordType = 0x03
	TypePing  RecordType = 0x04
	TypePong  RecordType = 0x05
)

func (t RecordType) String() string {
	switch t {
	case TypeOpen:
		return "OPEN"
	case TypeData:
		return "DATA"
	case TypeClose:
		return "CLOSE"
	case TypePing:
		return "PING"
	case TypePong:
		return "PONG"
	}
	return fmt.Sprintf("0x%02x", uint8(t))
}

// OpenPayload OPEN 记录的 payload。
type OpenPayload struct {
	Token     string `json:"token"`
	Userid    int64  `json:"userid"`
	Reconnect bool   `json:"reconnect"`
	ClientIP  string `json:"client_ip"`
}

var ErrClosed = errors.New("tunnel: connection closed")

// Conn 一条隧道连接。
type Conn struct {
	nc   net.Conn
	wmu  sync.Mutex
	name string

	closeOnce sync.Once
	closed    chan struct{}
}

// HandshakeClient 客户端（gateway）侧握手。
func HandshakeClient(nc net.Conn, psk string) (*Conn, error) {
	if err := nc.SetDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		return nil, err
	}
	if _, err := nc.Write([]byte(Magic)); err != nil {
		return nil, err
	}
	if _, err := nc.Write(mac(psk, "gateway")); err != nil {
		return nil, err
	}
	buf := make([]byte, len(Magic)+32)
	if _, err := io.ReadFull(nc, buf); err != nil {
		return nil, fmt.Errorf("tunnel: 读服务端握手: %w", err)
	}
	if string(buf[:len(Magic)]) != Magic {
		return nil, errors.New("tunnel: 服务端 magic 不符")
	}
	if !hmac.Equal(buf[len(Magic):], mac(psk, "gameserver")) {
		return nil, errors.New("tunnel: 服务端 PSK 校验失败")
	}
	return newConn(nc, "client"), nil
}

// HandshakeServer 服务端（gameserver）侧握手。
func HandshakeServer(nc net.Conn, psk string) (*Conn, error) {
	if err := nc.SetDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		return nil, err
	}
	head := make([]byte, len(Magic)+32)
	if _, err := io.ReadFull(nc, head); err != nil {
		return nil, fmt.Errorf("tunnel: 读客户端握手: %w", err)
	}
	if string(head[:len(Magic)]) != Magic {
		return nil, errors.New("tunnel: 客户端 magic 不符")
	}
	if !hmac.Equal(head[len(Magic):], mac(psk, "gateway")) {
		return nil, errors.New("tunnel: 客户端 PSK 校验失败")
	}
	if _, err := nc.Write([]byte(Magic)); err != nil {
		return nil, err
	}
	if _, err := nc.Write(mac(psk, "gameserver")); err != nil {
		return nil, err
	}
	return newConn(nc, "server"), nil
}

func mac(psk, role string) []byte {
	h := hmac.New(sha256.New, []byte(psk))
	h.Write([]byte(Magic))
	h.Write([]byte{0})
	h.Write([]byte(role))
	return h.Sum(nil)
}

func newConn(nc net.Conn, name string) *Conn {
	c := &Conn{nc: nc, name: name, closed: make(chan struct{})}
	_ = nc.SetDeadline(time.Time{}) // 清握手超时
	return c
}

// WriteRecord 写一条记录（并发安全）。
func (c *Conn) WriteRecord(t RecordType, sessionID uint64, payload []byte) error {
	select {
	case <-c.closed:
		return ErrClosed
	default:
	}
	if len(payload) > maxRecordPayload {
		return fmt.Errorf("tunnel: payload 过大 %d", len(payload))
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	head := make([]byte, 0, 13)
	head = binary.LittleEndian.AppendUint32(head, uint32(9+len(payload)))
	head = append(head, byte(t))
	head = binary.LittleEndian.AppendUint64(head, sessionID)
	if _, err := c.nc.Write(head); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := c.nc.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

// ReadRecord 读一条记录（阻塞）。并发读不安全：每条连接约定单读 goroutine。
func (c *Conn) ReadRecord() (RecordType, uint64, []byte, error) {
	var head [13]byte
	if _, err := io.ReadFull(c.nc, head[:]); err != nil {
		return 0, 0, nil, err
	}
	total := binary.LittleEndian.Uint32(head[0:4])
	if total < 9 || total-9 > maxRecordPayload {
		return 0, 0, nil, fmt.Errorf("tunnel: 非法记录长度 %d", total)
	}
	payload := make([]byte, total-9)
	if _, err := io.ReadFull(c.nc, payload); err != nil {
		return 0, 0, nil, err
	}
	return RecordType(head[4]), binary.LittleEndian.Uint64(head[5:13]), payload, nil
}

// Close 关闭连接（幂等）。
func (c *Conn) Close() error {
	c.closeOnce.Do(func() { close(c.closed); _ = c.nc.Close() })
	return nil
}

// Done 连接关闭通知。
func (c *Conn) Done() <-chan struct{} { return c.closed }

// RemoteAddr 对端地址。
func (c *Conn) RemoteAddr() string {
	if c.nc == nil {
		return ""
	}
	return c.nc.RemoteAddr().String()
}

// Name 连接角色名。
func (c *Conn) Name() string { return c.name }

// Dial 拨号并完成客户端握手。
func Dial(addr, psk string) (*Conn, error) {
	nc, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("tunnel: 拨号 %s: %w", addr, err)
	}
	c, err := HandshakeClient(nc, psk)
	if err != nil {
		_ = nc.Close()
		return nil, err
	}
	return c, nil
}

// Serve 阻塞接受隧道连接，每条交给 handler。
func Serve(ln net.Listener, psk string, handler func(*Conn)) error {
	for {
		nc, err := ln.Accept()
		if err != nil {
			return err
		}
		go func() {
			c, err := HandshakeServer(nc, psk)
			if err != nil {
				_ = nc.Close()
				return
			}
			handler(c)
		}()
	}
}

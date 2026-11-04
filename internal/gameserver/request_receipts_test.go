package gameserver

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/tunnel"
)

type receiptTestSession struct {
	sess      *Session
	peer      *tunnel.Conn
	responses chan *proto.Envelope
}

func newReceiptTestServer(t *testing.T, dbPath string) (*Server, *store.DB) {
	t.Helper()
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	srv := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "receipt-test"}}, logging.Discard(), nil)
	srv.SetDB(db)
	return srv, db
}

func newReceiptTestSession(t *testing.T, srv *Server, sid uint64, token string) *receiptTestSession {
	t.Helper()
	serverNet, peerNet := net.Pipe()
	serverConnCh := make(chan *tunnel.Conn, 1)
	errCh := make(chan error, 1)
	go func() {
		c, err := tunnel.HandshakeServer(serverNet, srv.cfg.Tunnel.PSK)
		if err != nil {
			errCh <- err
			return
		}
		serverConnCh <- c
	}()
	peer, err := tunnel.HandshakeClient(peerNet, srv.cfg.Tunnel.PSK)
	if err != nil {
		t.Fatal(err)
	}
	var serverConn *tunnel.Conn
	select {
	case serverConn = <-serverConnCh:
	case err := <-errCh:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("隧道握手超时")
	}
	h := &receiptTestSession{
		sess:      newSession(srv, sid, token, 1000001, serverConn),
		peer:      peer,
		responses: make(chan *proto.Envelope, 32),
	}
	go func() {
		defer close(h.responses)
		for {
			rt, _, payload, err := peer.ReadRecord()
			if err != nil {
				return
			}
			if rt != tunnel.TypeData {
				continue
			}
			env, ok := proto.ParseEnvelope(payload)
			if ok && (env.Marker == -2 || env.Marker == -3) {
				h.responses <- env
			}
		}
	}()
	t.Cleanup(func() {
		_ = serverConn.Close()
		_ = peer.Close()
	})
	return h
}

func (h *receiptTestSession) send(main, sub uint8, callID uint32, body []byte, expect byte) {
	h.sess.Srv.handleEnvelopePayload(h.sess,
		proto.BuildRequestEnvelope(callID, main, sub, append(append([]byte(nil), body...), expect)))
}

func waitReceiptResponse(t *testing.T, h *receiptTestSession) *proto.Envelope {
	t.Helper()
	select {
	case env, ok := <-h.responses:
		if !ok {
			t.Fatal("响应通道已关闭")
		}
		return env
	case <-time.After(2 * time.Second):
		t.Fatal("等待响应超时")
		return nil
	}
}

func assertNoReceiptResponse(t *testing.T, h *receiptTestSession) {
	t.Helper()
	select {
	case env := <-h.responses:
		t.Fatalf("不应收到响应，实得 marker=%d call=%d body=%x", env.Marker, env.CallID, env.Body)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestRequestReceiptReplayAcrossReconnectAndRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "receipt-replay.db")
	srv1, db1 := newReceiptTestServer(t, dbPath)
	var calls1 atomic.Int32
	want := []byte{0x14, 0x04, 'R', 'E', 'P', 'L'}
	srv1.dispatch.Register(250, 1, func(ctx *Ctx) ([]byte, bool) {
		calls1.Add(1)
		ctx.Reply(want)
		// 同时返回 body 也必须只回一次；Ctx.Reply 优先。
		return emptyArgsBody(), true
	})

	token := "1|2|1000001|receipt"
	requestBody := emptyArgsBody()
	sess1 := newReceiptTestSession(t, srv1, 1, token)
	sess1.send(250, 1, 77, requestBody, 0x00)
	resp := waitReceiptResponse(t, sess1)
	if resp.CallID != 77 || string(resp.Body) != string(want) {
		t.Fatalf("首次响应不符: call=%d body=%x", resp.CallID, resp.Body)
	}
	assertNoReceiptResponse(t, sess1) // Reply + return body 不得双回包

	// 同一连接重试：命中持久化回执，不执行 handler。
	sess1.send(250, 1, 77, requestBody, 0x00)
	resp = waitReceiptResponse(t, sess1)
	if string(resp.Body) != string(want) {
		t.Fatalf("同连接重放响应不符: %x", resp.Body)
	}

	// 重连后的新 Session 共享 token 键，仍只执行一次。
	sess2 := newReceiptTestSession(t, srv1, 2, token)
	sess2.send(250, 1, 77, requestBody, 0x00)
	resp = waitReceiptResponse(t, sess2)
	if string(resp.Body) != string(want) {
		t.Fatalf("重连重放响应不符: %x", resp.Body)
	}
	if got := calls1.Load(); got != 1 {
		t.Fatalf("handler 执行次数=%d，期望 1", got)
	}

	sess1.peer.Close()
	sess2.peer.Close()
	srv1.CloseAll()
	if err := db1.Close(); err != nil {
		t.Fatal(err)
	}

	// 模拟进程重启：新 Server + 新 DB 句柄接同一文件，回执仍可重放。
	srv2, db2 := newReceiptTestServer(t, dbPath)
	defer db2.Close()
	var calls2 atomic.Int32
	srv2.dispatch.Register(250, 1, func(ctx *Ctx) ([]byte, bool) {
		calls2.Add(1)
		return []byte{0x00}, true
	})
	sess3 := newReceiptTestSession(t, srv2, 3, token)
	sess3.send(250, 1, 77, requestBody, 0x00)
	resp = waitReceiptResponse(t, sess3)
	if string(resp.Body) != string(want) || resp.CallID != 77 {
		t.Fatalf("重启后重放响应不符: call=%d body=%x", resp.CallID, resp.Body)
	}
	if got := calls2.Load(); got != 0 {
		t.Fatalf("重启后 handler 不应执行，实得 %d", got)
	}

	key, bodySHA := requestReceiptKey(&Ctx{
		Sess: &Session{Token: token}, Main: 250, Sub: 1, CallID: 77, Body: requestBody,
	})
	rec, err := db2.GetRequestReceipt(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	if rec.BodySHA256 != bodySHA || rec.Status != store.RequestReceiptCompleted || !rec.HasResponse {
		t.Fatalf("持久化回执不符: %+v", rec)
	}
}

func TestRequestReceiptCallIDConflictDoesNotExecute(t *testing.T) {
	srv, db := newReceiptTestServer(t, filepath.Join(t.TempDir(), "receipt-conflict.db"))
	defer db.Close()
	var calls atomic.Int32
	want := emptyArgsBody()
	srv.dispatch.Register(250, 2, func(ctx *Ctx) ([]byte, bool) {
		calls.Add(1)
		return want, true
	})
	sess := newReceiptTestSession(t, srv, 1, "1|2|1000001|conflict")
	sess.send(250, 2, 88, emptyArgsBody(), 0x00)
	if resp := waitReceiptResponse(t, sess); string(resp.Body) != string(want) {
		t.Fatalf("首次响应不符: %x", resp.Body)
	}

	sess.send(250, 2, 88, []byte{0x03, 0x04, 0x00}, 0x00)
	assertNoReceiptResponse(t, sess)
	if got := calls.Load(); got != 1 {
		t.Fatalf("冲突 call_id 不应再次执行，次数=%d", got)
	}
}

func TestRequestReceiptConcurrentSameKeyExecutesOnce(t *testing.T) {
	srv, db := newReceiptTestServer(t, filepath.Join(t.TempDir(), "receipt-concurrent.db"))
	defer db.Close()
	var calls atomic.Int32
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	want := emptyArgsBody()
	srv.dispatch.Register(250, 3, func(ctx *Ctx) ([]byte, bool) {
		n := calls.Add(1)
		if n == 1 {
			entered <- struct{}{}
		}
		<-release
		return want, true
	})
	sess := newReceiptTestSession(t, srv, 1, "1|2|1000001|concurrent")
	const workers = 8
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			sess.send(250, 3, 99, emptyArgsBody(), 0x00)
		}()
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("handler 未开始执行")
	}
	close(release)
	wg.Wait()

	for i := 0; i < workers; i++ {
		if resp := waitReceiptResponse(t, sess); string(resp.Body) != string(want) {
			t.Fatalf("并发重放响应[%d]不符: %x", i, resp.Body)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("并发同 key handler 执行次数=%d，期望 1", got)
	}
}

func TestRequestReceiptFireAndForgetDeduplicatesWithoutReply(t *testing.T) {
	srv, db := newReceiptTestServer(t, filepath.Join(t.TempDir(), "receipt-fire.db"))
	defer db.Close()
	var calls atomic.Int32
	srv.dispatch.Register(250, 4, func(ctx *Ctx) ([]byte, bool) {
		calls.Add(1)
		ctx.Reply([]byte{0x03, 0x03}) // fire-and-forget 路径必须被抑制
		return []byte{0x03, 0x03}, true
	})
	sess := newReceiptTestSession(t, srv, 1, "1|2|1000001|fire")
	sess.send(250, 4, 100, emptyArgsBody(), 0x01)
	sess.send(250, 4, 100, emptyArgsBody(), 0x01)
	assertNoReceiptResponse(t, sess)
	if got := calls.Load(); got != 1 {
		t.Fatalf("fire-and-forget 重复请求不应再次执行，次数=%d", got)
	}

	bodySHA := func() string {
		sum := sha256.Sum256(emptyArgsBody())
		return hex.EncodeToString(sum[:])
	}()
	key := store.RequestReceiptKey{TokenHash: func() string {
		sum := sha256.Sum256([]byte("1|2|1000001|fire"))
		return hex.EncodeToString(sum[:])
	}(), Main: 250, Sub: 4, CallID: 100}
	rec, err := db.GetRequestReceipt(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	if rec.ExpectResponse || rec.HasResponse || rec.Status != store.RequestReceiptCompleted || rec.BodySHA256 != bodySHA {
		t.Fatalf("fire-and-forget 回执不符: %+v", rec)
	}
}

func TestRequestReplyIsUnifiedWithoutDB(t *testing.T) {
	srv := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "receipt-test"}}, logging.Discard(), nil)
	want := emptyArgsBody()
	srv.dispatch.Register(250, 5, func(ctx *Ctx) ([]byte, bool) {
		ctx.Reply(want)
		return []byte{0x14, 0x01, 'X'}, true
	})
	sess := newReceiptTestSession(t, srv, 1, "1|2|1000001|no-db")
	sess.send(250, 5, 101, emptyArgsBody(), 0x00)
	if resp := waitReceiptResponse(t, sess); string(resp.Body) != string(want) {
		t.Fatalf("无 DB 路径 Reply+return 响应不符: %x", resp.Body)
	}
	assertNoReceiptResponse(t, sess)
}

func TestRequestReceiptConcurrentAcrossServerInstancesExecutesOnce(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "receipt-cross-server.db")
	srv1, db1 := newReceiptTestServer(t, dbPath)
	defer db1.Close()
	srv2, db2 := newReceiptTestServer(t, dbPath)
	defer db2.Close()

	var calls atomic.Int32
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	want := emptyArgsBody()
	handler := func(ctx *Ctx) ([]byte, bool) {
		n := calls.Add(1)
		if n == 1 {
			entered <- struct{}{}
		}
		<-release
		return want, true
	}
	srv1.dispatch.Register(250, 6, handler)
	srv2.dispatch.Register(250, 6, handler)

	token := "1|2|1000001|cross-server"
	sess1 := newReceiptTestSession(t, srv1, 1, token)
	sess2 := newReceiptTestSession(t, srv2, 1, token)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		sess1.send(250, 6, 102, emptyArgsBody(), 0x00)
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("首个 Server 的 handler 未执行")
	}
	go func() {
		defer wg.Done()
		sess2.send(250, 6, 102, emptyArgsBody(), 0x00)
	}()
	time.Sleep(100 * time.Millisecond) // 让第二个实例命中 processing 回执
	close(release)
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Fatalf("双 Server 并发 handler 执行次数=%d，期望 1", got)
	}
	for i, sess := range []*receiptTestSession{sess1, sess2} {
		if resp := waitReceiptResponse(t, sess); string(resp.Body) != string(want) {
			t.Fatalf("双 Server 响应[%d]不符: %x", i, resp.Body)
		}
	}
}

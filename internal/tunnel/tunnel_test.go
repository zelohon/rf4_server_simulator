package tunnel

import (
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"
)

func dialPair(t *testing.T, psk string) (*Conn, *Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	type result struct {
		c   *Conn
		err error
	}
	serverCh := make(chan result, 1)
	go func() {
		nc, err := ln.Accept()
		if err != nil {
			serverCh <- result{err: err}
			return
		}
		c, err := HandshakeServer(nc, psk)
		serverCh <- result{c: c, err: err}
	}()

	nc, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	gc, err := HandshakeClient(nc, psk)
	if err != nil {
		t.Fatal(err)
	}
	sr := <-serverCh
	if sr.err != nil {
		t.Fatal(sr.err)
	}
	return gc, sr.c
}

func TestHandshakeOK(t *testing.T) {
	gc, sc := dialPair(t, "secret")
	defer gc.Close()
	defer sc.Close()
	if gc.Name() != "client" || sc.Name() != "server" {
		t.Fatalf("角色名错误")
	}
}

func TestHandshakeBadPSK(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		nc, _ := ln.Accept()
		if nc == nil {
			return
		}
		_, _ = HandshakeServer(nc, "right")
		_ = nc.Close()
	}()
	nc, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := HandshakeClient(nc, "wrong"); err == nil {
		t.Fatal("PSK 不符应握手失败")
	}
}

func TestRecordRoundtrip(t *testing.T) {
	gc, sc := dialPair(t, "s")
	defer gc.Close()
	defer sc.Close()

	op := OpenPayload{Token: "local|1|7247281|abc", Userid: 7247281, Reconnect: true, ClientIP: "1.2.3.4"}
	opb, _ := json.Marshal(op)
	if err := gc.WriteRecord(TypeOpen, 42, opb); err != nil {
		t.Fatal(err)
	}
	if err := gc.WriteRecord(TypeData, 42, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := sc.WriteRecord(TypeData, 42, []byte("world")); err != nil {
		t.Fatal(err)
	}
	// 并发写不串包
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_ = sc.WriteRecord(TypeData, uint64(n), []byte("x"))
		}(i)
	}
	wg.Wait()

	rt, sid, payload, err := sc.ReadRecord()
	if err != nil || rt != TypeOpen || sid != 42 {
		t.Fatalf("first record: rt=%v sid=%d err=%v", rt, sid, err)
	}
	var got OpenPayload
	if err := json.Unmarshal(payload, &got); err != nil || got.Token != op.Token || got.Userid != op.Userid || !got.Reconnect {
		t.Fatalf("open payload mismatch: %s", payload)
	}
	rt, _, payload, err = sc.ReadRecord()
	if err != nil || rt != TypeData || string(payload) != "hello" {
		t.Fatalf("data record mismatch: %v %s", err, payload)
	}
	rt, sid, payload, err = gc.ReadRecord()
	if err != nil || rt != TypeData || string(payload) != "world" {
		t.Fatalf("reverse record mismatch: %v %s", err, payload)
	}
	// 8 条并发记录
	for i := 0; i < 8; i++ {
		rt, sid, payload, err = gc.ReadRecord()
		if err != nil || rt != TypeData || string(payload) != "x" {
			t.Fatalf("concurrent record %d: rt=%v payload=%q err=%v", sid, rt, payload, err)
		}
	}
}

func TestCloseUnblocks(t *testing.T) {
	gc, sc := dialPair(t, "s")
	defer sc.Close()
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = gc.Close()
	}()
	done := make(chan error, 1)
	go func() {
		_, _, _, err := sc.ReadRecord()
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("关闭后读应返回错误")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("关闭后读未解除阻塞")
	}
}

func TestOversizeRejected(t *testing.T) {
	gc, sc := dialPair(t, "s")
	defer gc.Close()
	defer sc.Close()
	if err := gc.WriteRecord(TypeData, 1, make([]byte, maxRecordPayload+1)); err == nil {
		t.Fatal("超大 payload 应被拒")
	}
	// 连接未被破坏：正常记录仍可通
	if err := gc.WriteRecord(TypeData, 1, []byte("ok")); err != nil {
		t.Fatal(err)
	}
	_, _, payload, err := sc.ReadRecord()
	if err != nil || string(payload) != "ok" {
		t.Fatalf("正常记录失败: %v", err)
	}
}

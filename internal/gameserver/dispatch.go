package gameserver

import (
	"log/slog"
	"sync"

	"rf4server/internal/proto"
)

// Ctx 处理器请求上下文（body 树 / call_id / expect_response 一站式持有）。
type Ctx struct {
	Sess           *Session
	Main, Sub      int
	CallID         uint32
	Body           []byte
	Tree           *proto.Value // 惰性解码（解码失败为 nil）
	DecodeOK       bool
	ExpectResponse bool
	Log            *slog.Logger

	// replyCapture 非空时，Ctx.Reply 只暂存响应，由请求回执中间件统一持久化
	// 后再发送。这样 handler 自回包与 dispatch 统一回包不会重复。
	replyCapture *ctxReplyCapture
}

// Key 命令键 "main/sub"。
func (c *Ctx) Key() string { return cmdKeyI(c.Main, c.Sub) }

// TreeAt 解码成功时返回请求 body 的树。
func (c *Ctx) TreeAt() (*proto.Value, bool) { return c.Tree, c.DecodeOK }

// ArgInt 取顶层 args 第 i 个整数（u32/u64 皆可）；越界/类型不符返回 (0,false)。
func (c *Ctx) ArgInt(i int) (uint64, bool) {
	if c.Tree == nil || c.Tree.Kind != proto.KindArgs {
		return 0, false
	}
	it := c.Tree.ArgItem(i)
	if it == nil {
		return 0, false
	}
	switch it.Kind {
	case proto.KindU32, proto.KindU64:
		return it.Num, true
	}
	return 0, false
}

// Reply 回一个 body（封装成响应信封发送）。
//
// 普通路径保持原有行为；请求回执中间件路径下只缓存响应，由 dispatch 在
// 回执落库后统一发送一次。缓存会复制 body，避免 handler 复用切片导致回执漂移。
func (c *Ctx) Reply(body []byte) {
	if c.replyCapture != nil {
		c.replyCapture.capture(body)
		return
	}
	c.Sess.SendEnvelope(proto.BuildResponseEnvelope(c.CallID, body))
}

// ReplyValue 编码 payload 值并回包。
func (c *Ctx) ReplyValue(v *proto.Value) error {
	body, err := proto.EncodeValue(v, c.Sess.Table())
	if err != nil {
		return err
	}
	c.Reply(body)
	return nil
}

// ---- 分发注册表 ----

type key2 struct{ main, sub uint8 }

// Handler 处理器：返回 body 字节 + handled。handled=false 时走兜底链。
type Handler func(ctx *Ctx) ([]byte, bool)

// Dispatch (main,sub) 分发注册表（精确 → main 族 → 兜底链）。
type Dispatch struct {
	srv       *Server
	exact     map[key2]Handler
	family    map[uint8]Handler
	responses map[key2]ResponseContractRule
	mu        sync.RWMutex

	// request receipt 的进程内串行锁。按幂等键哈希分片；同一 key 必定同锁。
	receiptLocks [256]sync.Mutex
}

// NewDispatch 构建分发器。
func NewDispatch(srv *Server) *Dispatch {
	return &Dispatch{srv: srv, exact: map[key2]Handler{}, family: map[uint8]Handler{}, responses: defaultResponseContractRules()}
}

// Register 注册精确 (main,sub) 处理器。
func (d *Dispatch) Register(main, sub uint8, h Handler) {
	d.mu.Lock()
	d.exact[key2{main, sub}] = h
	d.mu.Unlock()
}

// RegisterFamily 注册 main 族兜底处理器。
func (d *Dispatch) RegisterFamily(main uint8, h Handler) {
	d.mu.Lock()
	d.family[main] = h
	d.mu.Unlock()
}

// lookup 精确优先，族兜底其次。
func (d *Dispatch) lookup(main, sub uint8) Handler {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if h, ok := d.exact[key2{main, sub}]; ok {
		return h
	}
	return d.family[main]
}

// handleEnvelopePayload 处理网关转发来的明文信封（marker -1 请求；-2 是客户端对推送的
// 响应不存在——客户端不发 -2，收到即记日志丢弃）。
func (s *Server) handleEnvelopePayload(sess *Session, payload []byte) {
	env, ok := proto.ParseEnvelope(payload)
	if !ok {
		sess.log.Warn("信封解析失败", "len", len(payload))
		return
	}
	if env.Marker != -1 {
		return
	}
	cmd := cmdKeyI(int(env.Main), int(env.Sub))
	gsRequests.WithLabelValues(cmd).Inc()
	ctx := &Ctx{
		Sess:   sess,
		Main:   int(env.Main),
		Sub:    int(env.Sub),
		CallID: env.CallID,
		Body:   env.Body,
		Log:    sess.log.With("cmd", cmd, "call", env.CallID),
	}
	if tree, _, err := proto.DecodeValue(env.Body, 0, s.table); err == nil {
		ctx.Tree = tree
		ctx.DecodeOK = true
	}
	// expect_response：尾字节 0x00 期望响应 / 0x01 fire-and-forget；缺失按期望处理
	expect := env.Expect == nil || *env.Expect == 0
	ctx.ExpectResponse = expect
	if traceCmds && !traceSkip[cmd] {
		sess.log.Info("TRACE 收", "cmd", cmd, "expect", expect,
			"len", len(env.Body), "hex", traceHexHead(env.Body, traceHexMax))
	}

	s.dispatchRequest(ctx)
}

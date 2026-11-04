package gameserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"sync"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

const (
	nativeFailureReceipt     = "native-failure-envelope"
	requestReceiptWait       = 30 * time.Second
	requestReceiptPoll       = 10 * time.Millisecond
	requestReceiptStaleAfter = 2 * time.Minute
)

// receiptSkipCmds 是不落回执表的高频命令。
//
// 这些命令只要求服务端应答，本身没有副作用，重复执行也无害；如果照常
// 落库，24/1 在移动时约 100 次/秒，会把 request_receipts 撑到千万行。
var receiptSkipCmds = map[string]bool{
	"24/1": true, // social/move 位置上报
}

type ctxReplyCapture struct {
	mu         sync.Mutex
	body       []byte
	present    bool
	duplicates int
	failure    bool
}

func (c *ctxReplyCapture) captureFailure(envelope []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.present {
		c.duplicates++
		return
	}
	c.present = true
	c.failure = true
	c.body = append([]byte(nil), envelope...)
}

func (c *ctxReplyCapture) capture(body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.present {
		c.duplicates++
		return
	}
	c.present = true
	c.body = append([]byte(nil), body...)
}

func (c *ctxReplyCapture) snapshot() ([]byte, bool, int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.body...), c.present, c.duplicates, c.failure
}

// requestExecutionResult 是尚未发送的处理器结果。
// hasResponse=false 表示无需回包，或处理、校验失败。
type requestExecutionResult struct {
	body          []byte
	hasResponse   bool
	failed        bool
	errorText     string
	nativeFailure bool
}

func noResponseResult() requestExecutionResult {
	return requestExecutionResult{}
}

func failedRequestResult(reason string) requestExecutionResult {
	return requestExecutionResult{failed: true, errorText: reason}
}

// executeRequest 执行 handler 和兜底链，但不直接发送响应。
// interceptReplies=true 时缓存 Ctx.Reply 的响应，确保回执落库后再发送。
func (d *Dispatch) executeRequest(ctx *Ctx, interceptReplies bool) requestExecutionResult {
	var (
		capture *ctxReplyCapture
	)
	if interceptReplies {
		capture = &ctxReplyCapture{}
		ctx.replyCapture = capture
		defer func() { ctx.replyCapture = nil }()
	}

	if !ctx.ExpectResponse {
		// fire-and-forget：绝不回包；fallback 也不执行。
		if h := d.lookup(uint8(ctx.Main), uint8(ctx.Sub)); h != nil {
			_, _ = h(ctx)
		}
		return noResponseResult()
	}

	if h := d.lookup(uint8(ctx.Main), uint8(ctx.Sub)); h != nil {
		body, handled := h(ctx)
		if capture != nil {
			captured, present, duplicates, failure := capture.snapshot()
			if present {
				if failure {
					return requestExecutionResult{body: captured, hasResponse: true,
						nativeFailure: true, errorText: nativeFailureReceipt}
				}
				if duplicates > 0 {
					ctx.Log.Warn("同一请求调用了多次 Ctx.Reply；仅保留首个响应", "duplicates", duplicates)
				}
				if handled && body != nil {
					ctx.Log.Warn("handler 同时 Ctx.Reply 与返回 body；以 Ctx.Reply 为准，避免双回包")
				}
				return d.finishHandlerResponse(ctx, captured)
			}
		}
		if handled {
			if body == nil {
				return noResponseResult()
			}
			return d.finishHandlerResponse(ctx, body)
		}
	}

	// 兜底链中 body==nil 表示无需回包。
	if body, kind := d.srv.fallbackFor(ctx.Main, ctx.Sub); body != nil {
		gsFallbacks.WithLabelValues(kind).Inc()
		if traceCmds {
			ctx.Sess.log.Info("TRACE 回(兜底)", "cmd", ctx.Key(), "kind", kind, "len", len(body),
				"hex", traceHexHead(body, traceHexMax))
		}
		return requestExecutionResult{body: body, hasResponse: true}
	} else if kind != "" {
		gsFallbacks.WithLabelValues(kind).Inc()
		return noResponseResult()
	}
	ctx.Sess.log.Warn("无兜底可回（跳过）", "cmd", ctx.Key())
	return failedRequestResult("no handler or fallback")
}

func (d *Dispatch) finishHandlerResponse(ctx *Ctx, body []byte) requestExecutionResult {
	if reason := d.checkRegisteredResponse(uint8(ctx.Main), uint8(ctx.Sub), body, d.srv.table); reason != "" {
		gsFallbacks.WithLabelValues("contract_rejected").Inc()
		ctx.Sess.log.Error("拒发响应：顶层合同不符", "cmd", ctx.Key(), "reason", reason)
		return failedRequestResult("response contract rejected: " + reason)
	}
	if traceCmds {
		ctx.Sess.log.Info("TRACE 回", "cmd", ctx.Key(), "len", len(body),
			"hex", traceHexHead(body, traceHexMax))
	}
	return requestExecutionResult{body: append([]byte(nil), body...), hasResponse: true}
}

func requestReceiptKey(ctx *Ctx) (store.RequestReceiptKey, string) {
	tokenSum := sha256.Sum256([]byte(ctx.Sess.Token))
	bodySum := sha256.Sum256(ctx.Body)
	key := store.RequestReceiptKey{
		TokenHash: hex.EncodeToString(tokenSum[:]),
		Main:      uint8(ctx.Main),
		Sub:       uint8(ctx.Sub),
		CallID:    ctx.CallID,
	}
	return key, hex.EncodeToString(bodySum[:])
}

func (d *Dispatch) lockReceipt(key store.RequestReceiptKey) func() {
	// tokenHash 已固定 64 字节，再混入命令/call_id 后分片。
	lockKey := key.TokenHash + ":" + strconv.Itoa(int(key.Main)) + ":" +
		strconv.Itoa(int(key.Sub)) + ":" + strconv.FormatUint(uint64(key.CallID), 10)
	sum := sha256.Sum256([]byte(lockKey))
	idx := int(sum[0]) ^ int(sum[1]) ^ int(sum[2])
	mu := &d.receiptLocks[idx]
	mu.Lock()
	return mu.Unlock
}

// dispatchRequest 是入站请求的统一执行入口。DB 可用时按
// (token 哈希, main, sub, call_id) 抢占回执；相同 key 与 body 只执行一次。
func (s *Server) dispatchRequest(ctx *Ctx) {
	if s.DB == nil {
		// 无 DB 时仍统一拦截 Ctx.Reply，避免重复回包并保证无需回包的命令静默完成。
		result := s.dispatch.executeRequest(ctx, true)
		s.sendRequestResult(ctx, result)
		return
	}

	if receiptSkipCmds[ctx.Key()] {
		// 高频、无副作用命令：正常执行并应答，但不写回执表。
		result := s.dispatch.executeRequest(ctx, true)
		s.sendRequestResult(ctx, result)
		return
	}

	key, bodySHA := requestReceiptKey(ctx)
	unlock := s.dispatch.lockReceipt(key)
	defer unlock()

	waitCtx, cancelWait := context.WithTimeout(context.Background(), requestReceiptWait)
	rec, claimed, err := s.DB.ClaimRequestReceipt(
		waitCtx, key, bodySHA, ctx.ExpectResponse, time.Now().Add(-requestReceiptStaleAfter))
	if err != nil {
		cancelWait()
		ctx.Log.Error("请求回执抢占失败；为避免重复副作用，拒绝执行", "err", err)
		return
	}

	deadline := time.NewTimer(requestReceiptWait)
	defer deadline.Stop()
	for !claimed {
		if rec.BodySHA256 != bodySHA || rec.ExpectResponse != ctx.ExpectResponse {
			cancelWait()
			ctx.Log.Error("请求回执冲突：同一 call_id 收到了不同请求，拒绝执行",
				"existing_body_sha256", rec.BodySHA256, "body_sha256", bodySHA,
				"existing_expect", rec.ExpectResponse, "expect", ctx.ExpectResponse)
			return
		}
		switch rec.Status {
		case store.RequestReceiptCompleted:
			cancelWait()
			s.replayRequestReceipt(ctx, rec)
			return
		case store.RequestReceiptFailed:
			cancelWait()
			ctx.Log.Warn("请求已执行但响应失败；按幂等策略不重复执行",
				"error", rec.ErrorText)
			return
		case store.RequestReceiptProcessing:
			select {
			case <-deadline.C:
				cancelWait()
				ctx.Log.Error("等待同 key 请求回执落定超时；拒绝重复执行")
				return
			case <-time.After(requestReceiptPoll):
			}
			rec, err = s.DB.GetRequestReceipt(waitCtx, key)
			if err != nil {
				cancelWait()
				ctx.Log.Error("轮询请求回执失败；拒绝重复执行", "err", err)
				return
			}
		default:
			cancelWait()
			ctx.Log.Error("请求回执状态非法；拒绝执行", "status", rec.Status)
			return
		}
	}
	cancelWait()

	result := s.dispatch.executeRequest(ctx, true)
	status := store.RequestReceiptCompleted
	if result.failed {
		status = store.RequestReceiptFailed
	}
	completeCtx, cancelComplete := context.WithTimeout(context.Background(), 5*time.Second)
	affected, err := s.DB.CompleteRequestReceipt(
		completeCtx, key, bodySHA, status, result.body, result.hasResponse, result.errorText)
	cancelComplete()
	if err != nil {
		ctx.Log.Error("请求回执落库失败；不发送响应，等待客户端安全重试", "err", err)
		return
	}
	if !affected {
		ctx.Log.Error("请求回执已被其他执行者接管；不发送本次响应")
		return
	}
	if status == store.RequestReceiptFailed {
		ctx.Log.Warn("请求执行完成但无可重放响应", "error", result.errorText)
		return
	}
	s.sendRequestResult(ctx, result)
}

func (s *Server) replayRequestReceipt(ctx *Ctx, rec *store.RequestReceipt) {
	if !rec.HasResponse {
		return
	}
	result := requestExecutionResult{body: rec.ResponseBody, hasResponse: true,
		nativeFailure: rec.ErrorText == nativeFailureReceipt}
	if traceCmds {
		ctx.Log.Info("TRACE 回(回执重放)", "cmd", ctx.Key(), "len", len(result.body),
			"hex", traceHexHead(result.body, 160))
	}
	s.sendRequestResult(ctx, result)
}

func (s *Server) sendRequestResult(ctx *Ctx, result requestExecutionResult) {
	if !ctx.ExpectResponse || !result.hasResponse {
		return
	}
	if result.nativeFailure {
		ctx.Sess.SendEnvelope(result.body)
		return
	}
	ctx.Sess.SendEnvelope(proto.BuildResponseEnvelope(ctx.CallID, result.body))
	for _, fn := range ctx.afterResponse {
		fn()
	}
}

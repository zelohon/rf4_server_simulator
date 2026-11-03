package gameserver

import (
	"encoding/hex"
	"os"
	"strconv"
)

// traceCmds 控制命令级请求与响应跟踪。
//
// 开启方式（启动时读一次，改完要重启进程）：
//   - 环境变量 RF4_TRACE_CMDS=1；或
//   - 工作目录下存在 data/trace_cmds.on 哨兵文件。
//
// 每条请求、响应和推送输出一行，包含命令号、长度和十六进制头部。
// 头部默认截断到 160 字节。
var traceCmds = func() bool {
	if os.Getenv("RF4_TRACE_CMDS") == "1" {
		return true
	}
	_, err := os.Stat("data/trace_cmds.on")
	return err == nil
}()

// traceHexMax 是每行 hex 的最大字节数。默认 160；存在 data/trace_full.on
// 时放宽到 4096，用于逐字节排查协议字段（调试完删掉该文件即可）。
var traceHexMax = func() int {
	if _, err := os.Stat("data/trace_full.on"); err == nil {
		return 4096
	}
	return 160
}()

// traceSkip 排除高频命令，避免 14/7 和 2/2 淹没日志。
var traceSkip = map[string]bool{"14/7": true, "2/2": true, "2/1": true}

// traceHexHead 取字节串前 n 字节的十六进制（超出部分用 …+K 标注长度）。
func traceHexHead(b []byte, n int) string {
	if len(b) <= n {
		return hex.EncodeToString(b)
	}
	return hex.EncodeToString(b[:n]) + "…+" + strconv.Itoa(len(b)-n)
}

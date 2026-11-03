package license

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// 在线复核：只在 admin 启动时调用一次。
//
// 设计意图：
//   - 有网 → 卡密被停用/过期时立刻拒绝，受平台控制；
//   - 无网 → 不阻塞启动，交给本地票据的有效期兜底。
//
// 所有失败原因只在内部日志里以错误码呈现，不向使用者展示任何提示。

const (
	// onlineEndpoint 是卡密登录接口。每个功能一条独立地址，
	// 这条对应「单码登录」。
	onlineEndpoint = "https://w.eydata.net/A1BF159728CB1440"
	// onlineAPIVer 是接口要求的固定版本号，不匹配会返回 -204。
	onlineAPIVer = "1.0"
)

// OnlineStatus 是在线复核的结论。
type OnlineStatus int

const (
	// OnlineValid 表示卡密有效，放行。
	OnlineValid OnlineStatus = iota
	// OnlineInvalid 表示平台明确判定卡密不可用。
	OnlineInvalid
	// OnlineOffline 表示连不上平台，交由本地票据判断。
	OnlineOffline
)

func (s OnlineStatus) String() string {
	switch s {
	case OnlineValid:
		return "valid"
	case OnlineInvalid:
		return "invalid"
	default:
		return "offline"
	}
}

// onlineCheck 是 CheckOnline 的调用入口，测试中会替换掉。
var onlineCheck = CheckOnline

// CheckOnline 向授权平台复核卡密。
//
// 机器码刻意传空：不绑定设备，换电脑无需解绑。
// 第二个返回值是平台原始返回，仅用于内部排查，不要写进用户可见的输出。
func CheckOnline(code string, timeout time.Duration) (OnlineStatus, string) {
	code = strings.TrimSpace(code)
	if code == "" {
		return OnlineOffline, "empty"
	}
	if timeout <= 0 {
		timeout = 4 * time.Second
	}

	form := url.Values{}
	form.Set("SingleCode", code)
	form.Set("Ver", onlineAPIVer)
	form.Set("Mac", "")

	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			// 显式不走系统代理：现场机器常配了代理但访问不了授权平台。
			Proxy:               nil,
			DisableKeepAlives:   true,
			TLSHandshakeTimeout: timeout,
		},
	}
	resp, err := client.PostForm(onlineEndpoint, form)
	if err != nil {
		return OnlineOffline, "network"
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		return OnlineOffline, "read"
	}
	text := strings.TrimSpace(string(body))

	if len(text) == 32 && isHexUpper(text) {
		return OnlineValid, "ok"
	}
	switch text {
	case "-402":
		// 机器码与绑定不符。能走到这一步说明卡密本身存在且有效，
		// 只是绑在别的设备上，按有效处理，避免换机后无法启动。
		return OnlineValid, "-402"
	case "", "-203", "-204":
		// -203 缺参数 / -204 版本不匹配属于调用侧问题，不能据此判定卡密失效。
		return OnlineOffline, text
	}
	return OnlineInvalid, text
}

func isHexUpper(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'A' || c > 'F') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return len(s) > 0
}

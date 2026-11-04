// Package config 提供 YAML 配置加载和环境变量覆盖。
//
// 空配置使用开发默认值；敏感配置通过 config.yaml 或 RF4_ 环境变量注入，
// 仓库只保留示例配置。
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 全量配置。每个进程只读自己关心的部分。
type Config struct {
	Env      string   `yaml:"env"` // dev | prod
	Log      Log      `yaml:"log"`
	SQLite   SQLite   `yaml:"sqlite"`
	Gateway  Gateway  `yaml:"gateway"`
	Game     Game     `yaml:"game"`
	Admin    Admin    `yaml:"admin"`
	Launcher Launcher `yaml:"launcher"`
}

// SQLite 本地单文件数据库（多进程共享，WAL 并发）。
type SQLite struct {
	Path       string `yaml:"path"`        // 主库，例如 data/rf4server.db
	StaticPath string `yaml:"static_path"` // 只读静态配置库，例如 server_data/static.sqlite3
}

type Log struct {
	Level  string `yaml:"level"`  // debug | info | warn | error
	Format string `yaml:"format"` // text | json
}

// Tunnel 网关↔主服内部隧道（gameserver 监听、gateway 拨号）。
type Tunnel struct {
	Addr string `yaml:"addr"` // gameserver 内部监听地址，如 127.0.0.1:9401
	PSK  string `yaml:"psk"`  // 预共享密钥，隧道握手认证
}

type Gateway struct {
	LoginAddr             string  `yaml:"login_addr"`        // HTTPS 登录监听，如 :443
	LoginCompatAddr       string  `yaml:"login_compat_addr"` // 可选：旧版 CN 客户端的明文 HTTP 登录兼容口，如 127.0.0.1:7515
	RealtimeAddr          string  `yaml:"realtime_addr"`     // realtime TCP 监听，如 :9341
	CertFile              string  `yaml:"cert_file"`
	KeyFile               string  `yaml:"key_file"`
	AdvertiseHost         string  `yaml:"advertise_host"`          // 写入 <logon><host> 的地址
	AdvertisePort         int     `yaml:"advertise_port"`          // 写入 <logon><port>
	Region                string  `yaml:"region"`                  // CN → 客户端中文界面
	ServerName            string  `yaml:"server_name"`             // 写入 <logon><server>
	LoginDomain           string  `yaml:"login_domain"`            // api.rf4game.ru（供日志/工具用）
	MaxConns              int     `yaml:"max_conns"`               // realtime 最大并发连接
	AckClientFrames       bool    `yaml:"ack_client_frames"`       // 先 ACK 后转发（协议要求，默认开启）
	TransportKeepaliveSec float64 `yaml:"transport_keepalive_sec"` // 4 字节零帧保活间隔

	Tunnel Tunnel `yaml:"tunnel"`

	ObsAddr string `yaml:"obs_addr"` // 观测端口 /healthz /readyz /metrics /debug/pprof

	TokenTTL          int     `yaml:"token_ttl_days"`         // 登录 token 有效期（天）
	UseridStart       int64   `yaml:"userid_start"`           // 自增 userid 起点

	// 账号登录：客户端在 /login.php 的 query 里上送 login / password(SHA1)。
	// 开启后每个账号固定独立 uid，一台机器可同时登录多个账号且互不串档。
	AuthEnabled      bool `yaml:"auth_enabled"`       // 启用账号校验（关闭则沿用固定 userid）
	AuthAutoRegister bool `yaml:"auth_auto_register"` // 首次登录的账号自动建立
	BiteProbability   float64 `yaml:"bite_probability"`       // 起竿判出鱼概率（1.0=每杆必出）
	BiteDelayScale    float64 `yaml:"bite_delay_scale"`       // 咬钩延迟倍率
	BiteDelayFixed    float64 `yaml:"bite_delay_fixed"`       // 固定咬钩延迟秒（>0 时忽略分布）
	KeepaliveInterval float64 `yaml:"keepalive_interval_sec"` // 2/2 世界时钟推送间隔（秒）
}

type Game struct {
	Tunnel        Tunnel `yaml:"tunnel"` // 与 Gateway.Tunnel 相同配置
	ObsAddr       string `yaml:"obs_addr"`
	DefaultUserid int64  `yaml:"default_userid"` // 单账号模式下使用的固定 userid

	KeepaliveIntervalSec float64 `yaml:"keepalive_interval_sec"` // 2/2 世界时钟推送间隔（秒）
}

// KeepaliveInterval 2/2 保活间隔；必须小于客户端断开阈值（~10s）。
func (g *Game) KeepaliveInterval() time.Duration {
	if g.KeepaliveIntervalSec <= 0 {
		return 5 * time.Second
	}
	return time.Duration(g.KeepaliveIntervalSec * float64(time.Second))
}

type Admin struct {
	HTTPAddr  string `yaml:"http_addr"` // 仅 cmd/admin 独立调试监听，如 :8080
	JWTSecret string `yaml:"jwt_secret"`
	ObsAddr   string `yaml:"obs_addr"`
	Username  string `yaml:"username"` // 管理员账号（以配置为准，启动时同步）
	Password  string `yaml:"password"` // 管理员密码（明文进 yaml，注意文件权限）
}

// Launcher 本地启动器设置。
type Launcher struct {
	GamePath   string `yaml:"game_path"`   // rf4_x64.exe 绝对路径
	GameRegion string `yaml:"game_region"` // 启动参数 -region 的值
}

// Load 从 path 读取 YAML 并应用环境变量覆盖；path 为空时只用默认值+环境变量。
func Load(path string) (*Config, error) {
	cfg := defaults()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("读取配置 %s: %w", path, err)
		}
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("解析配置 %s: %w", path, err)
		}
	}
	cfg.applyEnvOverrides()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func defaults() *Config {
	return &Config{
		Env:      "dev",
		Log:      Log{Level: "info", Format: "text"},
		Launcher: Launcher{GameRegion: "CN"},
		SQLite: SQLite{
			Path:       "data/rf4server.db",
			StaticPath: "server_data/static.sqlite3",
		},
		Gateway: Gateway{
			LoginAddr:             ":443",
			RealtimeAddr:          ":9341",
			CertFile:              "certs/server.crt",
			KeyFile:               "certs/server.key",
			AdvertiseHost:         "127.0.0.1",
			AdvertisePort:         9341,
			Region:                "CN",
			ServerName:            "RF4_566",
			LoginDomain:           "api.rf4game.ru",
			MaxConns:              512,
			AckClientFrames:       true,
			TransportKeepaliveSec: 5.0,
			Tunnel:                Tunnel{Addr: "127.0.0.1:9401", PSK: "change-me"},
			ObsAddr:               ":9501",
			TokenTTL:              365,
			UseridStart:           1000001,
			AuthEnabled:           true,
			AuthAutoRegister:      false,
			BiteProbability:       1.0,
			BiteDelayScale:        1.0,
			BiteDelayFixed:        0,
			KeepaliveInterval:     5.0,
		},
		Game: Game{
			Tunnel:               Tunnel{Addr: "127.0.0.1:9401", PSK: "change-me"},
			ObsAddr:              ":9502",
			DefaultUserid:        1000001,
			KeepaliveIntervalSec: 5.0,
		},
		Admin: Admin{
			HTTPAddr:  ":8080",
			JWTSecret: "change-me",
			ObsAddr:   ":9503",
			Username:  "admin",
			Password:  "admin123",
		},
	}
}

// applyEnvOverrides 环境变量覆盖：只覆盖部署时最常注入的字段。
func (c *Config) applyEnvOverrides() {
	envStr := func(key string, dst *string) {
		if v, ok := os.LookupEnv(key); ok {
			*dst = v
		}
	}
	envFloat := func(key string, dst *float64) {
		if v, ok := os.LookupEnv(key); ok {
			if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				*dst = f
			}
		}
	}
	envInt := func(key string, dst *int) {
		if v, ok := os.LookupEnv(key); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				*dst = n
			}
		}
	}

	envStr("RF4_LOG_LEVEL", &c.Log.Level)
	envStr("RF4_LOG_FORMAT", &c.Log.Format)
	envStr("RF4_SQLITE_PATH", &c.SQLite.Path)
	envStr("RF4_SQLITE_STATIC_PATH", &c.SQLite.StaticPath)
	envStr("RF4_GATEWAY_LOGIN_ADDR", &c.Gateway.LoginAddr)
	envStr("RF4_GATEWAY_REALTIME_ADDR", &c.Gateway.RealtimeAddr)
	envStr("RF4_GATEWAY_ADVERTISE_HOST", &c.Gateway.AdvertiseHost)
	envStr("RF4_GATEWAY_CERT", &c.Gateway.CertFile)
	envStr("RF4_GATEWAY_KEY", &c.Gateway.KeyFile)
	envStr("RF4_TUNNEL_ADDR", &c.Gateway.Tunnel.Addr)
	envStr("RF4_TUNNEL_PSK", &c.Gateway.Tunnel.PSK)
	envStr("RF4_GAME_TUNNEL_PSK", &c.Game.Tunnel.PSK)
	envStr("RF4_ADMIN_HTTP_ADDR", &c.Admin.HTTPAddr)
	envStr("RF4_ADMIN_JWT_SECRET", &c.Admin.JWTSecret)
	envStr("RF4_GAME_CLIENT_PATH", &c.Launcher.GamePath)
	envStr("RF4_GAME_CLIENT_REGION", &c.Launcher.GameRegion)
	envFloat("RF4_GATEWAY_KEEPALIVE_SEC", &c.Gateway.TransportKeepaliveSec)
	envFloat("RF4_KEEPALIVE_INTERVAL_SEC", &c.Gateway.KeepaliveInterval)
	envFloat("RF4_BITE_PROBABILITY", &c.Gateway.BiteProbability)
	envInt("RF4_GATEWAY_MAX_CONNS", &c.Gateway.MaxConns)
}

func (c *Config) validate() error {
	if c.SQLite.Path == "" {
		return fmt.Errorf("sqlite.path 未配置")
	}
	if c.Gateway.AdvertisePort == 0 {
		return fmt.Errorf("gateway.advertise_port 未配置")
	}
	return nil
}

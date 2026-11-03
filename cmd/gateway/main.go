// gateway —— 接入网关进程。
//
// 职责：HTTPS :443 登录（<logon> XML + token 签发）、TCP :9341 realtime 接入
// （握手/RC4/ACK/传输保活/重连接管）、内部隧道客户端（与 gameserver 交换明文信封）。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"rf4server/internal/buildinfo"
	"rf4server/internal/config"
	"rf4server/internal/gateway"
	"rf4server/internal/license"
	"rf4server/internal/logging"
	"rf4server/internal/metrics"
	"rf4server/internal/store"
)

func main() {
	configPath := flag.String("config", "config.yaml", "配置文件路径")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gateway:", err)
		os.Exit(2)
	}
	log := logging.New(cfg.Log.Level, cfg.Log.Format)

	// 离线授权校验：失败时只记录内部错误码，不向使用者暴露原因。
	if err := license.GuardConfig(*configPath); err != nil {
		log.Error("服务初始化失败", "code", "E2107")
		os.Exit(1)
	}

	log.Info("gateway 启动",
		"version", buildinfo.Version, "commit", buildinfo.GitCommit,
		"env", cfg.Env,
		"login", cfg.Gateway.LoginAddr, "realtime", cfg.Gateway.RealtimeAddr,
		"tunnel", cfg.Gateway.Tunnel.Addr)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	obs := metrics.NewServer(cfg.Gateway.ObsAddr, "gateway")
	obsErrCh := obs.Start()

	// ---- 共享 SQLite（会话凭据/重连接管存这里，与主服/管理后台同库）----
	db, err := store.OpenWithStatic(cfg.SQLite.Path, cfg.SQLite.StaticPath)
	if err != nil {
		log.Error("SQLite 打开失败", "path", cfg.SQLite.Path, "static", cfg.SQLite.StaticPath, "err", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		log.Error("迁移失败", "err", err)
		os.Exit(1)
	}
	storage := gateway.NewSQLiteStore(db, log)
	log.Info("SQLite 已连接", "path", cfg.SQLite.Path, "static", cfg.SQLite.StaticPath)

	// ---- TLS 证书 ----
	if err := gateway.EnsureCert(cfg.Gateway.CertFile, cfg.Gateway.KeyFile, cfg.Gateway.LoginDomain); err != nil {
		log.Error("TLS 证书不可用", "err", err)
		os.Exit(1)
	}

	// ---- 隧道管理器 + realtime 服务 ----
	tm := gateway.NewTunnelManager(cfg.Gateway.Tunnel.Addr, cfg.Gateway.Tunnel.PSK, log, obs, nil)
	rt := gateway.NewRealtimeServer(cfg.Gateway, storage, log, tm, obs)
	tm.SetGateway(rt)
	if err := rt.Listen(); err != nil {
		log.Error("realtime 监听失败", "err", err)
		os.Exit(1)
	}
	rtErrCh := make(chan error, 1)
	go func() { rtErrCh <- rt.Serve() }()

	// ---- HTTPS 登录服务 ----
	login, err := gateway.NewLoginServer(cfg.Gateway, storage, log, obs)
	if err != nil {
		log.Error("登录服务构建失败", "err", err)
		os.Exit(1)
	}
	loginErrCh, err := login.Start()
	if err != nil {
		log.Error("登录服务监听失败", "err", err)
		os.Exit(1)
	}

	log.Info("gateway 就绪", "advertise", fmt.Sprintf("%s:%d", cfg.Gateway.AdvertiseHost, cfg.Gateway.AdvertisePort))
	obs.SetReady(true)

	select {
	case <-ctx.Done():
	case err := <-obsErrCh:
		log.Error("观测端口异常退出", "err", err)
	case err := <-rtErrCh:
		log.Error("realtime 服务异常退出", "err", err)
	case err := <-loginErrCh:
		log.Error("登录服务异常退出", "err", err)
	}

	obs.SetReady(false)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = login.Shutdown(shutdownCtx)
	_ = rt.Close()
	tm.Close()
	_ = obs.Shutdown(shutdownCtx)
	_ = db.Close()
	log.Info("gateway 已退出")
	if !errors.Is(ctx.Err(), context.Canceled) {
		os.Exit(1)
	}
}

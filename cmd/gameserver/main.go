// gameserver —— 主服务器（游戏逻辑）进程。
//
// 职责：隧道服务端、会话核、RPC 处理器和运行时存储。
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"rf4server/internal/buildinfo"
	"rf4server/internal/config"
	"rf4server/internal/gameserver"
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
		fmt.Fprintln(os.Stderr, "gameserver:", err)
		os.Exit(2)
	}
	log := logging.New(cfg.Log.Level, cfg.Log.Format)

	// 离线授权校验：失败时只记录内部错误码，不向使用者暴露原因。
	if err := license.GuardConfig(*configPath); err != nil {
		log.Error("服务初始化失败", "code", "E2107")
		os.Exit(1)
	}

	log.Info("gameserver 启动",
		"version", buildinfo.Version, "commit", buildinfo.GitCommit,
		"env", cfg.Env, "tunnel", cfg.Game.Tunnel.Addr)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	obs := metrics.NewServer(cfg.Game.ObsAddr, "gameserver")
	obsErrCh := obs.Start()

	srv := gameserver.NewServer(cfg.Game, log, obs)
	// 共享 SQLite（fail-fast）
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
	srv.SetDB(db)
	log.Info("SQLite 已连接", "path", cfg.SQLite.Path, "static", cfg.SQLite.StaticPath)
	if err := srv.RepairItemPhysics(ctx); err != nil {
		log.Error("物品物理状态修复失败", "err", err)
		os.Exit(1)
	}
	if n, err := srv.BackfillRigProjections(ctx); err != nil {
		log.Warn("装配投影回填失败", "rods", n, "err", err)
	} else {
		log.Info("装配投影回填完成", "rods", n)
	}
	if n, path, err := srv.LoadCompatTable(); err != nil {
		log.Info("工坊兼容表未加载", "err", err)
	} else {
		log.Info("工坊兼容表已加载", "alternatives", n, "path", path)
	}
	if n, err := srv.RepairReelComponents(ctx); err != nil {
		log.Warn("渔线轮零件重建失败", "reels", n, "err", err)
	} else {
		log.Info("渔线轮零件重建完成", "reels", n)
	}
	if n, err := srv.RebuildReelPerformance(ctx); err != nil {
		log.Warn("渔线轮物理参数重建失败", "reels", n, "err", err)
	} else {
		log.Info("渔线轮物理参数已同步", "reels", n)
	}

	ln, err := net.Listen("tcp", cfg.Game.Tunnel.Addr)
	if err != nil {
		log.Error("隧道监听失败", "addr", cfg.Game.Tunnel.Addr, "err", err)
		os.Exit(1)
	}
	srvErrCh := make(chan error, 1)
	go func() { srvErrCh <- srv.Serve(ln) }()

	// 管理指令订阅（共享 SQLite 队列）+ 在线会话统计键（共享 KV）
	srvStop := make(chan struct{})
	srv.StartAdminSubscriber(db, srvStop)
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				close(srvStop)
				return
			case <-t.C:
				_ = db.KVSet(ctx, "stats:sessions", fmt.Sprintf("%d", srv.SessionCount()))
			}
		}
	}()

	log.Info("gameserver 就绪，等待网关接入")
	obs.SetReady(true)

	select {
	case <-ctx.Done():
	case err := <-obsErrCh:
		log.Error("观测端口异常退出", "err", err)
	case err := <-srvErrCh:
		log.Error("隧道服务异常退出", "err", err)
	}

	obs.SetReady(false)
	srv.CloseAll()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = obs.Shutdown(shutdownCtx)
	log.Info("gameserver 已退出")
}

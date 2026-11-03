// admin —— Web 管理后台进程。
//
// JWT 登录 + 玩家管理（改数据/发物品/发测试包/踢人/封禁）+ 运行参数热更 +
// 审计日志；前端为内嵌 SPA（internal/admin/web，无外部依赖）。
// 运行时指令写入共享 SQLite 队列，由 gameserver 轮询消费。
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

	"rf4server/internal/admin"
	"rf4server/internal/buildinfo"
	"rf4server/internal/config"
	"rf4server/internal/license"
	"rf4server/internal/logging"
	"rf4server/internal/metrics"
	"rf4server/internal/store"
)

func main() {
	configPath := flag.String("config", "config.yaml", "配置文件路径")
	initAdmin := flag.String("init-admin", "", "初始化管理员: -init-admin user:password（表空时生效）")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "admin:", err)
		os.Exit(2)
	}
	log := logging.New(cfg.Log.Level, cfg.Log.Format)

	// 授权校验：本地票据 + 在线复核。有网时由平台决定是否放行，
	// 无网时退回本地有效期。失败只记录内部错误码，不向使用者暴露原因。
	var licErr error
	if err := license.GuardConfigOnline(*configPath, 4*time.Second); err != nil {
		licErr = err
	}
	if licErr != nil {
		code := "E2107"
		if errors.Is(licErr, license.ErrOnlineRejected) {
			code = "E2108"
		}
		log.Error("服务初始化失败", "code", code)
		os.Exit(1)
	}

	log.Info("admin 启动",
		"version", buildinfo.Version, "commit", buildinfo.GitCommit,
		"env", cfg.Env, "http", cfg.Admin.HTTPAddr)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	obs := metrics.NewServer(cfg.Admin.ObsAddr, "admin")
	obsErrCh := obs.Start()

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
	// 管理员凭据以配置为准：账号不存在则创建，密码哈希不一致则更新。
	// -init-admin 仅用于首次初始化，优先级低于配置。
	if *initAdmin != "" {
		user, pass, ok := splitColon(*initAdmin)
		if !ok {
			log.Error("init-admin 格式应为 user:password")
			os.Exit(2)
		}
		if err := db.EnsureAdmin(ctx, user, pass); err != nil {
			log.Error("初始化管理员失败", "err", err)
			os.Exit(1)
		}
		log.Info("管理员已确保存在（仅表空时创建）", "user", user)
	}
	adminUser := cfg.Admin.Username
	adminPass := cfg.Admin.Password
	if adminUser == "" {
		adminUser, adminPass = "admin", "admin123" // 配置缺省时兜底默认
	}
	created, err := db.UpsertAdminPassword(ctx, adminUser, adminPass)
	if err != nil {
		log.Error("管理员凭据同步失败", "err", err)
		os.Exit(1)
	}
	if created {
		log.Info("管理员已创建", "user", adminUser)
	} else {
		log.Info("管理员凭据已同步", "user", adminUser)
	}

	srv := admin.NewServer(cfg.Admin, db, log, admin.WebFS())
	adminErrCh, err := srv.Start()
	if err != nil {
		log.Error("管理后台监听失败", "err", err)
		os.Exit(1)
	}
	log.Info("admin 就绪", "addr", srv.Addr())
	obs.SetReady(true)

	// 在线会话数：读共享 KV（gameserver 每 5s 刷新）
	go func() {
		for {
			if n, ok, _ := db.KVGet(ctx, "stats:sessions"); ok {
				var v int64
				fmt.Sscanf(n, "%d", &v)
				srv.SetSessionCount(v)
			}
			time.Sleep(5 * time.Second)
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-obsErrCh:
		log.Error("观测端口异常退出", "err", err)
	case err := <-adminErrCh:
		log.Error("管理后台异常退出", "err", err)
	}

	obs.SetReady(false)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	_ = obs.Shutdown(shutdownCtx)
	db.Close()
	log.Info("admin 已退出")
}

func splitColon(s string) (string, string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			return s[:i], s[i+1:], true
		}
	}
	return "", "", false
}

// launcher —— 图形化常驻监督器：一键初始化并管理 gameserver/gateway，
// 并在同一 HTTP 入口内直接挂载 admin 管理 handler。
//
// 系统托盘常驻（getlantern/systray，纯 Go 无 CGO）+ 本地统一 Web 控制中心
// （默认 http://127.0.0.1:9500）。管理后台不再单独监听端口。能力：
//   - 首次运行自动初始化数据库（迁移 + 静态配置导入 + 默认管理员）
//   - 按序启动两个游戏服务并做健康检查；崩溃指数退避自动重启
//   - 托盘菜单：控制面板 / 打开管理后台 / 启停全部 / 退出
//   - 面板：状态卡片、启停按钮、双进程日志实时尾巴
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/getlantern/systray"

	"rf4server/internal/admin"
	"rf4server/internal/config"
	"rf4server/internal/seed"
	"rf4server/internal/store"
	"rf4server/internal/supervisor"
)

//go:embed panel.html
var panelHTML []byte

// 应用状态（托盘/面板共享）。
type app struct {
	cfg      *config.Config
	cfgPath  string
	sup      *supervisor.Supervisor
	log      logger
	panelURL string

	ctx    context.Context
	cancel context.CancelFunc

	adminInitMu sync.Mutex
	adminMu     sync.RWMutex
	adminSrv    *admin.Server
	adminDB     *store.DB
	adminErr    string

	gameMu    sync.RWMutex
	startGame func(*exec.Cmd) error
}

type logger interface {
	Info(string)
	Warn(string)
	Error(string)
}

// adminLogger 将 launcher 的简单 logger 适配为 admin 的键值日志接口。
type adminLogger struct{ l logger }

func (l adminLogger) Info(msg string, args ...any)  { l.l.Info(logMessage(msg, args...)) }
func (l adminLogger) Warn(msg string, args ...any)  { l.l.Warn(logMessage(msg, args...)) }
func (l adminLogger) Error(msg string, args ...any) { l.l.Error(logMessage(msg, args...)) }

func main() {
	configPath := flag.String("config", "config.yaml", "配置文件路径")
	panelAddr := flag.String("panel-addr", "127.0.0.1:9500", "控制面板监听地址（仅本机）")
	adminUser := flag.String("admin-user", "admin", "初始管理员用户名（表空时生效）")
	adminPass := flag.String("admin-pass", "admin123", "初始管理员密码（表空时生效）")
	staticDB := flag.String("static-db", "", "静态配置库路径（默认取 sqlite.static_path）")
	flag.Parse()

	// ---- 工作目录解析：依次在 CWD、exe 目录和 exe 上级目录寻找 config.yaml。
	// 找到后切换工作目录，使 config、server_data、data、logs 等相对路径统一
	// 以仓库根为基准（双击启动时 CWD 通常是 bin/）。
	workDir := resolveWorkDir(*configPath)
	if workDir != "" {
		if err := os.Chdir(workDir); err != nil {
			showFatal("切换工作目录失败", err)
			return
		}
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		showFatal("配置加载失败", err)
		return
	}
	if *staticDB == "" {
		*staticDB = cfg.SQLite.StaticPath
	} else {
		cfg.SQLite.StaticPath = *staticDB
		_ = os.Setenv("RF4_SQLITE_STATIC_PATH", *staticDB)
	}
	appCtx, appCancel := context.WithCancel(context.Background())
	app := &app{cfg: cfg, cfgPath: *configPath, log: panelLogger{}, ctx: appCtx, cancel: appCancel}
	defer app.closeAdmin()

	// ---- Web 控制面板（先于首跑 seed 起来：GUI 无控制台，seed 可能耗时
	// 一分钟，浏览器面板是唯一可视化入口，须立即出现）----
	mux := http.NewServeMux()
	// admin 不再单独监听端口，其 handler 直接挂载到统一入口。
	mux.HandleFunc("/admin/", app.handleAdmin)
	mux.HandleFunc("/admin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/api/admin/retry", app.handleAdminRetry)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(panelHTML)
	})
	mux.HandleFunc("/api/status", app.handleStatus)
	mux.HandleFunc("/api/logs", app.handleLogs)
	mux.HandleFunc("/api/start", app.actionHandler("start"))
	mux.HandleFunc("/api/stop", app.actionHandler("stop"))
	mux.HandleFunc("/api/restart", app.actionHandler("restart"))
	mux.HandleFunc("/api/game/config", app.handleGameConfig)
	mux.HandleFunc("/api/game/launch", app.handleGameLaunch)
	mux.HandleFunc("/api/hosts", app.handleHosts)
	mux.HandleFunc("/api/open-admin", func(w http.ResponseWriter, r *http.Request) {
		openBrowser(app.panelURL + "#admin")
		_ = r
		w.WriteHeader(204)
	})
	ln, err := net.Listen("tcp", *panelAddr)
	if err != nil {
		showFatal("控制面板监听失败", err)
		return
	}
	app.panelURL = "http://" + normalizeLocal(ln.Addr().String())
	go func() { _ = http.Serve(ln, mux) }()
	// 启动即用默认浏览器打开统一控制中心
	openBrowser(app.panelURL)

	// ---- 首次初始化（幂等）----
	if seed.NeedsSeed(cfg, *staticDB) {
		app.log.Info("正在初始化数据库（首跑需导入静态配置，请稍候）…")
		res, err := seed.Ensure(context.Background(), cfg, *staticDB, *adminUser, *adminPass, app.log)
		if err != nil {
			showFatal("数据库初始化失败", err)
			return
		}
		app.log.Info(fmt.Sprintf("初始化完成 seeded=%v tables=%d admin_created=%v elapsed=%s",
			res.Seeded, res.Tables, res.AdminCreated, res.Elapsed.Round(time.Millisecond)))
		if res.AdminCreated {
			app.log.Warn("已创建默认管理员 admin/" + *adminPass + "，请尽快在管理后台修改")
		}
	}

	// ---- 内嵌管理后台 ----
	if err := app.initAdmin(app.ctx); err != nil {
		app.log.Error("管理后台初始化失败: " + err.Error())
	}
	updateTrayIcon(app)

	// ---- 监督器 ----
	binDir := filepath.Dir(mustExePath())
	app.sup = supervisor.New(binDir, *configPath, "logs")
	app.sup.SetStateCallback(func() { updateTrayIcon(app) })

	// 上次异常退出可能遗留 launcher 管理的 hosts 行；先按归属状态恢复官方解析。
	if err := hostsRestore(hostsFilePath()); err != nil {
		app.log.Warn("清理上次 hosts 遗留状态失败: " + err.Error())
	}

	// ---- 按序拉起游戏服务 ----
	go func() {
		if err := app.sup.StartAll(); err != nil {
			app.log.Error("进程启动失败: " + err.Error())
			return
		}
		if app.cfg.Launcher.HostsSwitch {
			if err := hostsSetLocal(hostsFilePath(), loginDomains(app.cfg)); err != nil {
				app.log.Warn("hosts 自动切换失败（请以管理员身份运行 launcher）: " + err.Error() +
					"；可手动将 api.rf4game.ru/com 指向 127.0.0.1，或以管理员 PowerShell 运行 tools/hosts.ps1 local")
			} else {
				app.log.Info("hosts 已指向本地服，launcher 退出时自动还原")
			}
		}
	}()

	// ---- 托盘常驻 ----
	systray.Run(app.onTrayReady, func() {
		app.sup.StopAll()
		if app.cfg.Launcher.HostsSwitch {
			if err := hostsRestore(hostsFilePath()); err != nil {
				app.log.Error("hosts 还原失败: " + err.Error() +
					"；hosts 仍指向本地服，如需回官服请手动删除 hosts 中的 rf4s-local 行，或以管理员 PowerShell 运行 tools/hosts.ps1 official")
			} else {
				app.log.Info("hosts 已还原为官方解析")
			}
		}
	})
}

// ---- 托盘 ----

var (
	mItemPanel   *systray.MenuItem
	mItemAdmin   *systray.MenuItem
	mItemStart   *systray.MenuItem
	mItemStop    *systray.MenuItem
	mItemRestart *systray.MenuItem
	mItemQuit    *systray.MenuItem
)

func (a *app) onTrayReady() {
	systray.SetIcon(iconColor(3)) // 初始灰色由状态回调刷新
	systray.SetTitle("fish-localserver")
	systray.SetTooltip("fish-localserver 本地服务器")

	mItemPanel = systray.AddMenuItem("打开控制中心", "进程状态、日志与游戏管理")
	mItemAdmin = systray.AddMenuItem("打开管理后台", "直接进入统一控制中心的管理页面")
	systray.AddSeparator()
	mItemStart = systray.AddMenuItem("启动全部", "按序启动两个游戏服务")
	mItemStop = systray.AddMenuItem("停止全部", "停止全部游戏服务")
	mItemRestart = systray.AddMenuItem("重启全部", "重启全部游戏服务")
	systray.AddSeparator()
	mItemQuit = systray.AddMenuItem("退出", "停止服务并退出")

	go func() {
		for range mItemPanel.ClickedCh {
			openBrowser(a.panelURL)
		}
	}()
	go func() {
		for range mItemAdmin.ClickedCh {
			openBrowser(a.panelURL + "#admin")
		}
	}()
	go func() {
		for range mItemStart.ClickedCh {
			go func() { _ = a.sup.StartAll() }()
		}
	}()
	go func() {
		for range mItemStop.ClickedCh {
			a.sup.StopAll()
		}
	}()
	go func() {
		for range mItemRestart.ClickedCh {
			a.sup.StopAll()
			time.Sleep(500 * time.Millisecond)
			go func() { _ = a.sup.StartAll() }()
		}
	}()
	go func() {
		for range mItemQuit.ClickedCh {
			systray.Quit()
		}
	}()

	updateTrayIcon(a)
}

// updateTrayIcon 按运行中进程数切换托盘颜色（绿=全部 / 黄=部分 / 红=无）。
func updateTrayIcon(a *app) {
	if a.sup == nil {
		return
	}
	running := 0
	for _, p := range a.sup.Status() {
		if p.State == supervisor.StateRunning {
			running++
		}
	}
	a.adminMu.RLock()
	adminReady := a.adminSrv != nil
	a.adminMu.RUnlock()
	switch {
	case running == 2 && adminReady:
		systray.SetIcon(iconColor(0)) // 绿
		systray.SetTooltip("fish-localserver：全部就绪")
	case running > 0 || adminReady:
		systray.SetIcon(iconColor(1)) // 黄
		systray.SetTooltip(fmt.Sprintf("fish-localserver：%d/2 游戏服务，管理后台=%v", running, adminReady))
	default:
		systray.SetIcon(iconColor(2)) // 红
		systray.SetTooltip("fish-localserver：已停止")
	}
}

// ---- Web 面板 API ----

func (a *app) handleStatus(w http.ResponseWriter, _ *http.Request) {
	// sup 为 nil = 首跑 seed 尚未结束
	procs := []supervisor.ProcInfo{}
	lastErr := ""
	if a.sup != nil {
		procs = a.sup.Status()
		lastErr = a.sup.LastError()
	}
	a.adminMu.RLock()
	adminReady := a.adminSrv != nil
	adminErr := a.adminErr
	a.adminMu.RUnlock()
	gamePath, gameRegion := a.gameState()
	writeJSON(w, map[string]any{
		"procs":       procs,
		"last_error":  lastErr,
		"admin":       "/admin/",
		"admin_ready": adminReady,
		"admin_error": adminErr,
		"game_path":   gamePath,
		"game_region": gameRegion,
		"hosts":       hostsMode(hostsFilePath()),
	})
}

// handleHosts 面板 hosts 指向开关：GET 返回当前指向，POST ?mode=local|official 切换。
func (a *app) handleHosts(w http.ResponseWriter, r *http.Request) {
	path := hostsFilePath()
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"mode": hostsMode(path)})
	case http.MethodPost:
		mode := r.URL.Query().Get("mode")
		var err error
		switch mode {
		case "local":
			err = hostsSetLocal(path, loginDomains(a.cfg))
		case "official":
			err = hostsRestore(path)
		default:
			httpError(w, "mode 必须是 local 或 official")
			return
		}
		if err != nil {
			httpError(w, "hosts 写入失败（请以管理员身份运行 launcher）: "+err.Error()+
				"；可手动将 api.rf4game.ru/com 指向 127.0.0.1，或以管理员 PowerShell 运行 tools/hosts.ps1 local")
			return
		}
		a.log.Info("hosts 指向已切换为 " + map[string]string{"local": "本地服务器", "official": "官方服务器"}[mode])
		w.WriteHeader(204)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (a *app) handleLogs(w http.ResponseWriter, r *http.Request) {
	if a.sup == nil { // 首跑 seed 阶段
		writeJSON(w, map[string]any{"lines": []string{}, "next": 0})
		return
	}
	name := r.URL.Query().Get("name")
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	lines, next := a.sup.Logs(name, since)
	writeJSON(w, map[string]any{"lines": lines, "next": next})
}

func (a *app) actionHandler(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.sup == nil {
			httpError(w, "服务初始化中，请稍候")
			return
		}
		name := r.URL.Query().Get("name")
		var err error
		switch action {
		case "start":
			if name == "" {
				err = a.sup.StartAll()
			} else {
				err = a.sup.StartOne(name)
			}
		case "stop":
			if name == "" {
				a.sup.StopAll()
			} else {
				a.sup.StopOne(name)
			}
		case "restart":
			if name == "" {
				a.sup.StopAll()
				time.Sleep(500 * time.Millisecond)
				err = a.sup.StartAll()
			} else {
				err = a.sup.RestartOne(name)
			}
		}
		if err != nil {
			httpError(w, err.Error())
			return
		}
		w.WriteHeader(204)
	}
}

func (a *app) initAdmin(ctx context.Context) error {
	a.adminInitMu.Lock()
	defer a.adminInitMu.Unlock()

	a.adminMu.RLock()
	ready := a.adminSrv != nil
	a.adminMu.RUnlock()
	if ready {
		return nil
	}

	db, err := store.OpenWithStatic(a.cfg.SQLite.Path, a.cfg.SQLite.StaticPath)
	if err != nil {
		a.setAdminError(err)
		return err
	}
	if err := db.Migrate(); err != nil {
		_ = db.Close()
		a.setAdminError(err)
		return err
	}
	user, pass := a.cfg.Admin.Username, a.cfg.Admin.Password
	if user == "" {
		user, pass = "admin", "admin123"
	}
	if _, err := db.UpsertAdminPassword(ctx, user, pass); err != nil {
		_ = db.Close()
		a.setAdminError(err)
		return err
	}
	srv := admin.NewServer(a.cfg.Admin, db, adminLogger{a.log}, admin.WebFS())
	a.adminMu.Lock()
	a.adminDB = db
	a.adminSrv = srv
	a.adminErr = ""
	a.adminMu.Unlock()
	go a.trackAdminSessions(a.ctx, db, srv)
	a.log.Info("管理后台已挂载到 /admin/")
	return nil
}

func (a *app) setAdminError(err error) {
	a.adminMu.Lock()
	a.adminErr = err.Error()
	a.adminMu.Unlock()
}

func (a *app) handleAdmin(w http.ResponseWriter, r *http.Request) {
	a.adminMu.RLock()
	srv := a.adminSrv
	a.adminMu.RUnlock()
	if srv == nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprint(w, `<!doctype html><meta charset="utf-8"><title>管理后台未就绪</title><style>body{background:#09111d;color:#e8f0f8;font:15px/1.7 "Segoe UI",sans-serif;padding:48px}</style><h2>管理后台尚未就绪</h2><p>请返回“运行控制”查看初始化状态并重试。</p>`)
		return
	}
	http.StripPrefix("/admin", srv.Handler()).ServeHTTP(w, r)
}

func (a *app) handleAdminRetry(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := a.initAdmin(ctx); err != nil {
		httpError(w, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *app) trackAdminSessions(ctx context.Context, db *store.DB, srv *admin.Server) {
	update := func() {
		if n, ok, _ := db.KVGet(ctx, "stats:sessions"); ok {
			var v int64
			_, _ = fmt.Sscanf(n, "%d", &v)
			srv.SetSessionCount(v)
		}
	}
	update()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			update()
		}
	}
}

func (a *app) closeAdmin() {
	if a.cancel != nil {
		a.cancel()
	}
	a.adminMu.Lock()
	db := a.adminDB
	a.adminDB = nil
	a.adminSrv = nil
	a.adminMu.Unlock()
	if db != nil {
		_ = db.Close()
	}
}

// ---- 辅助 ----

type panelLogger struct{}

func (panelLogger) Info(m string)  { fmt.Println("launcher:", m) }
func (panelLogger) Warn(m string)  { fmt.Println("launcher WARN:", m) }
func (panelLogger) Error(m string) { fmt.Println("launcher ERROR:", m) }

func logMessage(msg string, args ...any) string {
	if len(args) == 0 {
		return msg
	}
	return msg + " " + fmt.Sprint(args...)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(500)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// normalizeLocal ":9500"/"9500" → "127.0.0.1:9500"；host:port 原样返回。
func normalizeLocal(addr string) string {
	switch {
	case addr == "":
		return "127.0.0.1"
	case strings.HasPrefix(addr, ":"):
		return "127.0.0.1" + addr
	case !strings.Contains(addr, ":"):
		return "127.0.0.1:" + addr
	}
	return addr
}

func mustExePath() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe
}

func openBrowser(url string) {
	_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

func showFatal(msg string, err error) {
	panelLogger{}.Error(msg + ": " + err.Error())
	// 无控制台（windowsgui）场景至少把错误写进面板可达的日志文件
	_ = os.WriteFile("launcher_error.log", []byte(msg+": "+err.Error()+"\n"), 0o644)
}

func showTrayTip(_ string) {}

// resolveWorkDir 定位包含 config.yaml 的工作目录（CWD → exe 目录 → exe 上级）。
func resolveWorkDir(configPath string) string {
	candidates := []string{"."}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates, dir, filepath.Dir(dir))
	}
	for _, dir := range candidates {
		full := configPath
		if dir != "." {
			full = filepath.Join(dir, configPath)
		}
		if _, err := os.Stat(full); err == nil {
			if dir == "." {
				return "" // 已在正确目录
			}
			return dir
		}
	}
	return "" // 找不到就留在 CWD（config.Load 会给出明确报错）
}

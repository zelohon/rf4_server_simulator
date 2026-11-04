// Package supervisor —— 子进程监督：按序启动、健康等待、环形日志、退避重启、启停 API。
//
// launcher（托盘/Web 面板）与命令行场景共用。子进程以同目录下的可执行文件
// 启动，stdout/stderr 经管道转发进环形缓冲（面板增量拉取）并同时落盘
// logs/<name>.log。任一进程意外退出按指数退避自动重启；显式 Stop 不重启。
package supervisor

import (
	"bufio"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ProcState 进程状态。
type ProcState string

const (
	StateStopped  ProcState = "stopped"
	StateStarting ProcState = "starting"
	StateRunning  ProcState = "running"
	StateBackoff  ProcState = "backoff" // 意外退出，等待重启
	StateFailed   ProcState = "failed"  // 连续失败放弃
)

const (
	ringLines      = 800 // 每进程内存日志行数
	maxBackoff     = 30 * time.Second
	healthWait     = 20 * time.Second
	maxConsecFails = 5
)

// Spec 一个受管进程的定义。
type Spec struct {
	Name     string // gameserver / gateway
	ExeName  string // 可执行文件名（相对 launcher 同目录）
	Args     []string
	ObsPort  string // 观测端口（":9502" 形式；健康检查用，空=不检查）
	LogFile  string
	Critical bool // true：该进程退出则全组停机
}

// ProcInfo 面板展示的进程快照。
type ProcInfo struct {
	Name     string    `json:"name"`
	State    ProcState `json:"state"`
	PID      int       `json:"pid"`
	Restarts int       `json:"restarts"`
	UptimeS  float64   `json:"uptime_s"`
}

type proc struct {
	spec      Spec
	cmd       *exec.Cmd
	state     ProcState
	pid       int
	startedAt time.Time
	restarts  int
	fails     int

	ringMu  sync.Mutex
	ring    []string // 环形日志（含时间戳前缀）
	ringSeq int64    // 已写入总行数（增量拉取游标）

	stopReq bool // 用户显式停止
	logFile *os.File
}

// Supervisor 全组监督器。
type Supervisor struct {
	mu       sync.Mutex
	dir      string // 可执行文件所在目录
	config   string // 传给子进程的 -config 值
	procs    map[string]*proc
	order    []string // 启动顺序
	stopping bool
	logDir   string
	onState  func() // 状态变化回调（托盘图标刷新），可为 nil

	lgMu    sync.Mutex
	lg      []string // launcher 级进度日志（StartAll 每步）
	lgSeq   int64
	LastErr string // StartAll/StartOne 的最近错误（面板展示）
}

// New 构建监督器。binDir 为子进程可执行目录；configPath 透传 -config；
// logDir 为日志落盘目录（空=不落盘）。
func New(binDir, configPath, logDir string) *Supervisor {
	s := &Supervisor{
		dir:    binDir,
		config: configPath,
		procs:  map[string]*proc{},
		logDir: logDir,
	}
	// 启动顺序即依赖顺序：gameserver（隧道服务端）→ gateway（接入）。
	// 管理后台 handler 内嵌在 launcher 中，不再作为独立监听进程启动。
	for _, sp := range []Spec{
		{Name: "gameserver", ExeName: "gameserver.exe", ObsPort: "9502", LogFile: "gameserver.log"},
		{Name: "gateway", ExeName: "gateway.exe", ObsPort: "9501", LogFile: "gateway.log"},
	} {
		sp.Args = []string{"-config", configPath}
		p := &proc{spec: sp, state: StateStopped}
		s.procs[sp.Name] = p
		s.order = append(s.order, sp.Name)
	}
	return s
}

// SetStateCallback 注册状态变化回调（托盘图标刷新）。
func (s *Supervisor) SetStateCallback(f func()) { s.onState = f }

func (s *Supervisor) notify() {
	if s.onState != nil {
		s.onState()
	}
}

// slogf 记 launcher 级进度（/api/logs?name=_launcher 增量拉取）。
func (s *Supervisor) slogf(format string, a ...any) {
	line := time.Now().Format("15:04:05") + " " + fmt.Sprintf(format, a...)
	s.lgMu.Lock()
	s.lg = append(s.lg, line)
	if len(s.lg) > ringLines {
		s.lg = s.lg[len(s.lg)-ringLines:]
	}
	s.lgSeq++
	s.lgMu.Unlock()
}

func (p *proc) logf(format string, a ...any) {
	line := time.Now().Format("15:04:05") + " " + fmt.Sprintf(format, a...)
	p.ringMu.Lock()
	p.ring = append(p.ring, line)
	if len(p.ring) > ringLines {
		p.ring = p.ring[len(p.ring)-ringLines:]
	}
	p.ringSeq++
	if p.logFile != nil {
		_, _ = p.logFile.WriteString(line + "\n")
	}
	p.ringMu.Unlock()
}

// StartAll 按序启动全部进程（每个等到健康或超时）。
func (s *Supervisor) StartAll() error {
	s.mu.Lock()
	s.stopping = false
	s.mu.Unlock()
	for _, name := range s.order {
		s.slogf("启动 %s …", name)
		if err := s.StartOne(name); err != nil {
			s.LastErr = err.Error()
			s.slogf("启动 %s 失败: %v", name, err)
			return fmt.Errorf("%s: %w", name, err)
		}
		p := s.procs[name]
		if p.spec.ObsPort != "" {
			if err := waitHealthy(p.spec.ObsPort, healthWait); err != nil {
				s.LastErr = err.Error()
				s.slogf("%s 健康检查超时: %v", name, err)
				return fmt.Errorf("%s 健康检查超时: %w", name, err)
			}
		}
		s.slogf("%s 就绪", name)
	}
	s.LastErr = ""
	s.slogf("全部进程就绪")
	return nil
}

// StartOne 启动单个进程（进入监督循环）。
func (s *Supervisor) StartOne(name string) error {
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return errors.New("正在停止，拒绝启动")
	}
	p := s.procs[name]
	if p == nil {
		s.mu.Unlock()
		return fmt.Errorf("未知进程 %s", name)
	}
	if p.state == StateRunning || p.state == StateStarting {
		s.mu.Unlock()
		return nil // 已在运行
	}
	p.stopReq = false
	p.state = StateStarting
	s.mu.Unlock()

	if err := s.launch(p); err != nil {
		p.state = StateFailed
		s.slogf("launch %s 失败: %v", name, err)
		s.notify()
		return err
	}
	go s.supervise(p)
	s.notify()
	return nil
}

// launch 实际拉起子进程（日志管道 + 落盘）。
func (s *Supervisor) launch(p *proc) error {
	exe := filepath.Join(s.dir, p.spec.ExeName)
	if _, err := os.Stat(exe); err != nil {
		return fmt.Errorf("找不到 %s（请确认与 launcher 同目录）", p.spec.ExeName)
	}
	if s.logDir != "" && p.logFile == nil {
		_ = os.MkdirAll(s.logDir, 0o755)
		if f, err := os.OpenFile(filepath.Join(s.logDir, p.spec.LogFile),
			os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			p.logFile = f
		}
	}
	cmd := exec.Command(exe, p.spec.Args...)
	cmd.SysProcAttr = childSysProcAttr() // Windows: 隐藏子进程控制台黑框
	// 子进程工作目录与 launcher 相同，使 config、server_data、data 等
	// 相对路径保持一致；launcher 启动时已切换到仓库根目录。
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout // 合并 stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	p.cmd = cmd
	p.pid = cmd.Process.Pid
	p.state = StateRunning
	p.startedAt = time.Now()
	p.logf("[%s] 已启动 pid=%d", p.spec.Name, p.pid)

	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			p.logf("%s", sc.Text())
		}
	}()
	return nil
}

// supervise 监督循环：等待退出；意外退出按退避重启。
func (s *Supervisor) supervise(p *proc) {
	backoff := time.Second
	for {
		err := p.cmd.Wait()
		s.mu.Lock()
		if s.stopping || p.stopReq {
			p.state = StateStopped
			p.pid = 0
			s.mu.Unlock()
			p.logf("[%s] 已停止", p.spec.Name)
			s.notify()
			return
		}
		p.fails++
		p.restarts++
		consec := p.fails
		if consec > maxConsecFails {
			p.state = StateFailed
			s.mu.Unlock()
			p.logf("[%s] 意外退出（%v）；连续失败超过 %d 次，放弃重启",
				p.spec.Name, err, maxConsecFails)
			s.notify()
			return
		}
		p.state = StateBackoff
		s.mu.Unlock()
		p.logf("[%s] 意外退出（%v），第 %d 次重启（连续失败 %d）",
			p.spec.Name, err, p.restarts, consec)
		s.notify()

		time.Sleep(backoff)
		backoff = min(backoff*2, maxBackoff)

		s.mu.Lock()
		if s.stopping || p.stopReq {
			p.state = StateStopped
			p.pid = 0
			s.mu.Unlock()
			s.notify()
			return
		}
		s.mu.Unlock()
		if err := s.launch(p); err != nil {
			p.logf("[%s] 重启失败: %v", p.spec.Name, err)
			continue
		}
		s.mu.Lock()
		p.fails = 0 // 拉起成功清零连续失败
		s.mu.Unlock()
		s.notify()
	}
}

// StopOne 停止单个进程（显式停止，不自动重启）。
func (s *Supervisor) StopOne(name string) {
	s.mu.Lock()
	p := s.procs[name]
	if p == nil {
		s.mu.Unlock()
		return
	}
	p.stopReq = true
	s.mu.Unlock()
	if p.cmd != nil && p.cmd.Process != nil {
		p.logf("[%s] 收到停止指令", name)
		_ = p.cmd.Process.Kill() // SQLite WAL 下安全；硬杀避免 Windows 信号复杂性
	}
}

// StopAll 停止全部（退出用）。
func (s *Supervisor) StopAll() {
	s.mu.Lock()
	s.stopping = true
	s.mu.Unlock()
	for _, name := range s.order {
		s.StopOne(name)
	}
	// 等待全部落定（最多 10s）
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		busy := false
		for _, p := range s.procs {
			if p.state == StateRunning || p.state == StateStarting || p.state == StateBackoff {
				busy = true
			}
		}
		if !busy {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// RestartOne 重启单个进程。
func (s *Supervisor) RestartOne(name string) error {
	s.StopOne(name)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if s.procs[name].state == StateStopped {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	return s.StartOne(name)
}

// LastError StartAll 最近错误（空=无）。
func (s *Supervisor) LastError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.LastErr
}

// Status 全组快照。
func (s *Supervisor) Status() []ProcInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ProcInfo, 0, len(s.order))
	for _, name := range s.order {
		p := s.procs[name]
		uptime := 0.0
		if p.state == StateRunning && !p.startedAt.IsZero() {
			uptime = time.Since(p.startedAt).Seconds()
		}
		out = append(out, ProcInfo{
			Name: name, State: p.state, PID: p.pid,
			Restarts: p.restarts, UptimeS: uptime,
		})
	}
	return out
}

// AllRunning 全部处于 running。
func (s *Supervisor) AllRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.procs {
		if p.state != StateRunning {
			return false
		}
	}
	return true
}

// Logs 增量取环形日志：since 为上次游标（0=从头取现有全部）。
// name="_launcher" 返回 launcher 级进度日志。
func (s *Supervisor) Logs(name string, since int64) ([]string, int64) {
	if name == "_launcher" {
		s.lgMu.Lock()
		defer s.lgMu.Unlock()
		start := int64(len(s.lg)) - (s.lgSeq - since)
		if start < 0 {
			start = 0
		}
		if start > int64(len(s.lg)) {
			start = int64(len(s.lg))
		}
		out := make([]string, int64(len(s.lg))-start)
		copy(out, s.lg[start:])
		return out, s.lgSeq
	}
	s.mu.Lock()
	p := s.procs[name]
	s.mu.Unlock()
	if p == nil {
		return nil, 0
	}
	p.ringMu.Lock()
	defer p.ringMu.Unlock()
	start := int64(len(p.ring)) - (p.ringSeq - since)
	if start < 0 {
		start = 0
	}
	if start > int64(len(p.ring)) {
		start = int64(len(p.ring))
	}
	out := make([]string, int64(len(p.ring))-start)
	copy(out, p.ring[start:])
	return out, p.ringSeq
}

// waitHealthy 轮询观测端口 /healthz。
func waitHealthy(obsPort string, timeout time.Duration) error {
	addr := normalizeObs(obsPort)
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get("http://" + addr + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("%s: %v", addr, lastErr)
}

// normalizeObs "9502"/":9502"/"host:port" → "127.0.0.1:9502"/原样。
func normalizeObs(addr string) string {
	if !strings.Contains(addr, ":") {
		return "127.0.0.1:" + addr
	}
	if strings.HasPrefix(addr, ":") {
		return "127.0.0.1" + addr
	}
	return addr
}

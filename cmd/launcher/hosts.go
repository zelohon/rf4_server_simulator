// hosts.go —— hosts_switch 功能：launcher 启动服务后把登录域名指向
// 127.0.0.1，退出时还原，省去手动编辑 hosts 的步骤。
//
// 只还原 launcher 自己启用的行：归属状态保存在 hosts 同目录的 sidecar 文件，
// 不会把用户原本已有的 127.0.0.1 映射误当作 launcher 管理项。
//
// 写 hosts 需要管理员权限；失败时调用方记录告警，不阻断服务启动。
package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/sys/windows"

	"rf4server/internal/config"
)

const (
	hostsSection  = "# RF4 local server"
	hostsMarker   = "# rf4s-local"
	hostsLocalRow = "127.0.0.1 "
)

// hostsState 记录 launcher 实际启用的域名。状态写在 hosts 文件旁，随退出
// 或下一次启动恢复，崩溃后也能识别归属。
type hostsState struct {
	Owned []string `json:"owned,omitempty"`
}

var (
	reHostsActive  = regexp.MustCompile(`^127\.0\.0\.1\s+(api\.rf4game\.(?:ru|com))\s*$`)
	reHostsComment = regexp.MustCompile(`^#\s*rf4s-local\s+127\.0\.0\.1\s+(api\.rf4game\.(?:ru|com))`)
)

// hostsFilePath 返回系统 hosts 文件路径。
func hostsFilePath() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return root + `\System32\drivers\etc\hosts`
}

// loginDomains 返回需要指向本机的登录域名（主域名 + 备用 com 域，去重）。
func loginDomains(cfg *config.Config) []string {
	ru, com := cfg.Gateway.LoginDomain, "api.rf4game.com"
	if ru == com {
		return []string{ru}
	}
	return []string{ru, com}
}

func hostsStatePath(path string) string {
	return path + ".rf4s-state.json"
}

func readHostsState(path string) hostsState {
	var st hostsState
	data, err := os.ReadFile(hostsStatePath(path))
	if err == nil {
		_ = json.Unmarshal(data, &st)
	}
	return st
}

func saveHostsState(path string, st hostsState) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(hostsStatePath(path), data, 0o600)
}

func backupHosts(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	mode := fs.FileMode(0o644)
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	}
	backup := fmt.Sprintf("%s.rf4s-backup-%s", path, time.Now().UTC().Format("20060102T150405.000000000"))
	return os.WriteFile(backup, data, mode)
}

// writeFileAtomic 先写同目录临时文件，再通过 Windows MoveFileEx 替换目标。
// 这样 hosts 或状态文件不会在写入中途被截断。
func writeFileAtomic(path string, data []byte, mode fs.FileMode) (err error) {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	if err = f.Chmod(mode); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	from, err := windows.UTF16PtrFromString(tmp)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	err = windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
	return err
}

// hostsSetLocal 将本项目管理的行切换为启用状态（标准 hosts 格式，指向
// 127.0.0.1）。用户原本已有的同域名映射保持不变且不会被标记为 launcher
// 所有；只有本项目新增或从 rf4s-local 注释恢复的行才会在退出时还原。
func hostsSetLocal(path string, domains []string) error {
	text, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	oldState := readHostsState(path)
	oldOwned := make(map[string]bool, len(oldState.Owned))
	for _, d := range oldState.Owned {
		oldOwned[d] = true
	}
	lines := strings.Split(strings.ReplaceAll(string(text), "\r\n", "\n"), "\n")
	requested := make(map[string]bool, len(domains))
	for _, d := range domains {
		if d != "" {
			requested[d] = true
		}
	}
	active := make(map[string]bool, len(domains))
	for _, line := range lines {
		if m := reHostsActive.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			active[m[1]] = true
		}
	}

	out := make([]string, 0, len(lines)+len(domains)+2)
	owned := make(map[string]bool, len(domains))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if m := reHostsActive.FindStringSubmatch(trimmed); m != nil {
			domain := m[1]
			out = append(out, trimmed)
			if requested[domain] && oldOwned[domain] {
				owned[domain] = true
			}
			continue
		}
		if m := reHostsComment.FindStringSubmatch(trimmed); m != nil {
			domain := m[1]
			if requested[domain] && !active[domain] {
				out = append(out, hostsLocalRow+domain)
				active[domain] = true
				owned[domain] = true
				continue
			}
			if requested[domain] && active[domain] {
				continue
			}
		}
		out = append(out, strings.TrimRight(line, "\r"))
	}

	sectionAdded := false
	for _, d := range domains {
		if d == "" || active[d] {
			continue
		}
		if !sectionAdded {
			if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
				out = append(out, "")
			}
			out = append(out, hostsSection)
			sectionAdded = true
		}
		out = append(out, hostsLocalRow+d)
		active[d] = true
		owned[d] = true
	}
	state := hostsState{Owned: make([]string, 0, len(owned))}
	for _, d := range domains {
		if owned[d] {
			state.Owned = append(state.Owned, d)
			delete(owned, d)
		}
	}
	for d := range owned {
		state.Owned = append(state.Owned, d)
	}

	if err := backupHosts(path); err != nil {
		return err
	}
	// 先保存归属，再改 hosts；即使随后写 hosts 失败，下次启动也不会误认用户行。
	if err := saveHostsState(path, state); err != nil {
		return err
	}
	return writeFileAtomic(path, []byte(strings.Join(out, "\r\n")), 0o644)
}

// hostsMode 判断当前 hosts 指向：local=存在生效的标准格式行；official=
// 生效行全部处于注释状态或不存在；unknown=读取失败。
func hostsMode(path string) string {
	text, err := os.ReadFile(path)
	if err != nil {
		return "unknown"
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(text), "\r\n", "\n"), "\n") {
		if reHostsActive.MatchString(strings.TrimSpace(line)) {
			return "local"
		}
	}
	return "official"
}

// hostsRestore 只注释 launcher 归属状态中记录的行，恢复官方解析。幂等。
func hostsRestore(path string) error {
	st := readHostsState(path)
	if len(st.Owned) == 0 {
		return nil
	}
	owned := make(map[string]bool, len(st.Owned))
	for _, d := range st.Owned {
		owned[d] = true
	}
	text, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.ReplaceAll(string(text), "\r\n", "\n"), "\n")
	changed := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if m := reHostsActive.FindStringSubmatch(trimmed); m != nil && owned[m[1]] {
			lines[i] = hostsMarker + " " + trimmed
			changed = true
		}
	}
	if !changed {
		return saveHostsState(path, hostsState{})
	}
	if err := backupHosts(path); err != nil {
		return err
	}
	if err := writeFileAtomic(path, []byte(strings.Join(lines, "\r\n")), 0o644); err != nil {
		return err
	}
	return saveHostsState(path, hostsState{})
}

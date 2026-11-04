package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rf4server/internal/config"
)

func writeHosts(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readHosts(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

var testDomains = []string{"api.rf4game.ru", "api.rf4game.com"}

func TestHostsSetLocalAppends(t *testing.T) {
	path := writeHosts(t, "127.0.0.1 localhost\n")
	if err := hostsSetLocal(path, testDomains); err != nil {
		t.Fatal(err)
	}
	got := readHosts(t, path)
	for _, want := range []string{
		"\n127.0.0.1 api.rf4game.ru",
		"\n127.0.0.1 api.rf4game.com",
		"# RF4 local server",
		"127.0.0.1 localhost",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("缺少 %q:\n%s", want, got)
		}
	}
	// 启用行必须是标准格式（首列 IP），不能带 rf4s-local 前缀
	if strings.Contains(got, "\nrf4s-local") {
		t.Fatalf("启用行格式非法（首列必须是 IP）:\n%s", got)
	}
}

func TestHostsSetLocalIdempotent(t *testing.T) {
	path := writeHosts(t, "127.0.0.1 localhost\n")
	_ = hostsSetLocal(path, testDomains)
	_ = hostsSetLocal(path, testDomains)
	got := readHosts(t, path)
	if n := strings.Count(got, "127.0.0.1 api.rf4game"); n != 2 {
		t.Fatalf("启用行数量=%d，期望 2:\n%s", n, got)
	}
	if strings.Contains(got, "# rf4s-local") {
		t.Fatalf("不应残留注释行:\n%s", got)
	}
}

func TestHostsRestoreComments(t *testing.T) {
	path := writeHosts(t, "127.0.0.1 localhost\n")
	_ = hostsSetLocal(path, testDomains)
	if err := hostsRestore(path); err != nil {
		t.Fatal(err)
	}
	got := readHosts(t, path)
	if !strings.Contains(got, "# rf4s-local 127.0.0.1 api.rf4game.ru") {
		t.Fatalf("未按 hosts.ps1 约定注释:\n%s", got)
	}
	if hostsMode(path) != "official" {
		t.Fatalf("还原后 mode 应为 official")
	}
	// 幂等：再次还原不报错不变样
	if err := hostsRestore(path); err != nil {
		t.Fatal(err)
	}
	if got != readHosts(t, path) {
		t.Fatal("幂等还原结果不一致")
	}
}

func TestHostsReenableFromCommented(t *testing.T) {
	// 注释态（如手动跑过 hosts.ps1 official）→ 重新启用应恢复标准生效行
	path := writeHosts(t, "# RF4 local server\n# rf4s-local 127.0.0.1 api.rf4game.ru\n# rf4s-local 127.0.0.1 api.rf4game.com\n")
	if err := hostsSetLocal(path, testDomains); err != nil {
		t.Fatal(err)
	}
	got := readHosts(t, path)
	if strings.Contains(got, "# rf4s-local") {
		t.Fatalf("注释行未被启用:\n%s", got)
	}
	if !strings.Contains(got, "\n127.0.0.1 api.rf4game.com") {
		t.Fatalf("启用行缺失:\n%s", got)
	}
	if hostsMode(path) != "local" {
		t.Fatalf("重新启用后 mode 应为 local")
	}
}

func TestHostsMode(t *testing.T) {
	path := writeHosts(t, "127.0.0.1 localhost\n")
	if got := hostsMode(path); got != "official" {
		t.Fatalf("无生效行应视为 official，得到 %s", got)
	}
	_ = hostsSetLocal(path, testDomains)
	if got := hostsMode(path); got != "local" {
		t.Fatalf("启用后应为 local，得到 %s", got)
	}
	_ = hostsRestore(path)
	if got := hostsMode(path); got != "official" {
		t.Fatalf("注释后应为 official，得到 %s", got)
	}
	if got := hostsMode(filepath.Join(t.TempDir(), "missing")); got != "unknown" {
		t.Fatalf("文件缺失应为 unknown，得到 %s", got)
	}
}

func TestHostsAcceptsPs1StyleComment(t *testing.T) {
	// hosts.ps1 写入的注释行带 "# rf4s-local " 前缀，launcher 必须能识别恢复
	path := writeHosts(t, "# rf4s-local 127.0.0.1 api.rf4game.ru\n# rf4s-local 127.0.0.1 api.rf4game.com\n")
	if err := hostsSetLocal(path, testDomains); err != nil {
		t.Fatal(err)
	}
	got := readHosts(t, path)
	if hostsMode(path) != "local" {
		t.Fatalf("hosts.ps1 注释行未恢复:\n%s", got)
	}
	// 还原后应与 hosts.ps1 的注释格式一致，hosts.ps1 才能继续识别
	_ = hostsRestore(path)
	if !strings.Contains(readHosts(t, path), "# rf4s-local 127.0.0.1 api.rf4game.ru") {
		t.Fatal("还原格式与 hosts.ps1 约定不一致")
	}
}

func TestLoginDomainsDedup(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.LoginDomain = "api.rf4game.com"
	if got := loginDomains(cfg); len(got) != 1 {
		t.Fatalf("去重失败: %v", got)
	}
}

func TestHostsRestorePreservesPreexistingActiveLine(t *testing.T) {
	path := writeHosts(t, "127.0.0.1 api.rf4game.ru\r\n")
	if err := hostsSetLocal(path, testDomains); err != nil {
		t.Fatal(err)
	}
	if err := hostsRestore(path); err != nil {
		t.Fatal(err)
	}
	got := readHosts(t, path)
	if !strings.Contains(got, "127.0.0.1 api.rf4game.ru") {
		t.Fatalf("用户原有生效行不应被改动:\n%s", got)
	}
	if !strings.Contains(got, "# rf4s-local 127.0.0.1 api.rf4game.com") {
		t.Fatalf("launcher 新增行应被还原:\n%s", got)
	}
}

func TestHostsSetLocalCreatesBackupAndState(t *testing.T) {
	original := "127.0.0.1 localhost\r\n"
	path := writeHosts(t, original)
	if err := hostsSetLocal(path, testDomains); err != nil {
		t.Fatal(err)
	}
	backups, err := filepath.Glob(path + ".rf4s-backup-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) == 0 {
		t.Fatal("修改 hosts 前应创建备份")
	}
	if got := readHosts(t, backups[0]); got != original {
		t.Fatalf("备份内容不一致: %q", got)
	}
	st := readHostsState(path)
	if len(st.Owned) != 2 {
		t.Fatalf("归属状态未保存: %+v", st)
	}
}

func TestHostsRestoreClearsOwnershipState(t *testing.T) {
	path := writeHosts(t, "127.0.0.1 localhost\r\n")
	if err := hostsSetLocal(path, testDomains); err != nil {
		t.Fatal(err)
	}
	if err := hostsRestore(path); err != nil {
		t.Fatal(err)
	}
	if st := readHostsState(path); len(st.Owned) != 0 {
		t.Fatalf("还原后归属状态应清空: %+v", st)
	}
}

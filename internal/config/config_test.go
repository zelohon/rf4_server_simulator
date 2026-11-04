package config

import "testing"

func TestStaticDatabasePathDefaultAndEnvOverride(t *testing.T) {
	if got := defaults().SQLite.StaticPath; got != "server_data/static.sqlite3" {
		t.Fatalf("默认静态库路径=%q", got)
	}
	if got := defaults().Launcher.GameRegion; got != "CN" {
		t.Fatalf("默认游戏区域=%q", got)
	}
	t.Setenv("RF4_SQLITE_STATIC_PATH", "custom/static.sqlite3")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.SQLite.StaticPath; got != "custom/static.sqlite3" {
		t.Fatalf("环境变量静态库路径=%q", got)
	}
}

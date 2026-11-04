package gameserver

import (
	"context"
	"testing"
)

// fish_weight_cap 配置兼容测试：
// 默认值必须保持原版行为（钓具承重截断开启），只有管理员显式
// 写入 0 才关闭截断；存量配置升级（无该键、非法值）不受影响。

// TestFishWeightCapDefaultPreservesVanilla 管理员未配置该键时，
// 两处截断路径（DB 装备上限 / pool 内置上限）传入的默认值必须为 1，
// 即升级后默认行为与上游一致。
func TestFishWeightCapDefaultPreservesVanilla(t *testing.T) {
	s, _ := newDetachTestServer(t)
	// 与 fishing.go 两处调用点保持一致的读取形态。
	if got := s.tunableF("fish_weight_cap", 1); got != 1 {
		t.Fatalf("未配置时 fish_weight_cap = %v, want 1（原版截断）", got)
	}
}

// TestFishWeightCapInvalidValueFallsBack 非法配置值不得关闭截断，
// 必须回退默认（1）。
func TestFishWeightCapInvalidValueFallsBack(t *testing.T) {
	s, db := newDetachTestServer(t)
	if err := db.TunableSet(context.Background(), "fish_weight_cap", "abc", "test"); err != nil {
		t.Fatal(err)
	}
	s.execAdminCmd(&AdminCommand{Op: "reload_tunables"})
	if got := s.tunableF("fish_weight_cap", 1); got != 1 {
		t.Fatalf("非法值 fish_weight_cap = %v, want 1（回退默认）", got)
	}
}

// TestFishWeightCapExplicitOffAndOn 管理员显式 0 关闭截断、改回 1 恢复，
// 走管理指令 reload 的真实热更路径。
func TestFishWeightCapExplicitOffAndOn(t *testing.T) {
	s, db := newDetachTestServer(t)
	if err := db.TunableSet(context.Background(), "fish_weight_cap", "0", "test"); err != nil {
		t.Fatal(err)
	}
	s.execAdminCmd(&AdminCommand{Op: "reload_tunables"})
	if got := s.tunableF("fish_weight_cap", 1); got != 0 {
		t.Fatalf("显式 0 后 fish_weight_cap = %v, want 0（截断关闭）", got)
	}
	if err := db.TunableSet(context.Background(), "fish_weight_cap", "1", "test"); err != nil {
		t.Fatal(err)
	}
	s.execAdminCmd(&AdminCommand{Op: "reload_tunables"})
	if got := s.tunableF("fish_weight_cap", 1); got != 1 {
		t.Fatalf("显式 1 后 fish_weight_cap = %v, want 1（截断恢复）", got)
	}
}

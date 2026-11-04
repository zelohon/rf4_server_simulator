package gameserver

import (
	"math"
	"testing"

	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

func f32At(v *proto.Value, idx int) float64 {
	f := v.Field(idx)
	return float64(proto.F32FromBits(f.U32()))
}

// TestFish27UsesVerifiedFightFields 验证 obj27 的体力、初始体力和拉力恢复到
// 旧版真实服务器样本校准出的字段语义，而不是把积分/质量塞进搏鱼字段。
func TestFish27UsesVerifiedFightFields(t *testing.T) {
	srv := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "contract"}}, logging.Discard(), nil)
	f := &RolledFish{FishGUID: "11111111111111111111111111111111", Species: "c.roach", Grade: 1, WeightG: 186, LengthM: 0.1981}
	tree := srv.buildFish27(f, "22222222222222222222222222222222", "33333333333333333333333333333333")
	if got := f32At(tree, 5); math.Abs(got-1776.7018156) > 0.001 {
		t.Fatalf("c.roach f5 体力池=%f，期望1776.7018156", got)
	}
	if got := f32At(tree, 6); math.Abs(got-0.85) > 1e-6 {
		t.Fatalf("c.roach f6 初始体力=%f，期望0.85", got)
	}
	if got := f32At(tree, 8); math.Abs(got-1.8240369047) > 0.0001 {
		t.Fatalf("c.roach f8 拉力=%f，期望1.8240369047", got)
	}

	// 两条实际样本必须严格复现。
	for _, tc := range []struct {
		weightG float64
		force   float64
	}{
		{53, 0.5197524514},
		{63, 0.6178189516},
	} {
		f = &RolledFish{FishGUID: "44444444444444444444444444444444", Species: "ruffe_n", Grade: 2, WeightG: int64(tc.weightG), LengthM: 0.18}
		tree = srv.buildFish27(f, "", "")
		if got := f32At(tree, 8); math.Abs(got-tc.force) > 1e-6 {
			t.Fatalf("%.0fg f8 拉力=%f，期望%f", tc.weightG, got, tc.force)
		}
	}
}

// TestFish27UnknownSpeciesFallback 验证未知鱼种使用回退搏鱼参数。
func TestFish27UnknownSpeciesFallback(t *testing.T) {
	srv := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "contract"}}, logging.Discard(), nil)
	f := &RolledFish{FishGUID: "55555555555555555555555555555555", Species: "not_in_calib", Grade: 1, WeightG: 100, LengthM: 0.2}
	tree := srv.buildFish27(f, "", "")
	if got := f32At(tree, 5); math.Abs(got-955.2160299) > 0.001 {
		t.Fatalf("未知鱼 f5 体力池=%f，期望955.2160299", got)
	}
	if got := f32At(tree, 6); math.Abs(got-0.85) > 1e-6 {
		t.Fatalf("未知鱼 f6 初始体力=%f，期望0.85", got)
	}
	if got := f32At(tree, 9); got < 1.35 || got > 1.37 {
		t.Fatalf("未知鱼 f9=%f，期望按94/189g校准约1.3607", got)
	}
	if got := f32At(tree, 13); math.Abs(got-0.35) > 1e-6 {
		t.Fatalf("未知鱼 f13=%f，期望0.35", got)
	}
	if got := f32At(tree, 16); math.Abs(got-0.833907) > 1e-5 {
		t.Fatalf("未知鱼 f16=%f，期望0.833907", got)
	}
	if got := f32At(tree, 20); math.Abs(got-0.997804) > 1e-5 {
		t.Fatalf("未知鱼 f20=%f，期望0.997804", got)
	}
}

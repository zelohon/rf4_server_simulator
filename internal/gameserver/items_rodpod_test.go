package gameserver

import (
	"testing"

	"rf4server/internal/wiregen"
)

// 竿架（item_def.category=79）必须有 wire 类型映射：没有映射时会落到兜底
// 类型 140（饵料），客户端按饵料模型渲染，表现为物品格空白、没有图标、
// 也无法选中。客户端 4.0.25034 期望的模型是 pphmpibfgea。
func TestRodPodCategoryWireClass(t *testing.T) {
	entry, ok := categoryWireClass[79]
	if !ok {
		t.Fatal("category 79（竿架）缺少 wire 类型映射，会落到饵料兜底类型")
	}
	if entry.Class != "abmokghdbhf" || entry.TypeID != 11500 {
		t.Fatalf("category 79 映射=%#v，期望 abmokghdbhf/11500", entry)
	}
	info, ok := wiregen.ClassMeta[entry.Class]
	if !ok {
		t.Fatalf("wiregen 缺少类 %s", entry.Class)
	}
	if info.DTO != "pphmpibfgea" {
		t.Fatalf("类 %s 的 DTO=%q，期望 pphmpibfgea（客户端竿架模型）", entry.Class, info.DTO)
	}
	if got := wiregen.ClassForType(int32(entry.TypeID)); got != entry.Class {
		t.Fatalf("type_id %d 对应类=%q，期望 %q", entry.TypeID, got, entry.Class)
	}
}

package gameserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	_ "modernc.org/sqlite"

	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

// TestLeadcoreDefinitionsCarryLengthAndRigidity 守住 category=57（铅芯线）的静态数据契约。
//
// 空白卡片的根因是实例类型落成了钓线类（见 TestLeadcoreUsesWeightQuantityClass），
// 但铅芯线的物理参数（长度/刚性）只在 item_def.attrs_json 里，2026-11-02 曾手工给
// 83 条定义补过长度=10、刚性=0.4+LB/150；11-03 整包替换 static.sqlite3 时补丁被覆盖。
// 这里在每次测试时校验仓库自带静态库，避免描述/物理估算再次退化成 0。
func TestLeadcoreDefinitionsCarryLengthAndRigidity(t *testing.T) {
	db, err := sql.Open("sqlite", "file:../../server_data/static.sqlite3?mode=ro")
	if err != nil {
		t.Fatalf("打开静态库: %v", err)
	}
	defer db.Close()

	rows, err := db.Query(`SELECT item_id, attrs_json FROM item_def WHERE category=57 ORDER BY item_id`)
	if err != nil {
		t.Fatalf("查询铅芯线定义: %v", err)
	}
	defer rows.Close()

	total := 0
	for rows.Next() {
		var itemID, attrsJSON string
		if err := rows.Scan(&itemID, &attrsJSON); err != nil {
			t.Fatalf("读取定义: %v", err)
		}
		total++
		var attrs map[string]any
		if err := json.Unmarshal([]byte(attrsJSON), &attrs); err != nil {
			t.Errorf("%s attrs_json 解析失败: %v", itemID, err)
			continue
		}
		length, _ := attrs["ljnnpokapig"].(float64)
		if length <= 0 {
			t.Errorf("%s 缺少长度 ljnnpokapig（当前 %v）：客户端会渲染成空白卡并按无效物品过滤", itemID, attrs["ljnnpokapig"])
		}
		rigidity, _ := attrs["lgjeahdobch"].(float64)
		if rigidity <= 0 {
			t.Errorf("%s 缺少刚性 lgjeahdobch（当前 %v）", itemID, attrs["lgjeahdobch"])
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历定义: %v", err)
	}
	if total == 0 {
		t.Fatal("静态库里没有任何 category=57 的铅芯线定义")
	}
}

// TestLeadcoreUsesWeightQuantityClass 锁定铅芯线（category=57）的实例类。
//
// 客户端把铅芯线当成独立物品类 emfindpkead（wire 类 fmaajjlcdmc / type 167，
// f0 为强度位），不是钓线类 ofjgdndfipm / 140。映射缺失时回退到钓线类，
// 客户端在背包/选择框里只能画出空白卡片。
func TestLeadcoreUsesWeightQuantityClass(t *testing.T) {
	entry, ok := categoryWireClass[57]
	if !ok {
		t.Fatal("categoryWireClass 缺少 57（铅芯线）")
	}
	if entry.Class != "fmaajjlcdmc" || entry.TypeID != 167 {
		t.Fatalf("铅芯线映射 = %s/%d，期望 fmaajjlcdmc/167", entry.Class, entry.TypeID)
	}
	if got := wiregen.ClassTypeID["fmaajjlcdmc"]; got != 167 {
		t.Fatalf("fmaajjlcdmc type_id = %d，期望 167", got)
	}
	if idx := fieldIdxByCS("fmaajjlcdmc", csStrength); idx != 0 {
		t.Fatalf("fmaajjlcdmc 强度字段下标 = %d，期望 0", idx)
	}
}

// TestReclassifyLeadcoreFromFallback 验证存量铅芯线会在启动修复时从钓线类改写成 167。
func TestReclassifyLeadcoreFromFallback(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()

	def, err := db.GetItemDef(ctx, 8749) // lc_40246 Leadcore Elastic Brown Camo 80LB
	if err != nil || def == nil {
		t.Skip("静态库缺 lc_40246 定义: ", err)
	}
	if def.Category != 57 {
		t.Fatalf("lc_40246 品类 = %d，期望 57", def.Category)
	}

	guid := "00112233445566778899aabbccddeeff"
	obj, err := wiregen.MakeObj(fallbackWireClass, nil)
	if err != nil {
		t.Fatal(err)
	}
	fields := obj.Fields
	fields[fieldIdxByCS(fallbackWireClass, csGUID)].Hex = guid
	fields[fieldIdxByCS(fallbackWireClass, csNumericID)].Num = ptrU64(uint64(def.NumericID))
	fj, err := FieldsToJSON(fields)
	if err != nil {
		t.Fatal(err)
	}
	legacy := &store.Item{
		GUID: guid, UID: 1000004, ItemID: "lc_40246", NumericID: def.NumericID,
		WireClass: fallbackWireClass, TypeID: fallbackTypeID,
		Container: "backpack", Qty: 1, FieldsJSON: fj, Durability: 1, Workmanship: 1,
	}
	if err := db.InsertItem(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	row, err := db.GetItemByGUID(ctx, guid)
	if err != nil || row == nil {
		t.Fatal("读取探测物品失败: ", err)
	}

	changed, err := srv.reclassifyStoredItem(ctx, row, def)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("铅芯线未被改写")
	}
	fresh, err := db.GetItemByGUID(ctx, guid)
	if err != nil || fresh == nil {
		t.Fatal("改写后读取失败: ", err)
	}
	if fresh.WireClass != "fmaajjlcdmc" || fresh.TypeID != 167 {
		t.Fatalf("改写后 = %s/%d，期望 fmaajjlcdmc/167", fresh.WireClass, fresh.TypeID)
	}
	freshFields, err := FieldsFromJSON(fresh.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(freshFields) != len(wiregen.ClassOps["fmaajjlcdmc"]) {
		t.Fatalf("字段数 = %d，期望 %d", len(freshFields), len(wiregen.ClassOps["fmaajjlcdmc"]))
	}
	if freshFields[0].Num == nil || *freshFields[0].Num == 0 {
		t.Error("改写后强度字段为空")
	}
	if freshFields[1].Hex != guid {
		t.Errorf("guid 未保留: %s", freshFields[1].Hex)
	}
	if freshFields[4].Num == nil || int64(*freshFields[4].Num) != def.NumericID {
		t.Error("numeric id 未保留")
	}
}

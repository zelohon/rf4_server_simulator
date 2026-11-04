package gameserver

import (
	"database/sql"
	"encoding/json"
	"testing"

	_ "modernc.org/sqlite"
)

// TestLeadcoreDefinitionsCarryLengthAndRigidity 守住 category=57（铅芯线）的静态数据契约。
//
// 客户端把铅芯线实例按“钓线”渲染（wire 类 ohciikgjghe / type 140），实例长度来自
// item_def.attrs_json 的 ljnnpokapig。上游静态库这三项为空时，实例长度落 0，
// 客户端会把铅芯线当成 0 米的废线过滤掉——表现就是钓组“铅芯线”槽位只有空白卡，
// 无法装配，进而鱼饵/安装全部不可用。
//
// 2026-11-02 曾手工给 83 条定义补过长度=10、刚性=0.4+LB/150；11-03 整包替换
// static.sqlite3 时补丁被覆盖，问题回归。此用例在每次测试时校验仓库自带的静态库，
// 防止同样的覆盖再次发生。
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

package gameserver

import (
	"context"
	"testing"

	"rf4server/internal/proto"
)

// 船票租期只能取票定义里的 gloegohmand：运行参数 boat_rent_minutes 一旦覆盖，
// 时效对象的 f3 就与物品定义不一致，客户端 BoatWatchDog 判定激活失败并以约
// 16ms 间隔重发 3/7（买完票按 E 上船黑屏）。
func TestStartTicketRentIgnoresRentMinutesTunable(t *testing.T) {
	srv, sess, ctx := newMiscProbe(t)
	if err := srv.DB.TunableSet(context.Background(), "boat_rent_minutes", "14400", "test"); err != nil {
		t.Fatal(err)
	}
	mctx := miscCtx(sess, 3, 7, nil)
	it, err := srv.CreateItem(ctx, 1000001, "ticket_boat_komar_24", "backpack", 1)
	if err != nil || it == nil {
		t.Fatalf("创建船票失败: %v", err)
	}
	def, err := srv.DB.GetItemDef(ctx, it.NumericID)
	if err != nil || def == nil {
		t.Fatalf("读取船票定义失败: %v", err)
	}
	want, ok := defAttrU64(def, boatTicketDurationAttr)
	if !ok || want == 0 {
		t.Fatal("船票定义缺少租期属性")
	}
	if !srv.startTicketRent(mctx, it, def) {
		t.Fatal("3/7 租用状态未写入")
	}
	row, err := srv.DB.GetItemByGUID(ctx, it.GUID)
	if err != nil || row == nil {
		t.Fatalf("回读船票失败: %v", err)
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	ri := fieldIdxByCS(row.WireClass, "bkmnbppciao")
	if ri < 0 || ri >= len(fields) || fields[ri].Val == nil || fields[ri].Val.Kind != proto.KindObj || len(fields[ri].Val.Fields) < 4 {
		t.Fatalf("船票缺少时效对象: %#v", fields)
	}
	if got := u64Of(fields[ri].Val.Fields[3].Num); got != want {
		t.Fatalf("时效对象时长=%d，期望使用定义值 %d（boat_rent_minutes 不应生效）", got, want)
	}
}

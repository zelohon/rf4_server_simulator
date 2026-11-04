package gameserver

import (
	"context"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

func mountGearForTest(t *testing.T, srv *Server, sess *Session, rodGUID string, itemGUID string) *rigSetResult {
	t.Helper()
	ctx := context.Background()
	row, err := srv.DB.GetItemByGUID(ctx, itemGUID)
	if err != nil || row == nil {
		t.Fatalf("读取待装配物品失败: %v", err)
	}
	cat, _ := srv.itemCategoryOf(ctx, row)
	res := srv.rigSetComponent(ctx, row.UID, rodGUID, cat, itemGUID, 0,
		rigSetOpts{ParentType: familyRod, EnforceCategory: true, SplitStack: true})
	if res == nil || res.Rejected || res.MountedGUID == "" {
		t.Fatalf("装配 %s 失败: %+v", row.ItemID, res)
	}
	return res
}

func rigSlotGUIDs(t *testing.T, srv *Server, rodGUID string) map[int64]string {
	t.Helper()
	rows, err := srv.DB.ListRigComponents(context.Background(), rodGUID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[int64]string{}
	for _, row := range rows {
		out[row.Slot] = row.GUID
	}
	return out
}

func lineLengthOfTestItem(t *testing.T, srv *Server, guid string) int64 {
	t.Helper()
	row, err := srv.DB.GetItemByGUID(context.Background(), guid)
	if err != nil || row == nil {
		t.Fatalf("读取鱼线失败: %v", err)
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	return itemLengthField(row, fields)
}

func TestWeakestLeaderBreaksAndKeepsMainLine(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "tele_10180_4", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_15095", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	leader, err := srv.CreateItem(ctx, uid, "fc_leader_40048", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	hook, err := srv.CreateItem(ctx, uid, "clas_20163", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	bait, err := srv.CreateItem(ctx, uid, "worm_01", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	bobber, err := srv.CreateItem(ctx, uid, "bob_25001", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	for _, item := range []string{line.GUID, leader.GUID, hook.GUID, bait.GUID, bobber.GUID} {
		mountGearForTest(t, srv, sess, rod.GUID, item)
	}
	comps := rigSlotGUIDs(t, srv, rod.GUID)
	lineGUID := comps[catLine]
	if lineGUID != line.GUID {
		t.Fatalf("主线 GUID=%s，期望 %s", lineGUID, line.GUID)
	}
	beforeLength := lineLengthOfTestItem(t, srv, lineGUID)
	if beforeLength <= 1 {
		t.Fatalf("测试鱼线长度异常: %d", beforeLength)
	}
	beforeLine, _ := db.GetItemByGUID(ctx, lineGUID)

	tree := rigRequestTree(t, proto.Guid(rod.GUID), proto.Guid(lineGUID), proto.U32(2), proto.Null())
	body, ok := hDynamicRodBreak(&Ctx{Sess: sess, Main: 17, Sub: 9, Tree: tree,
		DecodeOK: true, ExpectResponse: true, Log: logging.Discard()})
	if !ok || len(body) == 0 {
		t.Fatal("17/9 断线未处理")
	}
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	if u64Of(dec.Fields[0].Num) == 0 {
		t.Fatal("断线响应 f0 应为 true")
	}
	if got := dec.Fields[1].GuidHex(); got != leader.GUID {
		t.Fatalf("断子线响应 f1=%s，期望子线 %s", got, leader.GUID)
	}

	afterLine, err := db.GetItemByGUID(ctx, lineGUID)
	if err != nil || afterLine == nil {
		t.Fatalf("断线后主线应保留: %v", err)
	}
	if afterLine.Durability != beforeLine.Durability {
		t.Fatalf("断线不应扣耐久: %v -> %v", beforeLine.Durability, afterLine.Durability)
	}
	afterLength := lineLengthOfTestItem(t, srv, lineGUID)
	if afterLength != beforeLength {
		t.Fatalf("断子线不应扣主线长度: 原=%d 现在=%d", beforeLength, afterLength)
	}
	if row, _ := db.GetItemByGUID(ctx, leader.GUID); row != nil {
		t.Fatal("断子线后子线实例应被删除")
	}
	if row, _ := db.GetItemByGUID(ctx, bobber.GUID); row == nil {
		t.Fatal("断子线后上方浮漂应保留")
	}

	for _, slot := range []int64{catHook, catLead, catWireLeader, catBait} {
		if guid := comps[slot]; guid != "" {
			if row, _ := db.GetItemByGUID(ctx, guid); row != nil {
				t.Fatalf("断线后槽位 %d 的物品 %s 应被删除，实际仍存在", slot, guid)
			}
		}
	}
	if got := rigSlotGUIDs(t, srv, rod.GUID); got[catLine] != lineGUID {
		t.Fatal("断线后竿 f3/rig_component 应仍指向主线")
	}
	if _, ok := rigCompsOf(rigStoredF5(t, mustItem(t, db, rod.GUID)))[catHook]; ok {
		t.Fatal("断线后竿 f5 不应残留鱼钩")
	}

	logs, err := db.ListItemDamageLogs(ctx, uid, 100)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, entry := range logs {
		counts[entry.Event]++
	}
	if counts[damageEventLineBreak] != 1 {
		t.Fatalf("line_break 记录=%d，期望 1；全部=%v", counts[damageEventLineBreak], counts)
	}
	if counts[damageEventLostOnLineBreak] < 3 {
		t.Fatalf("lost_on_line_break 记录=%d，期望至少 3；全部=%v", counts[damageEventLostOnLineBreak], counts)
	}
	var lineBreaks int64
	if err := db.SQL.QueryRow(`SELECT value FROM player_counters WHERE uid=? AND key='line_breaks'`, uid).Scan(&lineBreaks); err != nil || lineBreaks != 1 {
		t.Fatalf("line_breaks 计数=%d err=%v，期望 1", lineBreaks, err)
	}
}

func TestMainLineBreakLosesExactlyFiveMeters(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "tele_10180_4", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_15095", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)
	lineRow, _ := db.GetItemByGUID(ctx, line.GUID)
	fields, _ := FieldsFromJSON(lineRow.FieldsJSON)
	if !setItemLengthField(lineRow, fields, 50) {
		t.Fatal("设置鱼线长度失败")
	}
	fj, _ := FieldsToJSON(fields)
	if err := db.UpdateItemFields(ctx, line.GUID, fj, lineRow.Qty, lineRow.Durability, lineRow.Workmanship, lineRow.RigGUID); err != nil {
		t.Fatal(err)
	}

	// 客户端故意报竿断，服务端仍应按同规格“主线优先于竿”判定为断主线。
	tree := rigRequestTree(t, proto.Guid(rod.GUID), proto.Guid(rod.GUID), proto.U32(1), proto.Null())
	body, ok := hDynamicRodBreak(&Ctx{Sess: sess, Main: 17, Sub: 9, Tree: tree,
		DecodeOK: true, ExpectResponse: true, Log: logging.Discard()})
	if !ok || len(body) == 0 {
		t.Fatal("17/9 主线断裂未处理")
	}
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	if u64Of(dec.Fields[0].Num) == 0 {
		t.Fatal("主线断裂响应 f0 应为 true")
	}
	if got := dec.Fields[1].GuidHex(); got != line.GUID {
		t.Fatalf("应断主线 %s，实得 %s", line.GUID, got)
	}
	if got := lineLengthOfTestItem(t, srv, line.GUID); got != 45 {
		t.Fatalf("断主线后长度=%d，期望固定扣 5m 后为 45", got)
	}
}

func TestBrokenRodPersistsZeroDurabilityAndCannotMount(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	rod, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	tree := rigRequestTree(t, proto.Guid(rod.GUID), proto.Guid(rod.GUID), proto.U32(1), proto.Null())
	body, ok := hDynamicRodBreak(&Ctx{Sess: sess, Main: 17, Sub: 9, Tree: tree,
		DecodeOK: true, ExpectResponse: true, Log: logging.Discard()})
	if !ok || len(body) == 0 {
		t.Fatal("17/9 断竿未处理")
	}
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	if u64Of(dec.Fields[0].Num) != 0 {
		t.Fatal("断竿响应 f0 应为 false")
	}
	row, err := db.GetItemByGUID(ctx, rod.GUID)
	if err != nil || row == nil || row.Durability > 0 {
		t.Fatalf("断竿耐久应落库为 0: row=%+v err=%v", row, err)
	}

	hook, err := srv.CreateItem(ctx, uid, "clas_20163", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetItemDurability(ctx, hook.GUID, 0); err != nil {
		t.Fatal(err)
	}
	res := srv.rigSetComponent(ctx, uid, rod.GUID, catHook, hook.GUID, 0,
		rigSetOpts{ParentType: familyRod, EnforceCategory: true})
	if res == nil || !res.Rejected {
		t.Fatalf("耐久 0 的鱼钩应禁止装配: %+v", res)
	}

	line, err := srv.CreateItem(ctx, uid, "mono_40853", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	lineFields, err := FieldsFromJSON(line.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if !setItemLengthField(line, lineFields, 0) {
		t.Fatal("设置鱼线长度失败")
	}
	lineJSON, _ := FieldsToJSON(lineFields)
	if err := db.UpdateItemFields(ctx, line.GUID, lineJSON, line.Qty, line.Durability, line.Workmanship, line.RigGUID); err != nil {
		t.Fatal(err)
	}
	res = srv.rigSetComponent(ctx, uid, rod.GUID, catLine, line.GUID, 0,
		rigSetOpts{ParentType: familyRod, EnforceCategory: true})
	if res == nil || !res.Rejected {
		t.Fatalf("剩余长度为 0 的鱼线应禁止装配: %+v", res)
	}

	takeTree := rigRequestTree(t, proto.Guid(rod.GUID))
	takeBody, ok := hRodTake(&Ctx{Sess: sess, Main: 17, Sub: 7, Tree: takeTree,
		DecodeOK: true, ExpectResponse: true, Log: logging.Discard()})
	if !ok || len(takeBody) == 0 {
		t.Fatal("17/7 取断竿未处理")
	}
	takeDec, _, err := proto.DecodeValue(takeBody, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	if got := takeDec.Fields[0].GuidHex(); got != zeroGUID {
		t.Fatalf("断竿不应被拿到手上，158.f0=%s", got)
	}
	logs, err := db.ListItemDamageLogs(ctx, uid, 20)
	if err != nil || len(logs) == 0 || logs[0].Event != damageEventRodBreak {
		t.Fatalf("缺少 rod_break 记录: logs=%+v err=%v", logs, err)
	}
	var rodBreaks int64
	if err := db.SQL.QueryRow(`SELECT value FROM player_counters WHERE uid=? AND key='rod_breaks'`, uid).Scan(&rodBreaks); err != nil || rodBreaks != 1 {
		t.Fatalf("rod_breaks 计数=%d err=%v，期望 1", rodBreaks, err)
	}
}

func TestWearAddsHistoryAndWearsRodBody(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	rod, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_40853", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)
	srv.wearRigForCatchPersisted(ctx, rod.GUID)

	freshRod, _ := db.GetItemByGUID(ctx, rod.GUID)
	freshLine, _ := db.GetItemByGUID(ctx, line.GUID)
	if freshRod.Durability >= 1 {
		t.Fatalf("竿本体应产生磨损，耐久=%v", freshRod.Durability)
	}
	if freshLine.Durability >= 1 {
		t.Fatalf("鱼线应产生磨损，耐久=%v", freshLine.Durability)
	}
	logs, err := db.ListItemDamageLogs(ctx, uid, 20)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, entry := range logs {
		if entry.Event == damageEventWear {
			got[entry.ItemGUID] = true
		}
	}
	if !got[rod.GUID] || !got[line.GUID] {
		t.Fatalf("磨损历史缺少竿或线: %+v", got)
	}
}

func TestLineBreakLossBounds(t *testing.T) {
	if got := lineBreakLoss(100, 30); got != 30 {
		t.Fatalf("客户端提供 30m 时应扣 30m，实得 %d", got)
	}
	if got := lineBreakLoss(100, 200); got != 100 {
		t.Fatalf("请求长度不得超过剩余长度，实得 %d", got)
	}
	for i := 0; i < 200; i++ {
		got := lineBreakLoss(100, 0)
		if got < 1 || got > 80 {
			t.Fatalf("默认随机扣减=%d，应在 1..80m", got)
		}
	}
}

func TestLineBreakExhaustsAndDeletesLine(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	rod, err := srv.CreateItem(ctx, uid, "tele_10180_4", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_15095", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)
	lineRow, _ := db.GetItemByGUID(ctx, line.GUID)
	fields, _ := FieldsFromJSON(lineRow.FieldsJSON)
	if !setItemLengthField(lineRow, fields, 1) {
		t.Fatal("设置鱼线长度失败")
	}
	fj, _ := FieldsToJSON(fields)
	if err := db.UpdateItemFields(ctx, line.GUID, fj, lineRow.Qty, lineRow.Durability, lineRow.Workmanship, lineRow.RigGUID); err != nil {
		t.Fatal(err)
	}

	tree := rigRequestTree(t, proto.Guid(rod.GUID), proto.Guid(line.GUID), proto.U32(2), proto.Null())
	if _, ok := hDynamicRodBreak(&Ctx{Sess: sess, Main: 17, Sub: 9, Tree: tree,
		DecodeOK: true, ExpectResponse: true, Log: logging.Discard()}); !ok {
		t.Fatal("17/9 断线未处理")
	}
	if row, _ := db.GetItemByGUID(ctx, line.GUID); row != nil {
		t.Fatal("鱼线长度耗尽后应删除物品")
	}
	freshRod, _ := db.GetItemByGUID(ctx, rod.GUID)
	rodFields, _ := FieldsFromJSON(freshRod.FieldsJSON)
	if idx := fieldIdxByCS(freshRod.WireClass, csLine); idx >= 0 && rodFields[idx].GuidHex() != "" {
		t.Fatal("鱼线删除后竿 f3 应清空")
	}
	logs, _ := db.ListItemDamageLogs(ctx, uid, 20)
	seen := map[string]bool{}
	for _, entry := range logs {
		seen[entry.Event] = true
	}
	if !seen[damageEventLineBreak] || !seen[damageEventLineExhausted] {
		t.Fatalf("长度耗尽应记录 line_break 和 line_exhausted: %+v", seen)
	}
}

func TestBrokenHandRodDeletesFloat(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "tele_10180_4", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_15065", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	bobber, err := srv.CreateItem(ctx, uid, "bob_25001", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, bobber.GUID)
	comps := rigSlotGUIDs(t, srv, rod.GUID)
	if comps[catFloat] != bobber.GUID {
		t.Fatalf("手竿浮漂未装配: slot28=%s，期望 %s", comps[catFloat], bobber.GUID)
	}

	tree := rigRequestTree(t, proto.Guid(rod.GUID), proto.Guid(line.GUID), proto.U32(2), proto.Null())
	if _, ok := hDynamicRodBreak(&Ctx{Sess: sess, Main: 17, Sub: 9, Tree: tree,
		DecodeOK: true, ExpectResponse: true, Log: logging.Discard()}); !ok {
		t.Fatal("17/9 手竿断线未处理")
	}
	if row, _ := db.GetItemByGUID(ctx, bobber.GUID); row != nil {
		t.Fatal("手竿断线后浮漂应被删除")
	}
	if got := rigSlotGUIDs(t, srv, rod.GUID); got[catFloat] != "" {
		t.Fatalf("手竿断线后 rig_component 仍残留浮漂: %s", got[catFloat])
	}
	freshRod := mustItem(t, db, rod.GUID)
	if _, ok := rigCompsOf(rigStoredF5(t, freshRod))[catFloat]; ok {
		t.Fatal("手竿断线后竿 f5 仍残留浮漂")
	}
}

// 钩/拟饵等终端件断裂：组件耐久归零并离竿，竿本体与承力线不受影响
// （回归：终端件断件上报此前会被物理裁决吞掉——有强度数据时误判为断线并
// 连带丢失钩/饵/漂，无强度数据时兜底成断竿归零竿耐久）。
func TestHookBreakDetachesComponentKeepsRod(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "tele_10012_tl16", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_40853", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	hook, err := srv.CreateItem(ctx, uid, "clas_20163", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)
	res := srv.rigSetComponent(ctx, uid, rod.GUID, catHook, hook.GUID, 0,
		rigSetOpts{ParentType: familyRod, EnforceCategory: true})
	if res == nil || res.Rejected {
		t.Fatalf("鱼钩装配被拒: %+v", res)
	}
	if got := rigSlotGUIDs(t, srv, rod.GUID); got[catHook] != hook.GUID {
		t.Fatalf("鱼钩未装配: slot12=%s 期望 %s", got[catHook], hook.GUID)
	}

	tree := rigRequestTree(t, proto.Guid(rod.GUID), proto.Guid(hook.GUID), proto.U32(2), proto.Null())
	body, ok := hDynamicRodBreak(&Ctx{Sess: sess, Main: 17, Sub: 9, Tree: tree,
		DecodeOK: true, ExpectResponse: true, Log: logging.Discard()})
	if !ok || len(body) == 0 {
		t.Fatal("17/9 钩断未处理")
	}
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	if u64Of(dec.Fields[0].Num) != 0 {
		t.Fatal("钩断响应 f0 应为 false（非断线）")
	}
	if got := dec.Fields[1].GuidHex(); got != hook.GUID {
		t.Fatalf("钩断响应 f1=%s，期望 %s", got, hook.GUID)
	}
	if got := rigSlotGUIDs(t, srv, rod.GUID); got[catHook] != "" {
		t.Fatalf("钩断后 rig_component 仍残留鱼钩: slot12=%s", got[catHook])
	}
	freshRod := mustItem(t, db, rod.GUID)
	if _, ok := rigCompsOf(rigStoredF5(t, freshRod))[catHook]; ok {
		t.Fatal("钩断后竿 f5 仍残留鱼钩")
	}
	if freshRod.Durability <= 0 {
		t.Fatalf("钩断不应归零竿耐久: %v", freshRod.Durability)
	}
	freshHook := mustItem(t, db, hook.GUID)
	if freshHook.Durability != 0 {
		t.Fatalf("断钩耐久应归零: %v", freshHook.Durability)
	}
	if freshHook.Container != "backpack" || freshHook.RigGUID != "" || freshHook.Slot != 0 {
		t.Fatalf("损坏组件应退回背包: container=%q rig_guid=%q slot=%d", freshHook.Container, freshHook.RigGUID, freshHook.Slot)
	}
	if got := rigStoredGUID(t, freshHook, csParent); got != "" {
		t.Fatalf("损坏组件快照仍保留父件: %s", got)
	}
	// 等价重登后重建容器：按背包容器查询仍能读到该组件。
	bagRows, err := srv.DB.ListItemsByContainer(ctx, uid, "backpack")
	if err != nil {
		t.Fatal(err)
	}
	backInBag := false
	for _, row := range bagRows {
		if row.GUID == hook.GUID {
			backInBag = true
			break
		}
	}
	if !backInBag {
		t.Fatal("重建背包容器后应能查询到损坏组件")
	}
	if mustItem(t, db, line.GUID) == nil {
		t.Fatal("钩断不应影响鱼线")
	}
	logs, err := db.ListItemDamageLogs(ctx, uid, 20)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, entry := range logs {
		seen[entry.Event] = true
	}
	if !seen[damageEventComponentBreak] {
		t.Fatalf("应记录 component_break: %+v", seen)
	}
	if seen[damageEventRodBreak] || seen[damageEventLineBreak] {
		t.Fatalf("钩断不应记录 rod_break/line_break: %+v", seen)
	}
}

func TestTerminalComponentProjectionOnlyBreakCleansProjection(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "tele_10012_tl16", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	hook, err := srv.CreateItem(ctx, uid, "clas_20163", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	// 模拟历史/部分同步状态：rig_component 有投影，但竿 f5 尚未包含该组件。
	if err := db.SetItemRigLink(ctx, hook.GUID, rod.GUID, catHook); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertRigComponentAt(ctx, rod.GUID, catHook, 0, hook.GUID, 0); err != nil {
		t.Fatal(err)
	}

	outcome, err := srv.breakLineOrRod(ctx, uid, rod.GUID, hook.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome == nil || !outcome.ComponentBroken {
		t.Fatalf("应裁决为终端件断裂: %+v", outcome)
	}
	if got := rigSlotGUIDs(t, srv, rod.GUID)[catHook]; got != "" {
		t.Fatalf("仅投影存在的组件断裂后仍残留 rig_component: %s", got)
	}
	freshHook := mustItem(t, db, hook.GUID)
	if freshHook.Container != "backpack" || freshHook.RigGUID != "" || freshHook.Slot != 0 {
		t.Fatalf("损坏组件应退回背包: container=%q rig_guid=%q slot=%d", freshHook.Container, freshHook.RigGUID, freshHook.Slot)
	}
	if got := rigStoredGUID(t, freshHook, csParent); got != "" {
		t.Fatalf("损坏组件快照仍保留父件: %s", got)
	}
}

func TestInvalidBreakReportPreservesCatchAndEquipment(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	sess := newProbeSess(srv)
	uid := sess.Userid
	create := func(owner int64, itemID string) string {
		t.Helper()
		row, err := srv.CreateItem(ctx, owner, itemID, "backpack", 1)
		if err != nil {
			t.Fatal(err)
		}
		return row.GUID
	}
	rod := create(uid, "feeder_10125_FD360")
	line := create(uid, "mono_40853")
	mountGearForTest(t, srv, sess, rod, line)
	bell := mountGearForTest(t, srv, sess, rod, create(uid, "feeder_bell")).MountedGUID
	otherRod := create(uid, "feeder_10125_FD360")
	foreignRod := create(uid+1, "feeder_10125_FD360")
	unmounted := create(uid, "mono_40853")
	foreignHook := create(uid+1, "clas_20163")
	otherLine := create(uid, "mono_40853")
	mountGearForTest(t, srv, sess, otherRod, otherLine)
	wrongSlot := create(uid, "clas_20163")
	if err := db.UpsertRigComponentAt(ctx, rod, catFloat, 0, wrongSlot, 0); err != nil {
		t.Fatal(err)
	}

	st := srv.stateFor(uid)
	pending := &RolledFish{Species: "perch"}
	st.mu.Lock()
	st.GearGuid, st.Pending, st.CastActive, st.Hooked = rod, pending, true, true
	st.mu.Unlock()
	length := lineLengthOfTestItem(t, srv, line)
	before := mustItem(t, db, rod).Durability

	for _, tc := range []struct {
		name, target, part string
	}{
		{"unknown", rod, "ffffffffffffffffffffffffffffffff"},
		{"foreign component", rod, foreignHook},
		{"foreign rod", foreignRod, foreignRod},
		{"unmounted line", rod, unmounted},
		{"another rig's line", rod, otherLine},
		{"unsupported mounted item", rod, bell},
		{"wrong projection slot", rod, wrongSlot},
		{"nonrod target", line, line},
		{"undecoded request", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := &Ctx{Sess: sess, Main: 17, Sub: 9, ExpectResponse: true, Log: logging.Discard()}
			if tc.target != "" {
				request.Tree = rigRequestTree(t, proto.Guid(tc.target), proto.Guid(tc.part), proto.U32(1), proto.Null())
				request.DecodeOK = true
			}
			result := srv.dispatch.executeRequest(request, true)
			if !result.hasResponse || !result.nativeFailure {
				t.Fatalf("无效断件没有返回协议错误: %+v", result)
			}
			st.mu.Lock()
			unchanged := st.Pending == pending && st.CastActive && st.Hooked && !st.Resolved && st.GearGuid == rod
			st.mu.Unlock()
			if !unchanged || mustItem(t, db, rod).Durability != before || lineLengthOfTestItem(t, srv, line) != length {
				t.Fatal("无效断件改变了钓获或装备")
			}
			logs, err := db.ListItemDamageLogs(ctx, uid, 20)
			if err != nil || len(logs) != 0 {
				t.Fatalf("无效断件写入了损坏记录: %+v %v", logs, err)
			}
		})
	}
}

func TestBreakReportAcceptsMountedLoadComponents(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	sess := newProbeSess(srv)
	uid := sess.Userid
	create := func(id string) string {
		t.Helper()
		row, err := srv.CreateItem(ctx, uid, id, "backpack", 1)
		if err != nil {
			t.Fatal(err)
		}
		return row.GUID
	}
	rod := create("feeder_10125_FD360")
	for _, id := range []string{"mono_40853", "spin_5820", "fc_leader_40048"} {
		item := mountGearForTest(t, srv, sess, rod, create(id)).MountedGUID
		if report, err := srv.validateBreakReport(ctx, uid, rod, item); err != nil || report.terminalCategory != 0 || report.embedded {
			t.Fatalf("已装配的承力件 %s 没有交由承力规则: %+v %v", id, report, err)
		}
	}

	factoryRod := create("spin_10158_M88HH")
	factoryGUID := func(guid string) string {
		t.Helper()
		tree, err := srv.ItemToTree(ctx, mustItem(t, db, guid))
		if err != nil {
			t.Fatal(err)
		}
		child := tree.Field(0).Val
		if child == nil {
			t.Fatal("缺少出厂竿件")
		}
		return child.Field(fieldIdxByCS(child.Class, csGUID)).Hex
	}
	if _, err := srv.breakLineOrRod(ctx, uid, factoryRod, factoryGUID(create("spin_10158_M88HH"))); err == nil {
		t.Fatal("错误接收了另一把竿的出厂部件")
	}
	part := factoryGUID(factoryRod)
	st := srv.stateFor(uid)
	st.mu.Lock()
	st.GearGuid = factoryRod
	st.mu.Unlock()
	request := &Ctx{Sess: sess, Main: 17, Sub: 9, DecodeOK: true, ExpectResponse: true, Log: logging.Discard(),
		Tree: rigRequestTree(t, proto.Guid(zeroGUID), proto.Guid(part), proto.U32(1), proto.Null())}
	result := srv.dispatch.executeRequest(request, true)
	if !result.hasResponse || result.nativeFailure || result.failed {
		t.Fatalf("该竿的出厂部件未走原有断竿响应: %+v", result)
	}
	decoded, _, err := proto.DecodeValue(result.body, 0, srv.Table())
	if err != nil || decoded.TypeID != 11028 || decoded.Field(1).Hex != part {
		t.Fatalf("断竿响应应指明经校验的出厂部件: %v %v", decoded, err)
	}
	if mustItem(t, db, factoryRod).Durability != 0 {
		t.Fatal("已裁决断裂的母竿没有落库")
	}
	logs, err := db.ListItemDamageLogs(ctx, uid, 10)
	if err != nil || len(logs) != 1 || logs[0].ItemGUID != factoryRod || logs[0].BrokenGUID != part {
		t.Fatalf("出厂部件与母竿的损坏记录不一致: %+v %v", logs, err)
	}
}

func TestTerminalComponentBreakRechecksAttachment(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	sess := newProbeSess(srv)
	rod, err := srv.CreateItem(ctx, sess.Userid, "tele_10012_tl16", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	hook, err := srv.CreateItem(ctx, sess.Userid, "clas_20163", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.breakComponentTx(ctx, sess.Userid, rod.GUID, hook.GUID, catHook); err == nil {
		t.Fatal("事务接受了已卸下的组件")
	}
	if mustItem(t, db, hook.GUID).Durability != hook.Durability {
		t.Fatal("事务损坏了未装配的组件")
	}
}

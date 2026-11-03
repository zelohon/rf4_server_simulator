package gameserver

import (
	"context"
	"fmt"

	"rf4server/internal/proto"
)

// itemPhysicsRepairKey 记录最近一次物理字段修复的版本与统计。
// 每次启动仍会重新扫描，以修复后续导入或回档产生的异常数据。
const itemPhysicsRepairKey = "repair:item_physics:v5"
const itemPhysicsRepairVersion = "7"

// RepairItemPhysics 修复已落库物品的物理字段快照：
//   - 把误落成钓线类的工坊工具和材料改写成自己的实例类；
//   - 重新物化缺失的渔轮子件；
//   - 补全线、钩、竿、轮的顶层强度；
//   - 回填零值状态位和容器枚举；
//   - 修正渔轮刹车片曳力与成色；
//   - 将竿的 f5 类型限制在竿型允许范围内。
//
// 兼容性只用于装配推荐，不覆盖玩家显式选择。修复写回 fields_json，
// 并通过 ensureRig 同步 rig/rig_component。成功后写版本和统计标记；
// 失败时保留上一次成功标记。
func (s *Server) RepairItemPhysics(ctx context.Context) error {
	if s.DB == nil {
		return fmt.Errorf("物品物理状态修复：数据库未连接")
	}

	rows, err := s.DB.SQL.QueryContext(ctx, `SELECT guid FROM item ORDER BY guid`)
	if err != nil {
		return fmt.Errorf("物品物理状态修复：列出物品: %w", err)
	}
	var guids []string
	for rows.Next() {
		var guid string
		if err := rows.Scan(&guid); err != nil {
			_ = rows.Close()
			return fmt.Errorf("物品物理状态修复：读取 guid: %w", err)
		}
		guids = append(guids, guid)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("物品物理状态修复：关闭查询: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("物品物理状态修复：遍历物品: %w", err)
	}

	changed := 0
	rigsRepaired := 0
	for _, guid := range guids {
		it, err := s.DB.GetItemByGUID(ctx, guid)
		if err != nil {
			return fmt.Errorf("物品物理状态修复：读取 %s: %w", guid, err)
		}
		if it == nil {
			continue
		}
		if def, derr := s.DB.GetItemDef(ctx, it.NumericID); derr == nil && def != nil {
			rewritten, rerr := s.reclassifyStoredItem(ctx, it, def)
			if rerr != nil {
				return fmt.Errorf("物品物理状态修复：改写类别 %s: %w", guid, rerr)
			}
			if rewritten {
				changed++
				continue
			}
		}
		fields, err := FieldsFromJSON(it.FieldsJSON)
		if err != nil {
			return fmt.Errorf("物品物理状态修复：解析 %s: %w", guid, err)
		}
		fields = alignFieldsToOps(it, fields)
		before, _ := FieldsToJSON(fields)
		obj := proto.Obj(it.WireClass, uint32(it.TypeID), fields...)
		rowChanged := false
		var rodCreated proto.Field
		hasRodCreated := false
		// 与 ItemToTree 的字段修复保持一致：状态零值和缺失子件必须写回数据库。
		if def, derr := s.DB.GetItemDef(ctx, it.NumericID); derr == nil && def != nil {
			stateFlagsZeroFill(it.WireClass, obj, def)
			s.materializeInto(ctx, it.WireClass, def, obj, it.GUID, it.Workmanship, it.Durability)
		}
		// 竿实例的厂件和 f5 也必须持久化，不能只在渲染时重建。
		if it.WireClass == "cnolibojkao" {
			if idx := fieldIdxByCS(it.WireClass, csCreated); idx >= 0 && idx < len(fields) {
				rodCreated = fields[idx]
				hasRodCreated = true
			}
			if err := s.rebuildRod(ctx, it, obj); err != nil {
				return fmt.Errorf("物品物理状态修复：重建鱼竿 %s: %w", guid, err)
			}
			fields = obj.Fields
			// f16 是每次下发都会刷新的动态时间戳，不参与持久化变化判断；
			// f0、f5 等字段仍通过后续 JSON 比较检测。
			if hasRodCreated {
				if idx := fieldIdxByCS(it.WireClass, csCreated); idx >= 0 && idx < len(fields) {
					fields[idx] = rodCreated
				}
			}
		}
		if s.canonicalizeItemPhysicalFields(ctx, it, fields) {
			rowChanged = true
		}
		if after, aerr := FieldsToJSON(fields); aerr == nil && after != before {
			rowChanged = true
		}
		// 计件物品以 fields_json 的 ljnnpokapig 为准，修正 item.qty。
		if def, derr := s.DB.GetItemDef(ctx, it.NumericID); derr == nil && def != nil && countableCategories[def.Category] {
			if qi := fieldIdxByCS(it.WireClass, csQty); qi >= 0 && qi < len(fields) {
				if q := int64(u64Of(fields[qi].Num)); q > 0 && q != it.Qty {
					it.Qty = q
					rowChanged = true
				}
			}
		}
		if rowChanged {
			bumpCreatedTicks(fields, it.WireClass)
			fj, err := FieldsToJSON(fields)
			if err != nil {
				return fmt.Errorf("物品物理状态修复：编码 %s: %w", guid, err)
			}
			if err := s.DB.UpdateItemFields(ctx, it.GUID, fj, it.Qty, it.Durability, it.Workmanship, it.RigGUID); err != nil {
				return fmt.Errorf("物品物理状态修复：写回 %s: %w", guid, err)
			}
			changed++
		}
		if it.WireClass == "cnolibojkao" {
			if s.ensureRig(ctx, it.UID, it.GUID) {
				rigsRepaired++
			}
		}
	}

	marker := fmt.Sprintf("done;version=%s;scanned=%d;changed=%d;rods=%d",
		itemPhysicsRepairVersion, len(guids), changed, rigsRepaired)
	if err := s.DB.KVSet(ctx, itemPhysicsRepairKey, marker); err != nil {
		return fmt.Errorf("物品物理状态修复：写完成标记: %w", err)
	}
	s.log.Info("物品物理状态修复完成", "version", itemPhysicsRepairVersion,
		"items_changed", changed, "rods_repaired", rigsRepaired, "total", len(guids))
	return nil
}

package gameserver

import (
	"context"
	"fmt"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

// RepairReelComponents 修复渔线轮零件槽，但保留工坊已选择的同品类改装件。
//
// 历史数据里的子零件可能是旧型号/别的轮子留下的（定义与实际零件对不上），
// 而 materializeInto 只补空槽不会覆盖，导致客户端按零件定义算兼容型号时全部算错。
// 这里以 item_def.attrs_json 为准，逐槽用 buildPartInstance 重建并回写。
func (s *Server) RepairReelComponents(ctx context.Context) (int, error) {
	if s.DB == nil {
		return 0, fmt.Errorf("数据库未连接")
	}
	rows, err := s.DB.SQL.QueryContext(ctx,
		`SELECT guid FROM item WHERE wire_class=?`, "dgjinoebdop")
	if err != nil {
		return 0, err
	}
	var guids []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			rows.Close()
			return 0, err
		}
		guids = append(guids, g)
	}
	rows.Close()

	fixed := 0
	for _, g := range guids {
		it, gerr := s.DB.GetItemByGUID(ctx, g)
		if gerr != nil || it == nil {
			continue
		}
		def, derr := s.DB.GetItemDef(ctx, it.NumericID)
		if derr != nil || def == nil {
			continue
		}
		attrs := defAttrs(def)
		if attrs == nil {
			continue
		}
		fields, ferr := FieldsFromJSON(it.FieldsJSON)
		if ferr != nil {
			continue
		}
		changed := false
		for key, raw := range attrs {
			cfg, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			subID, _ := cfg["nhjofmmmngl"].(string)
			if subID == "" {
				continue
			}
			idx := fieldIdxByCS(it.WireClass, key)
			if idx < 0 || idx >= len(fields) || fields[idx].Op != "VAL" {
				continue
			}
			// 只重建已有零件槽：缺槽新增会用错 wire 类（f14 引擎槽就曾因此
			// 让客户端解码抛 InvalidCastException），缺的槽留给后续单独适配。
			if fields[idx].Val == nil || fields[idx].Val.Kind != proto.KindObj {
				continue
			}
			old := fields[idx].Val
			expectedCat := int64(0)
			switch n := cfg["mgkfaekpnoj"].(type) {
			case float64:
				expectedCat = int64(n)
			case int64:
				expectedCat = n
			}
			// 同品类即视为合法工坊改装件，绝不能按母轮出厂配置覆盖。
			// 同时补齐历史版本漏写的摩擦力、挂载位和母轮传动比。
			if selected := s.reelPartDef(ctx, old); selected != nil && selected.Category == expectedCat {
				if s.normalizeReelPart(it, fields, old, selected) {
					changed = true
				}
				continue
			}
			child, cerr := s.buildPartInstance(ctx, it, subID, key)
			if cerr != nil || child == nil {
				continue
			}
			// 保留原零件的 guid（若已有），避免客户端物品集合里出现重复实例。
			if old := fields[idx].Val; old != nil && old.Kind == proto.KindObj {
				if gidx := fieldIdxByCS(old.Class, csGUID); gidx >= 0 && gidx < old.FieldCount() {
					if oldGUID := old.Fields[gidx].Hex; oldGUID != "" {
						if nidx := fieldIdxByCS(child.Class, csGUID); nidx >= 0 && nidx < child.FieldCount() {
							child.Fields[nidx].Hex = oldGUID
						}
					}
				}
				// 保留零件原有磨损，否则每次启动都会把耐久重置为 1.0。
				if didx := fieldIdxByCS(old.Class, csDurability); didx >= 0 && didx < old.FieldCount() && old.Fields[didx].Num != nil {
					if cidx := fieldIdxByCS(child.Class, csDurability); cidx >= 0 && cidx < child.FieldCount() {
						child.Fields[cidx].Num = old.Fields[didx].Num
					}
				}
			}
			fields[idx].Val = child
			changed = true
		}
		if !changed {
			continue
		}
		fj, jerr := FieldsToJSON(fields)
		if jerr != nil {
			continue
		}
		if err := s.DB.UpdateItemFields(ctx, it.GUID, fj, it.Qty, it.Durability, it.Workmanship, it.RigGUID); err != nil {
			continue
		}
		fixed++
	}
	return fixed, nil
}

// normalizeReelPart 回填改装零件会影响的运行时物理字段。
func (s *Server) normalizeReelPart(host *store.Item, hostFields []proto.Field, child *proto.Value, def *store.ItemDef) bool {
	if host == nil || child == nil || def == nil {
		return false
	}
	changed := false
	if idx := fieldIdxByCS(child.Class, csStateEnum); idx >= 0 && idx < child.FieldCount() {
		old := u64Of(child.Fields[idx].Num)
		want := old | subMountFlag
		if want != old {
			child.Fields[idx].Num = ptrU64(want)
			changed = true
		}
	}
	if def.Category == 9 {
		if idx := fieldIdxByCS(child.Class, csDragForce); idx >= 0 && idx < child.FieldCount() {
			want := uint64(proto.F32Bits(float32(itemDefF32(def, gearAttrDragMax) * host.Workmanship)))
			if u64Of(child.Fields[idx].Num) != want {
				child.Fields[idx].Num = ptrU64(want)
				changed = true
			}
		}
	}
	if def.Category == 52 {
		if idx := fieldIdxByCS(host.WireClass, csStrength); idx >= 0 && idx < len(hostFields) {
			want := uint64(proto.F32Bits(float32(itemDefF32(def, gearAttrRatio) * host.Workmanship)))
			if u64Of(hostFields[idx].Num) != want {
				hostFields[idx].Num = ptrU64(want)
				changed = true
			}
		}
	}
	return changed
}

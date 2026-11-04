package gameserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

type baitChange struct {
	Updated []string `json:"updated,omitempty"`
	Removed []string `json:"removed,omitempty"`
}

func (s *Server) canFishWithBait(ctx context.Context, uid int64, gearGUID string) bool {
	if s.DB == nil || gearGUID == "" {
		return true
	}
	rod, _ := s.DB.GetItemByGUID(ctx, gearGUID)
	return rod == nil || rod.UID == uid && len(s.currentBaits(ctx, gearGUID)) > 0
}

// baitUseRule 区分可食饵、重复使用的仿生饵及入水消耗的窝料。
func baitUseRule(def *store.ItemDef, cast bool) (bool, float64) {
	if def == nil {
		return false, 0
	}
	if cast {
		return def.Category == 30 || def.Category == 46, 0
	}
	var attrs struct {
		BaitType int64 `json:"fopbhlmkcpe"`
		LureType int64 `json:"mbemakijdeh"`
	}
	if json.Unmarshal([]byte(def.AttrsJSON), &attrs) != nil {
		return false, 0
	}
	if def.Category == 27 {
		if attrs.BaitType == 17 {
			return false, 0.005
		}
		return true, 0
	}
	if def.Category == 17 {
		switch attrs.LureType {
		case 3, 8, 9, 13, 14, 16:
			return false, 0.02
		}
	}
	return false, 0
}

func (s *Server) biteBaitSnapshot(ctx context.Context, rod string) (string, bool) {
	if s.DB == nil {
		return "", false
	}
	row, err := s.DB.GetItemByGUID(ctx, rod)
	if err != nil || row == nil {
		return "", false
	}
	comps := s.storedRigComponents(ctx, rod)
	var ids []string
	for _, category := range []int64{27, 17} {
		for _, key := range rigCompKeysOfCategory(comps, category) {
			ids = append(ids, comps[key].GUID)
		}
	}
	return strings.Join(ids, " "), true
}

// consumeBaitEvent 与库存更新共用一个事务，重复咬口、重推及重启不会再扣。
func (s *Server) consumeBaitEvent(ctx context.Context, uid int64, rod, event string, ids []string, cast bool) (baitChange, error) {
	var out baitChange
	if s.DB == nil || rod == "" || event == "" {
		return out, nil
	}
	kind := "bait_bite"
	if cast {
		kind = "bait_cast"
	}
	tx, err := beginEconomyTx(ctx, s.DB)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var previous string
	lookup := tx.QueryRowContext(ctx, `SELECT COALESCE(note,'') FROM txn WHERE uid=? AND kind=? AND guid=?`, uid, kind, event).Scan(&previous)
	if lookup == nil {
		_ = json.Unmarshal([]byte(previous), &out)
		return out, nil
	}
	if lookup != sql.ErrNoRows {
		return out, lookup
	}
	parent, err := getOwnedItemTx(ctx, tx, uid, rod)
	if err != nil {
		return out, err
	}
	if err := addTxnTx(ctx, tx, uid, kind, "", event, 0, 0, "饵料使用"); err != nil {
		return out, err
	}
	seen := map[string]bool{}
	for _, guid := range ids {
		if seen[guid] {
			continue
		}
		seen[guid] = true
		row, err := getOwnedItemTx(ctx, tx, uid, guid)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return out, err
		}
		// 换饵后的背包物品和其他钓组不能被旧咬口扣除。
		if row.RigGUID == "" || row.Container != "rig" || !cast && row.RigGUID != rod {
			continue
		}
		if cast {
			owner := row.RigGUID
			visited := map[string]bool{}
			for owner != rod && owner != "" && !visited[owner] {
				visited[owner] = true
				p, e := getOwnedItemTx(ctx, tx, uid, owner)
				if e != nil {
					return out, e
				}
				owner = p.RigGUID
			}
			if owner != rod {
				continue
			}
		}
		def, err := itemDefByNumericIDTx(ctx, tx, row.NumericID)
		if err != nil {
			return out, err
		}
		consumed, wear := baitUseRule(def, cast)
		if consumed && baitQuantity(row) <= 0 {
			return out, fmt.Errorf("挂载饵料已用尽")
		}
		if !consumed {
			if wear == 0 {
				continue
			}
			fields, err := FieldsFromJSON(row.FieldsJSON)
			if err != nil {
				return out, err
			}
			row.Durability = math.Max(0, row.Durability-wear)
			if idx := fieldIdxByCS(row.WireClass, csDurability); idx >= 0 {
				if !setWireFloat(fields, row.WireClass, idx, row.Durability) {
					return out, fmt.Errorf("饵料耐久字段无效")
				}
			}
			if err := updateItemFieldsTx(ctx, s.DB, tx, row, fields); err != nil {
				return out, err
			}
			out.Updated = append(out.Updated, guid)
			continue
		}
		// 只消耗已挂载的一份，下一次抛竿前再补饵。
		if _, err := consumeItemQtyTx(ctx, tx, row, 1); err != nil {
			return out, err
		}
		if row.Qty > 0 {
			out.Updated = append(out.Updated, guid)
			continue
		}
		if !cast && def.Category == 27 {
			fields, err := FieldsFromJSON(parent.FieldsJSON)
			if err != nil {
				return out, err
			}
			if idx := fieldIdxByCS(parent.WireClass, csRigObject); idx >= 0 && idx < len(fields) {
				if key, _, ok := rigCompKeyForGUID(rigCompsOf(fields[idx].Val), guid); ok {
					if _, err := tx.ExecContext(ctx, `INSERT INTO rod_setting(guid,key,value,updated_ts) VALUES(?,?,?,?) ON CONFLICT(guid,key) DO UPDATE SET value=excluded.value,updated_ts=excluded.updated_ts`, rod, fmt.Sprintf("rebait:%d", rigCompOrdinal(key)), def.ItemID, float64(time.Now().Unix())); err != nil {
						return out, err
					}
				}
			}
		}
		out.Removed = append(out.Removed, guid)
		owner := parent
		if row.RigGUID != rod {
			owner, err = getOwnedItemTx(ctx, tx, uid, row.RigGUID)
			if err != nil {
				return out, err
			}
		}
		if err := removeConsumedRigReferenceTx(ctx, s.DB, tx, owner, guid); err != nil {
			return out, err
		}
		out.Updated = append(out.Updated, owner.GUID)
	}
	receipt, err := json.Marshal(out)
	if err != nil {
		return baitChange{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE txn SET note=? WHERE uid=? AND kind=? AND guid=?`, string(receipt), uid, kind, event); err != nil {
		return baitChange{}, err
	}
	if err := tx.Commit(); err != nil {
		return baitChange{}, err
	}
	return out, nil
}

func removeConsumedRigReferenceTx(ctx context.Context, db *store.DB, tx *sql.Tx, parent *store.Item, guid string) error {
	fields, err := FieldsFromJSON(parent.FieldsJSON)
	if err != nil {
		return err
	}
	idx := fieldIdxByCS(parent.WireClass, csRigObject)
	if idx < 0 || idx >= len(fields) || fields[idx].Val == nil {
		return nil
	}
	f5 := fields[idx].Val
	comps := rigCompsOf(f5)
	key, _, exists := rigCompKeyForGUID(comps, guid)
	if !exists {
		return nil
	}
	delete(comps, key)
	name, rt, _ := rigIdentity(f5)
	fields[idx].Val = buildRig153WithClip(name, rt, comps, rigDepthOf(f5, 100), rigClipOf(f5))
	return updateItemFieldsTx(ctx, db, tx, parent, fields)
}

func (s *Session) pushBaitChange(change baitChange) {
	if len(change.Removed) > 0 {
		s.Push4_21(change.Removed)
	}
	var trees []*proto.Value
	seen := map[string]bool{}
	for _, guid := range change.Updated {
		if seen[guid] {
			continue
		}
		seen[guid] = true
		if row, err := s.Srv.DB.GetItemByGUID(s.pushCtx(), guid); err == nil && row != nil {
			if tree, err := s.Srv.itemTree(s.pushCtx(), row); err == nil {
				trees = append(trees, tree)
			}
		}
	}
	if len(trees) > 0 {
		s.Push(4, 22, proto.Args(proto.S("61"), trees...))
	}
}

func (s *Server) pushBaitedFish(sess *Session, f *RolledFish, rod string) {
	hook := s.mountedHookGuid(s.dbCtx(), rod)
	var change baitChange
	if f.BaitsKnown {
		var err error
		change, err = s.consumeBaitEvent(sess.pushCtx(), sess.Userid, rod, f.FishGUID, strings.Fields(f.BaitGUIDs), false)
		if err != nil {
			s.log.Warn("鱼饵扣减失败，稍后重试", "uid", sess.Userid, "err", err)
			time.AfterFunc(6*time.Second, func() { s.repushPending14_14(sess.Userid) })
			return
		}
	}
	sess.Push14_14(f, hook, rod)
	sess.pushBaitChange(change)
}

func (s *Server) consumeCastBait(sess *Session, st *FishState) error {
	st.mu.Lock()
	rod, cast := st.GearGuid, st.CastTs
	st.mu.Unlock()
	if s.DB == nil || rod == "" || cast.IsZero() {
		return nil
	}
	seen := map[string]bool{rod: true}
	queue := []string{rod}
	var ids []string
	for len(queue) > 0 && len(seen) <= 64 {
		parent := queue[0]
		queue = queue[1:]
		rows, err := s.DB.ListRigComponents(sess.pushCtx(), parent)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if seen[row.GUID] {
				continue
			}
			seen[row.GUID] = true
			ids = append(ids, row.GUID)
			queue = append(queue, row.GUID)
		}
	}
	change, err := s.consumeBaitEvent(sess.pushCtx(), sess.Userid, rod, fmt.Sprintf("%s:%d", rod, cast.UnixNano()), ids, true)
	if err == nil {
		sess.pushBaitChange(change)
	}
	return err
}

// reloadBaitForCast 只在下一次抛竿前补同种钩饵，不提前消耗搏鱼中的备用饵。
func (s *Server) reloadBaitForCast(ctx context.Context, uid int64, rod string) (baitChange, error) {
	var out baitChange
	parent, err := s.DB.GetItemByGUID(ctx, rod)
	if err != nil {
		return out, err
	}
	if parent == nil {
		return out, nil
	}
	if parent.UID != uid {
		return out, fmt.Errorf("不能为其他玩家的钓组补饵")
	}
	for ordinal := int64(0); ordinal < 2; ordinal++ {
		key := fmt.Sprintf("rebait:%d", ordinal)
		id, err := s.DB.GetRodSetting(ctx, rod, key)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return out, err
		}
		if id == "" {
			continue
		}
		comps := s.storedRigComponents(ctx, rod)
		if _, ok := comps[rigCompKey(27, ordinal)]; ok {
			if _, err := s.DB.SQL.ExecContext(ctx, `DELETE FROM rod_setting WHERE guid=? AND key=?`, rod, key); err != nil {
				return out, err
			}
			continue
		}
		rows, err := s.DB.SQL.QueryContext(ctx, `SELECT guid FROM item WHERE uid=? AND item_id=? AND container='backpack' AND qty>0 ORDER BY created_ts,guid`, uid, id)
		if err != nil {
			return out, err
		}
		var candidates []string
		for rows.Next() {
			var guid string
			if err := rows.Scan(&guid); err != nil {
				rows.Close()
				return out, err
			}
			candidates = append(candidates, guid)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, err
		}
		for _, guid := range candidates {
			row, err := s.DB.GetItemByGUID(ctx, guid)
			if err != nil {
				return out, err
			}
			if baitQuantity(row) <= 0 {
				continue
			}
			result := s.rigSetComponent(ctx, uid, rod, 27, guid, 350+ordinal, rigSetOpts{ParentType: familyRod, EnforceCategory: true, SplitStack: true})
			if result.Rejected || result.MountedGUID == "" {
				return out, fmt.Errorf("自动补饵失败")
			}
			out.Updated = append(out.Updated, rod, guid, result.MountedGUID)
			out.Removed = append(out.Removed, result.RemovedGUIDs...)
			break
		}
	}
	return out, nil
}

func (s *Session) prepareBaitForCast(st *FishState, rod string) error {
	st.mu.Lock()
	busy := st.CastActive && st.Pending != nil && !st.Resolved
	st.mu.Unlock()
	if busy || s.Srv.DB == nil || rod == "" {
		return nil
	}
	change, err := s.Srv.reloadBaitForCast(s.pushCtx(), s.Userid, rod)
	if err == nil {
		s.pushBaitChange(change)
	}
	return err
}

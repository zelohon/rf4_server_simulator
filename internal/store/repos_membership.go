package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
)

// 金币金额使用百分位，只购买本地存档中的会员时长。
type MembershipPlan struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Days      int64  `json:"days"`
	PriceGold int64  `json:"priceGold"`
}

func MembershipPlans() []MembershipPlan {
	return []MembershipPlan{
		{ID: "premium_1d", Name: "高级会员 1 天", Days: 1, PriceGold: 100},
		{ID: "premium_7d", Name: "高级会员 7 天", Days: 7, PriceGold: 500},
		{ID: "premium_30d", Name: "高级会员 30 天", Days: 30, PriceGold: 1500},
	}
}

type MembershipState struct {
	Active      bool    `json:"active"`
	Permanent   bool    `json:"permanent"`
	ExpiresAt   int64   `json:"expiresAt"`
	XPBonusRate float64 `json:"xpBonusRate"`
}

func MembershipAt(kind, expiresAt, now int64) MembershipState {
	state := MembershipState{Permanent: kind == 2, ExpiresAt: expiresAt}
	state.Active = state.Permanent || kind == 1 && expiresAt > now
	if state.Permanent {
		state.ExpiresAt = 0
	}
	if state.Active {
		state.XPBonusRate = 1
	}
	return state
}

type MembershipBalance struct {
	Gold   int64 `json:"gold"`
	Silver int64 `json:"silver"`
}

type MembershipStatus struct {
	Membership MembershipState   `json:"membership"`
	Balance    MembershipBalance `json:"balance"`
	Plans      []MembershipPlan  `json:"plans"`
}

type MembershipPurchase struct {
	RequestID   string `json:"requestID"`
	PlanID      string `json:"planID"`
	ChargedGold int64  `json:"chargedGold"`
	ExpiresAt   int64  `json:"expiresAt"`
	Replayed    bool   `json:"replayed"`
}

type MembershipError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *MembershipError) Error() string { return e.Message }
func membershipError(code, message string) error {
	return &MembershipError{Code: code, Message: message}
}

func (d *DB) GetMembershipStatus(ctx context.Context, uid, now int64) (*MembershipStatus, error) {
	profile, err := d.GetProfile(ctx, uid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, membershipError("user_not_found", "本地角色不存在")
	}
	if err != nil {
		return nil, err
	}
	return &MembershipStatus{Membership: MembershipAt(profile.Membership, profile.MembershipExp, now),
		Balance: MembershipBalance{Gold: profile.Gold, Silver: profile.Silver}, Plans: MembershipPlans()}, nil
}

var membershipRequestID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{7,79}$`)

// PurchaseMembership 扣款、续期、订单和在线档案刷新指令一起提交。
func (d *DB) PurchaseMembership(ctx context.Context, uid int64, planID, requestID string, now int64) (*MembershipPurchase, error) {
	var plan MembershipPlan
	for _, candidate := range MembershipPlans() {
		if candidate.ID == planID {
			plan = candidate
			break
		}
	}
	if plan.ID == "" {
		return nil, membershipError("invalid_plan", "会员套餐不存在")
	}
	if !membershipRequestID.MatchString(requestID) {
		return nil, membershipError("invalid_request_id", "购买请求编号无效")
	}
	if uid <= 0 || now < 0 || now > 253402300799-366*86400 {
		return nil, membershipError("invalid_request", "本地购买参数无效")
	}
	tx, err := d.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// 先取得写锁，避免两个进程同时读旧余额后再扣款。
	locked, err := tx.ExecContext(ctx, `UPDATE profile SET uid=uid WHERE uid=?`, uid)
	if err != nil {
		return nil, err
	}
	count, err := locked.RowsAffected()
	if err != nil {
		return nil, err
	}
	if count != 1 {
		return nil, membershipError("user_not_found", "本地角色不存在")
	}
	receipt := &MembershipPurchase{RequestID: requestID, PlanID: planID}
	err = tx.QueryRowContext(ctx, `SELECT plan_id,charged_gold,expires_at FROM membership_order WHERE uid=? AND request_id=?`, uid, requestID).
		Scan(&receipt.PlanID, &receipt.ChargedGold, &receipt.ExpiresAt)
	if err == nil {
		if receipt.PlanID != planID {
			return nil, membershipError("request_conflict", "同一请求编号不能购买不同套餐")
		}
		receipt.Replayed = true
		return receipt, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var gold, membership, expiresAt int64
	if err := tx.QueryRowContext(ctx, `SELECT gold,membership,membership_exp FROM profile WHERE uid=?`, uid).
		Scan(&gold, &membership, &expiresAt); err != nil {
		return nil, err
	}
	if membership == 2 {
		return nil, membershipError("already_permanent", "当前角色已是永久会员，无需购买")
	}
	if gold < plan.PriceGold {
		return nil, membershipError("insufficient_gold", "本地金币不足")
	}
	base := now
	if membership == 1 && expiresAt > base {
		base = expiresAt
	}
	if base > 253402300799-plan.Days*86400 {
		return nil, membershipError("invalid_expiry", "会员到期时间超出范围")
	}
	receipt.ExpiresAt = base + plan.Days*86400
	receipt.ChargedGold = plan.PriceGold
	if _, err := tx.ExecContext(ctx, `UPDATE profile SET gold=gold-?,membership=1,membership_exp=?,updated_ts=? WHERE uid=?`,
		plan.PriceGold, receipt.ExpiresAt, now, uid); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO membership_order(uid,request_id,plan_id,charged_gold,expires_at,created_ts) VALUES(?,?,?,?,?,?)`,
		uid, requestID, planID, receipt.ChargedGold, receipt.ExpiresAt, now); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO txn(uid,ts,kind,item_id,gold_delta,note) VALUES(?,?,'membership',?,?,'local_membership')`,
		uid, now, planID, -plan.PriceGold); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO admin_queue(payload,created_ts) VALUES(?,?)`,
		fmt.Sprintf(`{"op":"refresh_player","uid":%d}`, uid), now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return receipt, nil
}

// SetMembership 保存管理员调整的会员状态、刷新指令和审计。
func (d *DB) SetMembership(ctx context.Context, uid, kind, expiresAt int64, admin string, now int64) error {
	if uid <= 0 || kind < 0 || kind > 2 || (kind == 1 && (expiresAt <= now || expiresAt > 253402300799)) || (kind != 1 && expiresAt != 0) {
		return fmt.Errorf("请选择有效的会员类型和到期时间")
	}
	tx, err := d.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "UPDATE profile SET membership=?,membership_exp=?,updated_ts=? WHERE uid=?", kind, expiresAt, now, uid)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("本地角色不存在")
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO admin_queue(payload,created_ts) VALUES(?,?)", fmt.Sprintf(`{"op":"refresh_player","uid":%d}`, uid), now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO audit_log(admin,action,target,detail,ts) VALUES(?,'membership_update',?,?,?)", admin, fmt.Sprint(uid), fmt.Sprintf("type=%d expiry=%d", kind, expiresAt), now); err != nil {
		return err
	}
	return tx.Commit()
}

package gameserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"rf4server/internal/store"
)

// keepnetCapacity 返回当前玩家可用的护网容量。
// 导入存档中的容量优先；没有导入数据时按普通 100、会员 150。
func (s *Server) keepnetCapacity(ctx context.Context, uid int64) (int64, error) {
	normal, premium := s.importedContainerCapacity(ctx, uid, containerKeepnet, 100, 150)
	p, err := s.DB.GetProfile(ctx, uid)
	if errors.Is(err, sql.ErrNoRows) {
		return normal, nil
	}
	if err != nil {
		return 0, err
	}
	if store.MembershipAt(p.Membership, p.MembershipExp, time.Now().Unix()).Active {
		return premium, nil
	}
	return normal, nil
}

// importedContainerCapacity 读取导入快照中的容器容量，缺失时使用默认值。
func (s *Server) importedContainerCapacity(ctx context.Context, uid int64, kind uint64, normal, member int64) (int64, int64) {
	raw, found, err := s.DB.KVGet(ctx, fmt.Sprintf("native-import:%d:inventory", uid))
	if err != nil || !found {
		return normal, member
	}
	var snapshot struct {
		Counts map[string]struct{ A, B int64 } `json:"countsByKind"`
	}
	if json.Unmarshal([]byte(raw), &snapshot) != nil {
		return normal, member
	}
	if counts, ok := snapshot.Counts[fmt.Sprint(kind)]; ok && counts.A >= 0 && counts.A <= 2147483647 && counts.B >= 0 && counts.B <= 2147483647 {
		return counts.A, counts.B
	}
	return normal, member
}

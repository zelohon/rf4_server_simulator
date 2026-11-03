// Package gateway 实现接入网关：
//
//	HTTPS :443 登录（<logon> XML + token 签发）
//	TCP :9341 realtime 接入（握手/RC4/ACK/传输保活/重连接管）
//	内部隧道客户端（与 gameserver 交换明文 RPC 信封）
//
// 职责边界（架构决策）：RC4 会话加密在网关；主服只见明文信封。
package gateway

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"rf4server/internal/proto"
)

// TokenInfo 登录签发的会话凭据信息。
type TokenInfo struct {
	Userid   int64  `json:"userid"`
	IssuedAt int64  `json:"issued_at"`
	Region   string `json:"region"`
}

// Handoff 重连接管状态：RC4 双向流 + 出帧 wire_id 计数。
// （服务器推送 call_id 计数在 gameserver 侧，由主服自行按 token 恢复。）
type Handoff struct {
	RC4In      proto.RC4State `json:"rc4_in"`       // C→S 解密流
	RC4Out     proto.RC4State `json:"rc4_out"`      // S→C 加密流
	NextWireID uint64         `json:"next_wire_id"` // 下一发出帧的 wire_id
	SavedAt    int64          `json:"saved_at"`
}

// Store 是会话凭据与重连接管的存储抽象。
// 生产环境使用 SQLiteStore；测试可使用 MemStore。
type Store interface {
	SaveToken(ctx context.Context, token string, info TokenInfo, ttl time.Duration) error
	LookupToken(ctx context.Context, token string) (TokenInfo, bool, error)
	SaveHandoff(ctx context.Context, token string, h Handoff, ttl time.Duration) error
	LoadHandoff(ctx context.Context, token string) (*Handoff, error)
	DeleteHandoff(ctx context.Context, token string) error
	// NextUserid 全局自增玩家 id（登录签发用）。
	NextUserid(ctx context.Context) (int64, error)

	// 账号：客户端登录框里的 login/password 映射到独立 uid。
	LookupAccount(ctx context.Context, login string) (Account, bool, error)
	CreateAccount(ctx context.Context, a Account) error
	TouchAccount(ctx context.Context, login, ip, version string) error
	DeleteAccount(ctx context.Context, login string) error
	ListAccounts(ctx context.Context) ([]Account, error)
	SetAccountPassword(ctx context.Context, login, sha1Hex string) error
	SetAccountBanned(ctx context.Context, login string, banned bool) error
	// NextAccountUserid 为新账号分配 uid，跳过 base 之前与已存在的档案。
	NextAccountUserid(ctx context.Context, base int64) (int64, error)
}

// ---- MemStore（测试和内存模式）----

// MemStore 进程内存储（非持久；测试与小规模本地开发用）。
type MemStore struct {
	mu         sync.Mutex
	tokens     map[string]tokenEntry
	handoff    map[string]Handoff
	userid     int64
	accounts   map[string]Account
	accountUid int64
}

type tokenEntry struct {
	info    TokenInfo
	expires time.Time
}

// NewMemStore 构建内存存储。
func NewMemStore(startUserid int64) *MemStore {
	return &MemStore{
		tokens:   map[string]tokenEntry{},
		handoff:  map[string]Handoff{},
		userid:   startUserid - 1,
		accounts: map[string]Account{},
	}
}

func (m *MemStore) SaveToken(_ context.Context, token string, info TokenInfo, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens[token] = tokenEntry{info: info, expires: time.Now().Add(ttl)}
	return nil
}

func (m *MemStore) LookupToken(_ context.Context, token string) (TokenInfo, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.tokens[token]; ok && time.Now().After(e.expires) == false {
		return e.info, true, nil
	}
	// 前缀兜底（客户端回传 logintoken=token[:64] 的场景）
	for t, e := range m.tokens {
		if len(t) > len(token) && strings.HasPrefix(t, token) && time.Now().Before(e.expires) {
			return e.info, true, nil
		}
	}
	return TokenInfo{}, false, nil
}

func (m *MemStore) SaveHandoff(_ context.Context, token string, h Handoff, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	h.SavedAt = time.Now().Unix()
	m.handoff[token] = h
	return nil
}

func (m *MemStore) LoadHandoff(_ context.Context, token string) (*Handoff, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.handoff[token]
	if !ok {
		return nil, nil
	}
	return &h, nil
}

func (m *MemStore) DeleteHandoff(_ context.Context, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.handoff, token)
	return nil
}

func (m *MemStore) NextUserid(_ context.Context) (int64, error) {
	return atomic.AddInt64(&m.userid, 1), nil
}

// ErrNoToken token 不存在/已过期。
var ErrNoToken = errors.New("token 不存在或已过期")

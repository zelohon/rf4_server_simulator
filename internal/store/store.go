// Package store —— 本地 SQLite 存储层：连接 + 内嵌迁移 + 仓储。
//
// 主库（WAL 模式）承载玩家状态、运营表和运行时表（会话凭据、指令队列、
// 共享 KV）；静态资料库以只读 schema ATTACH。
// gateway、gameserver 与内嵌在 launcher 的 admin handler 共享同一个主库文件，
// 跨进程并发依赖 WAL + busy_timeout。
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DB 存储句柄。StaticPath 非空时，每个物理连接都只读 ATTACH 该配置库，
// 并由 staticConnector 创建同名 TEMP 视图与写保护 authorizer。
type DB struct {
	SQL        *sql.DB
	Path       string
	StaticPath string
	static     *staticState
}

// Open 打开（必要时创建）主库并设置 WAL/忙等待。该入口不挂载 static，
// 仅供 seed、迁移和测试使用；服务进程应使用 OpenWithStatic。
func Open(path string) (*DB, error) {
	return openDatabase(path, "")
}

// Migrate 执行内嵌迁移（幂等；自研轻量迁移器，按文件序应用）。
func (d *DB) Migrate() error {
	if _, err := d.SQL.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations(
		version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at REAL NOT NULL)`); err != nil {
		return fmt.Errorf("建迁移表: %w", err)
	}
	// 已应用 00001 的数据库可能缺少后续列。CREATE TABLE IF NOT EXISTS 不会
	// 修改现有表，因此先补齐这些列，避免仓储查询或写入失败。
	if err := d.ensureLegacyPlayerColumns(); err != nil {
		return fmt.Errorf("补全玩家表列: %w", err)
	}
	// 历史遗留：本项目的 gateway_account / happy_hour 曾占用 00018/00019，
	// 与上游同名编号冲突（schema_migrations.version 唯一）。两条迁移本身幂等，
	// 清掉旧登记后按新编号（00028/00029）重新应用即可。
	for _, legacy := range []string{
		"migrations/00018_gateway_account.sql",
		"migrations/00019_happy_hour.sql",
	} {
		if _, err := d.SQL.Exec(`DELETE FROM schema_migrations WHERE name=?`, legacy); err != nil {
			return fmt.Errorf("清理旧迁移登记 %s: %w", legacy, err)
		}
	}
	entries, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(entries)
	for _, name := range entries {
		var applied int
		if err := d.SQL.QueryRow(
			`SELECT COUNT(*) FROM schema_migrations WHERE name=?`, name).Scan(&applied); err != nil {
			return err
		}
		if applied > 0 {
			continue
		}
		body, err := fs.ReadFile(migrationsFS, name)
		if err != nil {
			return err
		}
		tx, err := d.SQL.Begin()
		if err != nil {
			return err
		}
		if name == "migrations/00022_membership_exp_compat.sql" {
			// 兼容两种历史列名：上游早期用 membership_expires_at，
			// 本项目自研会员曾用 membership_expire_at（少一个 s）。
			for _, legacyCol := range []string{"membership_expires_at", "membership_expire_at"} {
				var legacy int
				if err := tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('profile') WHERE name=?`, legacyCol).Scan(&legacy); err != nil {
					_ = tx.Rollback()
					return err
				}
				if legacy == 0 {
					continue
				}
				if _, err := tx.Exec(fmt.Sprintf(
					`UPDATE profile SET membership_exp=%[1]s WHERE membership_exp=0 AND membership=1 AND %[1]s BETWEEN 1 AND 253402300799`,
					legacyCol)); err != nil {
					_ = tx.Rollback()
					return err
				}
			}
		}
		if _, err := tx.Exec(string(body)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("迁移 %s: %w", name, err)
		}
		if _, err := tx.Exec(
			`INSERT INTO schema_migrations(version, name, applied_at) VALUES(?,?,?)`,
			migrationVersion(name), name, float64(time.Now().Unix())); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	if d.static != nil {
		guardCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := d.EnableStaticReadOnly(guardCtx); err != nil {
			return fmt.Errorf("启用静态表只读隔离: %w", err)
		}
	}
	return nil
}

// ensureLegacyPlayerColumns 为已存在的玩家表补齐缺失列；新库由 00001
// 正常创建。列名使用固定白名单，不能把用户输入拼进 DDL。
func (d *DB) ensureLegacyPlayerColumns() error {
	specs := []struct {
		table   string
		column  string
		declSQL string
	}{
		{"profile", "region", "TEXT DEFAULT 'CN'"},
		{"profile", "membership", "INTEGER NOT NULL DEFAULT 0"},
		{"profile", "membership_exp", "INTEGER NOT NULL DEFAULT 0"},
		{"profile", "stars", "INTEGER NOT NULL DEFAULT 0"},
		{"profile", "created_ts", "REAL"},
		{"profile", "updated_ts", "REAL"},
		{"profile", "bank", "INTEGER NOT NULL DEFAULT 0"},
		{"profile", "email", "TEXT DEFAULT ''"},
		{"profile", "role", "INTEGER NOT NULL DEFAULT 0"},
		{"profile", "banned", "INTEGER NOT NULL DEFAULT 0"},
		{"item", "item_id", "TEXT"},
		{"item", "numeric_id", "INTEGER"},
		{"item", "wire_class", "TEXT"},
		{"item", "type_id", "INTEGER"},
		{"item", "container", "TEXT NOT NULL DEFAULT 'backpack'"},
		{"item", "slot", "INTEGER NOT NULL DEFAULT 0"},
		{"item", "rig_guid", "TEXT"},
		{"item", "qty", "INTEGER NOT NULL DEFAULT 1"},
		{"item", "buy_price", "INTEGER NOT NULL DEFAULT 0"},
		{"item", "fields_json", "TEXT"},
		{"item", "created_ts", "REAL"},
		{"item", "durability", "REAL NOT NULL DEFAULT 1.0"},
		{"item", "workmanship", "REAL NOT NULL DEFAULT 1.0"},
	}
	for _, spec := range specs {
		var tableExists int
		if err := d.SQL.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`,
			spec.table).Scan(&tableExists); err != nil {
			return err
		}
		if tableExists == 0 {
			continue
		}
		var columnExists int
		q := fmt.Sprintf(`SELECT COUNT(*) FROM pragma_table_info('%s') WHERE name=?`, spec.table)
		if err := d.SQL.QueryRow(q, spec.column).Scan(&columnExists); err != nil {
			return err
		}
		if columnExists != 0 {
			continue
		}
		stmt := fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`,
			quoteIdentifier(spec.table), quoteIdentifier(spec.column), spec.declSQL)
		if _, err := d.SQL.Exec(stmt); err != nil {
			// admin 与 gameserver 可能同时启动并同时补列；后到的进程会收到
			// duplicate column name，视作已补齐而不是启动失败。
			if strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
				continue
			}
			return fmt.Errorf("%s.%s: %w", spec.table, spec.column, err)
		}
	}
	return nil
}

// migrationVersion 从文件名取序号（00001_xxx.sql → 1）。
func migrationVersion(name string) int64 {
	base := strings.TrimSuffix(strings.TrimPrefix(name, "migrations/"), ".sql")
	var v int64
	for _, ch := range base {
		if ch < '0' || ch > '9' {
			break
		}
		v = v*10 + int64(ch-'0')
	}
	return v
}

// Close 关库。
func (d *DB) Close() error { return d.SQL.Close() }

// contextWithTimeout 独立小助手（避免包内多处直接引 context 循环依赖）。
func contextWithTimeout(d time.Duration) (ctxT, func()) {
	return contextWithTimeoutImpl(d)
}

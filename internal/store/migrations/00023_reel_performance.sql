-- 渔线轮改装后的服务端物理参数。
-- base_drag_kg 来自当前安装的摩擦制动器；locked_drag_kg 按耐久阶段计算。
CREATE TABLE IF NOT EXISTS reel_performance (
    reel_guid       TEXT PRIMARY KEY,
    uid             INTEGER NOT NULL,
    base_drag_kg    REAL NOT NULL DEFAULT 0,
    locked_drag_kg  REAL NOT NULL DEFAULT 0,
    durability      REAL NOT NULL DEFAULT 1,
    updated_ts      REAL NOT NULL,
    FOREIGN KEY(reel_guid) REFERENCES item(guid) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_reel_performance_uid
    ON reel_performance(uid);

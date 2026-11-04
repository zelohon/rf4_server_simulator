-- 玩家篝火点火状态。expires_ft 是服务端持久化的 Windows FILETIME；
-- 客户端收到的是按其世界时钟换算后的绝对结束游戏分钟。
CREATE TABLE IF NOT EXISTS campfire_state (
    uid INTEGER PRIMARY KEY,
    loc TEXT NOT NULL DEFAULT '',
    spot INTEGER NOT NULL DEFAULT 0,
    duration_min INTEGER NOT NULL,
    expires_ft INTEGER NOT NULL,
    updated_ts REAL NOT NULL
);

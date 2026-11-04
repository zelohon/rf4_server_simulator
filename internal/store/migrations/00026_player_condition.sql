-- 玩家姿态/体能状态。对应 308 对象中会随状态变化的前两个比例值；
-- 抛投系统直接从该表读取，后续食物、饮料和疲劳系统也共用同一权威数据。
CREATE TABLE IF NOT EXISTS player_condition(
    uid       INTEGER PRIMARY KEY,
    primary_value   REAL NOT NULL DEFAULT 0.85,
    secondary_value REAL NOT NULL DEFAULT 0.9946,
    updated_ts REAL NOT NULL DEFAULT 0
);

-- 欢乐时光配置（单行，id=1）。
-- 客户端只负责显示，是否在窗口内、是否给加成全部由服务端判定。
CREATE TABLE IF NOT EXISTS happy_hour_config (
  id            INTEGER PRIMARY KEY CHECK (id = 1),
  enabled       INTEGER NOT NULL DEFAULT 1,
  needs_premium INTEGER NOT NULL DEFAULT 1,
  start_minute  INTEGER NOT NULL DEFAULT 0,    -- 0 = 00:00，全天
  duration_min  INTEGER NOT NULL DEFAULT 1440, -- 1440 = 24 小时
  xp_mult       REAL    NOT NULL DEFAULT 1.5,
  money_mult    REAL    NOT NULL DEFAULT 1.0
);
INSERT OR IGNORE INTO happy_hour_config (id) VALUES (1);

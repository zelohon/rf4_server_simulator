-- 轮子轴承与润滑油对真实物理参数的影响。
-- 品质来自静态物品定义 obifdihmcfn（0..100）；wear_progress 保存尚未跨过
-- 20% 耐久档位的超载磨损，确保高品质润滑油在重登后仍然生效。
ALTER TABLE reel_performance ADD COLUMN bearing_item_id TEXT NOT NULL DEFAULT '';
ALTER TABLE reel_performance ADD COLUMN bearing_quality REAL NOT NULL DEFAULT 100;
ALTER TABLE reel_performance ADD COLUMN bearing_factor REAL NOT NULL DEFAULT 1;
ALTER TABLE reel_performance ADD COLUMN oil_item_id TEXT NOT NULL DEFAULT '';
ALTER TABLE reel_performance ADD COLUMN oil_quality REAL NOT NULL DEFAULT 100;
ALTER TABLE reel_performance ADD COLUMN oil_wear_factor REAL NOT NULL DEFAULT 1;
ALTER TABLE reel_performance ADD COLUMN wear_progress REAL NOT NULL DEFAULT 0;

-- 渔线轮抛投放线参数。摩擦系数由轴承、润滑油和轮子耐久共同计算；
-- 放线效率直接参与服务器下发给客户端的最大抛投距离。
ALTER TABLE reel_performance ADD COLUMN cast_friction REAL NOT NULL DEFAULT 0.05;
ALTER TABLE reel_performance ADD COLUMN cast_efficiency REAL NOT NULL DEFAULT 0.95;

-- 临时会员到期时间和本地金币购买回执。
CREATE TABLE membership_order(
    uid INTEGER NOT NULL,
    request_id TEXT NOT NULL,
    plan_id TEXT NOT NULL,
    charged_gold INTEGER NOT NULL CHECK(charged_gold >= 0),
    expires_at INTEGER NOT NULL CHECK(expires_at > 0),
    created_ts INTEGER NOT NULL,
    PRIMARY KEY(uid, request_id)
);

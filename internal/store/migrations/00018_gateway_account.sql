-- 网关账号：客户端登录时携带的 login/password（SHA1）→ 独立 uid。
CREATE TABLE IF NOT EXISTS gateway_account(
    login          TEXT PRIMARY KEY,
    password_sha1  TEXT NOT NULL,
    uid            INTEGER NOT NULL UNIQUE,
    created_ts     INTEGER NOT NULL DEFAULT 0,
    last_login_ts  INTEGER NOT NULL DEFAULT 0,
    last_ip        TEXT    NOT NULL DEFAULT '',
    client_version TEXT    NOT NULL DEFAULT '',
    banned         INTEGER NOT NULL DEFAULT 0,
    note           TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_gateway_account_uid ON gateway_account(uid);

-- 玩家状态表（单文件 SQLite）。
CREATE TABLE IF NOT EXISTS profile(
    uid          INTEGER PRIMARY KEY,
    name         TEXT    NOT NULL DEFAULT 'player',
    level        INTEGER NOT NULL DEFAULT 1,
    xp           INTEGER NOT NULL DEFAULT 0,
    silver       INTEGER NOT NULL DEFAULT 5000,
    gold         INTEGER NOT NULL DEFAULT 0,
    region       TEXT    DEFAULT 'CN',
    membership   INTEGER NOT NULL DEFAULT 0,
    membership_exp INTEGER NOT NULL DEFAULT 0,
    stars        INTEGER NOT NULL DEFAULT 0,
    created_ts   REAL,
    updated_ts   REAL,
    bank         INTEGER NOT NULL DEFAULT 0,
    email        TEXT DEFAULT '',
    role         INTEGER NOT NULL DEFAULT 0,
    banned       INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS item(
    guid         TEXT PRIMARY KEY,
    uid          INTEGER NOT NULL,
    item_id      TEXT,
    numeric_id   INTEGER,
    wire_class   TEXT,
    type_id      INTEGER,
    container    TEXT NOT NULL DEFAULT 'backpack',
    slot         INTEGER NOT NULL DEFAULT 0,
    rig_guid     TEXT,
    qty          INTEGER NOT NULL DEFAULT 1,
    buy_price    INTEGER NOT NULL DEFAULT 0,
    fields_json  TEXT,
    created_ts   REAL,
    durability   REAL NOT NULL DEFAULT 1.0,
    workmanship  REAL NOT NULL DEFAULT 1.0
);
CREATE TABLE IF NOT EXISTS container_slot(
    uid       INTEGER NOT NULL,
    container INTEGER NOT NULL,
    slot      INTEGER NOT NULL,
    guid      TEXT,
    PRIMARY KEY(uid, container, slot)
);
CREATE TABLE IF NOT EXISTS owned_items(
    guid TEXT PRIMARY KEY, uid INTEGER NOT NULL,
    item_id TEXT NOT NULL, price INTEGER, ts REAL
);
CREATE TABLE IF NOT EXISTS rig(
    guid        TEXT PRIMARY KEY,
    uid         INTEGER NOT NULL,
    rig_type    INTEGER NOT NULL,
    name        TEXT,
    fields_json TEXT,
    created_ts  REAL
);
CREATE TABLE IF NOT EXISTS rig_component(
    rig_guid TEXT    NOT NULL,
    slot     INTEGER NOT NULL,
    guid     TEXT,
    extra    INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(rig_guid, slot)
);
CREATE TABLE IF NOT EXISTS rod_pod_setting(
    guid        TEXT PRIMARY KEY,
    uid         INTEGER NOT NULL,
    position    INTEGER NOT NULL DEFAULT 0,
    updated_ts  REAL
);
CREATE TABLE IF NOT EXISTS rod_pod_alarm(
    guid        TEXT    NOT NULL,
    slot        INTEGER NOT NULL,
    volume      REAL    NOT NULL DEFAULT 1.0,
    enabled     INTEGER NOT NULL DEFAULT 1,
    color       INTEGER NOT NULL DEFAULT 0,
    sound       INTEGER NOT NULL DEFAULT 0,
    updated_ts  REAL,
    PRIMARY KEY(guid, slot)
);
CREATE TABLE IF NOT EXISTS rod_setting(
    guid        TEXT    NOT NULL,
    key         TEXT    NOT NULL,
    value       TEXT,
    updated_ts  REAL,
    PRIMARY KEY(guid, key)
);
CREATE TABLE IF NOT EXISTS txn(
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    uid          INTEGER NOT NULL,
    ts           REAL    NOT NULL,
    kind         TEXT    NOT NULL,
    item_id      TEXT,
    guid         TEXT,
    silver_delta INTEGER NOT NULL DEFAULT 0,
    gold_delta   INTEGER NOT NULL DEFAULT 0,
    note         TEXT
);
CREATE TABLE IF NOT EXISTS mail(
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    uid        INTEGER NOT NULL,
    sender     INTEGER NOT NULL DEFAULT 0,
    kind       INTEGER NOT NULL DEFAULT 0,
    text       TEXT,
    items_json TEXT,
    created_ts REAL,
    read       INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS keepnet_items(
    echo_guid TEXT PRIMARY KEY, uid INTEGER NOT NULL,
    species TEXT, name_zh TEXT, weight_g INTEGER, length_m REAL, cat INTEGER,
    keep_loc TEXT, keep_spot INTEGER, keep_bait TEXT, ts REAL
);
CREATE TABLE IF NOT EXISTS skill(
    uid      INTEGER NOT NULL,
    skill_id INTEGER NOT NULL,
    points   REAL    NOT NULL DEFAULT 0,
    PRIMARY KEY(uid, skill_id)
);
CREATE TABLE IF NOT EXISTS skill_node(
    uid      INTEGER NOT NULL,
    node_id  INTEGER NOT NULL,
    level    INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(uid, node_id)
);
CREATE TABLE IF NOT EXISTS player_counters(
    uid INTEGER NOT NULL, key TEXT NOT NULL, value INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(uid, key)
);
CREATE TABLE IF NOT EXISTS player_achievements(
    uid INTEGER NOT NULL, ach_id INTEGER NOT NULL,
    unlocked INTEGER NOT NULL DEFAULT 0,
    progress REAL NOT NULL DEFAULT 0.0,
    claimed INTEGER NOT NULL DEFAULT 0,
    ts REAL NOT NULL DEFAULT 0,
    PRIMARY KEY(uid, ach_id)
);
CREATE TABLE IF NOT EXISTS device(
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    uid        INTEGER NOT NULL,
    fp_id      TEXT    NOT NULL,
    screen_w   INTEGER,
    screen_h   INTEGER,
    sys_lang   TEXT,
    ui_lang    TEXT,
    first_ts   REAL,
    last_ts    REAL,
    seen_count INTEGER NOT NULL DEFAULT 1,
    UNIQUE(uid, fp_id)
);
CREATE TABLE IF NOT EXISTS catch_log(
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ts REAL NOT NULL, loc TEXT, spot INTEGER,
    fish_key TEXT, name_zh TEXT, weight_g INTEGER, length_m REAL,
    source TEXT NOT NULL, bait TEXT, grade INTEGER, xp INTEGER, silver INTEGER, loc_level TEXT, uid INTEGER,
    UNIQUE(source, fish_key, weight_g, loc, spot)
);
CREATE TABLE IF NOT EXISTS competition(
    id         TEXT PRIMARY KEY,
    name       TEXT,
    state      INTEGER NOT NULL DEFAULT 0,
    owner      INTEGER NOT NULL,
    map_key    TEXT,
    max_members INTEGER NOT NULL DEFAULT 0,
    cond_json  TEXT,
    created_ts REAL
);
CREATE TABLE IF NOT EXISTS competition_member(
    comp_id  TEXT NOT NULL,
    uid      INTEGER NOT NULL,
    team     INTEGER NOT NULL DEFAULT 0,
    ready    INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(comp_id, uid)
);
CREATE TABLE IF NOT EXISTS named_fish(
    fish_id  TEXT PRIMARY KEY,
    species  TEXT    NOT NULL,
    name     TEXT    NOT NULL,
    owner    TEXT,
    place    TEXT,
    weight_g INTEGER,
    length_m REAL,
    grade    INTEGER,
    ts       REAL,
    catches  INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS place_access(
    uid INTEGER NOT NULL, place TEXT NOT NULL,
    kind TEXT NOT NULL DEFAULT 'buyout', ts REAL, PRIMARY KEY(uid, place, kind)
);

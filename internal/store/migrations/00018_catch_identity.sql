-- Keep every historic row and its ID; new catches are identified by fish GUID.
CREATE TABLE catch_log_next(
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ts REAL NOT NULL, loc TEXT, spot INTEGER,
    fish_key TEXT, name_zh TEXT, weight_g INTEGER, length_m REAL,
    source TEXT NOT NULL, bait TEXT, grade INTEGER, xp INTEGER, silver INTEGER,
    loc_level TEXT, uid INTEGER, catch_guid TEXT
);
INSERT INTO sqlite_sequence(name,seq)
SELECT 'catch_log_next',seq FROM sqlite_sequence WHERE name='catch_log';
INSERT INTO catch_log_next(
    id,ts,loc,spot,fish_key,name_zh,weight_g,length_m,source,bait,grade,xp,silver,loc_level,uid)
SELECT id,ts,loc,spot,fish_key,name_zh,weight_g,length_m,source,bait,grade,xp,silver,loc_level,uid
FROM catch_log;
DROP TABLE catch_log;
ALTER TABLE catch_log_next RENAME TO catch_log;
CREATE UNIQUE INDEX uq_catch_log_fish_identity ON catch_log(uid,source,catch_guid)
WHERE catch_guid IS NOT NULL;
CREATE INDEX idx_catch_log_uid ON catch_log(uid);
CREATE INDEX idx_catch_log_fish ON catch_log(fish_key);
CREATE INDEX idx_catch_log_uid_fish_weight ON catch_log(uid,fish_key,weight_g);
CREATE INDEX idx_catch_log_uid_ts ON catch_log(uid,ts);

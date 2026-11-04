-- 每次咬口、每次入水分别只记一次饵料消耗，不影响历史流水。
CREATE UNIQUE INDEX IF NOT EXISTS uq_txn_bait_event ON txn(uid, kind, guid)
WHERE kind IN ('bait_bite', 'bait_cast');

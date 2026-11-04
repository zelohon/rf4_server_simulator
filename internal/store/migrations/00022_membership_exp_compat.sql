-- 统一使用上游会员到期字段；旧本地字段仅在本次迁移时回填。
CREATE TRIGGER profile_membership_exp_insert BEFORE INSERT ON profile
WHEN NEW.membership_exp IS NULL OR typeof(NEW.membership_exp)<>'integer' OR NEW.membership_exp<0 OR NEW.membership_exp>253402300799
BEGIN SELECT RAISE(ABORT,'invalid membership expiry'); END;
CREATE TRIGGER profile_membership_exp_update BEFORE UPDATE OF membership_exp ON profile
WHEN NEW.membership_exp IS NULL OR typeof(NEW.membership_exp)<>'integer' OR NEW.membership_exp<0 OR NEW.membership_exp>253402300799
BEGIN SELECT RAISE(ABORT,'invalid membership expiry'); END;

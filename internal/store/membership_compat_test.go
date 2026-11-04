package store

import (
	"context"
	"testing"
)

func TestMembershipUpgradePreservesUpstreamAndLocalExpiry(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "upstream", true: "local"}[legacy], func(t *testing.T) {
			db := openCatchHistoryDBAtVersion(t, 19)
			if _, err := db.SQL.Exec(`INSERT INTO profile(uid,name,gold,membership,membership_exp) VALUES(1,'player',500,1,2000000000)`); err != nil {
				t.Fatal(err)
			}
			if legacy {
				if _, err := db.SQL.Exec(`ALTER TABLE profile ADD COLUMN membership_expires_at INTEGER NOT NULL DEFAULT 0;
					UPDATE profile SET membership_exp=0,membership_expires_at=2000000000 WHERE uid=1`); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				if err := db.Migrate(); err != nil {
					t.Fatal(err)
				}
			}
			p, err := db.GetProfile(context.Background(), 1)
			if err != nil || p.MembershipExp != 2000000000 || p.Gold != 500 {
				t.Fatalf("upgrade changed profile: %+v %v", p, err)
			}
			if _, err := db.PurchaseMembership(context.Background(), 1, "premium_1d", "upgrade-order", 1900000000); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(db.Path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if err := reopened.Migrate(); err != nil {
				t.Fatal(err)
			}
			p, err = reopened.GetProfile(context.Background(), 1)
			if err != nil || p.MembershipExp != 2000086400 || p.Gold != 400 {
				t.Fatalf("restart changed purchase: %+v %v", p, err)
			}
			if err := reopened.SetMembership(context.Background(), 1, 0, 0, "admin", 1900000000); err != nil {
				t.Fatal(err)
			}
			if err := reopened.Migrate(); err != nil {
				t.Fatal(err)
			}
			p, _ = reopened.GetProfile(context.Background(), 1)
			if p.Membership != 0 || p.MembershipExp != 0 {
				t.Fatal("legacy expiry reactivated cleared membership")
			}
		})
	}
}

func TestMembershipCompatibilityBackfillRollsBack(t *testing.T) {
	db := openCatchHistoryDBAtVersion(t, 20)
	if _, err := db.SQL.Exec(`ALTER TABLE profile ADD COLUMN membership_expires_at INTEGER NOT NULL DEFAULT 0;
	INSERT INTO profile(uid,name,membership,membership_expires_at) VALUES(1,'player',1,2000000000);
	CREATE TRIGGER fail_expiry_compat BEFORE INSERT ON schema_migrations WHEN NEW.version=22 BEGIN SELECT RAISE(ABORT,'test'); END`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err == nil {
		t.Fatal("expected migration failure")
	}
	assertCount(t, db, `SELECT COUNT(*) FROM profile WHERE membership_exp=0 AND membership_expires_at=2000000000`, 1)
	if _, err := db.SQL.Exec(`DROP TRIGGER fail_expiry_compat`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	assertCount(t, db, `SELECT COUNT(*) FROM profile WHERE membership_exp=2000000000`, 1)
}

package store

import (
	"context"
	"path/filepath"
	"testing"
)

func openFishGradeTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := OpenWithStatic(filepath.Join(t.TempDir(), "fish-grade.db"), filepath.Join("..", "..", "server_data", "static.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestFishGradeLinesCoverEverySpecies(t *testing.T) {
	db := openFishGradeTestDB(t)
	ctx := context.Background()
	var count int
	if err := db.SQL.QueryRowContext(ctx, `SELECT COUNT(*) FROM fish_grade_lines`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 252 {
		t.Fatalf("三色标记录=%d，期望252", count)
	}
	var missing int
	if err := db.SQL.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM species_weight w
		LEFT JOIN fish_grade_lines g ON g.fish_key=w.fish_key
		WHERE g.fish_key IS NULL`).Scan(&missing); err != nil {
		t.Fatal(err)
	}
	if missing != 0 {
		t.Fatalf("有 %d 个鱼种缺少三色标重量线", missing)
	}
	var invalid int
	if err := db.SQL.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM fish_grade_lines
		WHERE qualified_g <= 0 OR trophy_g <= qualified_g OR blue_trophy_g <= trophy_g`).Scan(&invalid); err != nil {
		t.Fatal(err)
	}
	if invalid != 0 {
		t.Fatalf("有 %d 条三色标阈值顺序错误", invalid)
	}
}

func TestGradeLinesFallsBackToSpeciesWeight(t *testing.T) {
	db := openIntegrityTestDB(t)
	ctx := context.Background()
	if _, err := db.SQL.ExecContext(ctx, `
		INSERT INTO species_weight(fish_key,min_g,max_g,trophy_g,trophy_line_g,super_trophy_line_g)
		VALUES('custom_fish',10,5000,100,1000,2500)`); err != nil {
		t.Fatal(err)
	}
	q, y, b := db.GradeLines(ctx, "custom_fish")
	if q == nil || y == nil || b == nil || *q != 100 || *y != 1000 || *b != 2500 {
		t.Fatalf("无覆盖记录时应回退 species_weight，实得 (%v,%v,%v)", q, y, b)
	}
}

func TestGradeLinesUsesCompleteThreeColorData(t *testing.T) {
	db := openFishGradeTestDB(t)
	ctx := context.Background()
	tests := []struct {
		key  string
		want [3]int64
	}{
		{key: "perch", want: [3]int64{280, 1600, 2700}},
		{key: "c.roach", want: [3]int64{220, 1200, 2000}},
		{key: "w_catfish", want: [3]int64{1000, 100000, 200000}},
		{key: "roach", want: [3]int64{220, 1200, 2000}},
	}
	for _, tc := range tests {
		q, y, b := db.GradeLines(ctx, tc.key)
		if q == nil || y == nil || b == nil || *q != tc.want[0] || *y != tc.want[1] || *b != tc.want[2] {
			t.Fatalf("%s 三色标=(%v,%v,%v)，期望%v", tc.key, q, y, b, tc.want)
		}
	}
}

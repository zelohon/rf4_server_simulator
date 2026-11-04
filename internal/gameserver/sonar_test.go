package gameserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"testing"

	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

const sonarAssetSHA256 = "19b44b49f001e1e59723b9a7ed659770acdf9ffdf0fdc35359e340dc8803fa5e"

func TestSonarGridAsset(t *testing.T) {
	gotHash := fmt.Sprintf("%x", sha256.Sum256(sonarGridsJSON))
	if gotHash != sonarAssetSHA256 {
		t.Fatalf("声呐网格资产被改写: %s, 期望 %s", gotHash, sonarAssetSHA256)
	}

	var asset sonarGridAsset
	if err := json.Unmarshal(sonarGridsJSON, &asset); err != nil {
		t.Fatal(err)
	}
	if asset.SchemaVersion != 1 || asset.Profile != "4.0.24799" || asset.MapID != "level_012_north_sea" {
		t.Fatalf("资产元数据不符: %+v", asset)
	}
	if asset.SourceLevel != "level24" ||
		asset.SourceSHA256 != "79be0a7a3254d7e28c3d39dc7e1e029b8e25eae74efda2b81e8968674886c65f" ||
		asset.CellOrder != "row-major: index=row*columns+column; rows run from topLeftZ toward -Z" {
		t.Fatalf("资产来源/格序元数据不符: %+v", asset)
	}
	if len(asset.Grids) != 273 {
		t.Fatalf("网格数=%d, 期望 273", len(asset.Grids))
	}

	grids, err := loadSonarGrids()
	if err != nil {
		t.Fatal(err)
	}
	if got := grids["loc12_01"]; got != (sonarGrid{268, 1148, 116, 72, 4}) {
		t.Fatalf("loc12_01 几何=%+v", got)
	}
	if got := grids["loc12_273"]; got != (sonarGrid{2480, -1340, 470, 210, 10}) {
		t.Fatalf("loc12_273 几何=%+v", got)
	}
}

func TestSonarSynthesisKnownWaterShape(t *testing.T) {
	obj, err := synthesizeSonar("loc12_01", nil, rand.New(rand.NewSource(7)))
	if err != nil {
		t.Fatal(err)
	}
	if obj.Class != sonarResponseClass || obj.TypeID != 15003 {
		t.Fatalf("响应类型=%s/%d", obj.Class, obj.TypeID)
	}
	f0 := obj.Field(0)
	if got := f0.StrOr(); got != "loc12_01" {
		t.Fatalf("f0=%q", got)
	}
	cells := obj.Field(1).Val
	if cells == nil || cells.Kind != proto.KindArgs || cells.ArgCodeStr() != sonarCellArgCode {
		t.Fatalf("f1 应为 args(15004): %#v", cells)
	}

	// loc12_01 = 116/4 * 72/4 = 29 * 18。
	if cells.ArgCount() != 522 {
		t.Fatalf("15004 格数=%d, 期望 522", cells.ArgCount())
	}

	nonEmpty := 0
	for idx, cell := range cells.Items {
		if cell.Kind == proto.KindElemNull {
			continue
		}
		nonEmpty++
		if cell.Class != sonarCellClass || cell.TypeID != 15004 {
			t.Fatalf("格 %d 类型=%s/%d", idx, cell.Class, cell.TypeID)
		}
		echoes := cell.Field(0).Val
		if echoes == nil || echoes.Kind != proto.KindArgs || echoes.ArgCodeStr() != sonarEchoArgCode {
			t.Fatalf("格 %d f0 应为 args(15005)", idx)
		}
		if echoes.ArgCount() < 1 || echoes.ArgCount() > 4 {
			t.Fatalf("格 %d 回声数=%d, 期望 1..4", idx, echoes.ArgCount())
		}
		for echoIdx, echo := range echoes.Items {
			if echo.Class != sonarEchoClass || echo.TypeID != 15005 {
				t.Fatalf("格 %d 回声 %d 类型=%s/%d", idx, echoIdx, echo.Class, echo.TypeID)
			}
			sizeCM := float64(echo.Fields[0].F32())
			depthM := float64(echo.Fields[1].F32())
			if math.IsNaN(sizeCM) || math.IsInf(sizeCM, 0) || math.IsNaN(depthM) || math.IsInf(depthM, 0) {
				t.Fatalf("格 %d 回声 %d 非有限: %f/%f", idx, echoIdx, sizeCM, depthM)
			}
		}
	}
	if nonEmpty == 0 {
		t.Fatal("已知水域没有生成任何鱼群格")
	}
}

func TestSonarSynthesisWireRoundTrip(t *testing.T) {
	obj, err := synthesizeSonar("loc12_225", nil, rand.New(rand.NewSource(11)))
	if err != nil {
		t.Fatal(err)
	}
	body, err := proto.EncodeValue(obj, wiregen.Table())
	if err != nil {
		t.Fatal(err)
	}
	decoded, _, err := proto.DecodeValue(body, 0, wiregen.Table())
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Class != sonarResponseClass || decoded.TypeID != 15003 {
		t.Fatalf("解码类型=%s/%d", decoded.Class, decoded.TypeID)
	}
	cells := decoded.Field(1).Val
	if cells == nil || cells.ArgCodeStr() != sonarCellArgCode || cells.ArgCount() == 0 {
		t.Fatalf("解码后 15004 网格为空: %#v", cells)
	}

	foundEcho := false
	for _, cell := range cells.Items {
		if cell == nil || cell.Kind == proto.KindElemNull {
			continue
		}
		if cell.Class != sonarCellClass || cell.Field(0).Val.ArgCodeStr() != sonarEchoArgCode {
			t.Fatalf("解码后的 15004/15005 形状错误: %#v", cell)
		}
		foundEcho = true
	}
	if !foundEcho {
		t.Fatal("线缆往返后没有保留任何 15004/15005 回声")
	}
}

func TestSonarUnknownWaterReturnsEmptyGrid(t *testing.T) {
	obj, err := synthesizeSonar("loc99_unknown", nil, rand.New(rand.NewSource(3)))
	if err != nil {
		t.Fatal(err)
	}
	if obj.Class != sonarResponseClass || obj.TypeID != 15003 {
		t.Fatalf("未知水域响应类型=%s/%d", obj.Class, obj.TypeID)
	}
	f0 := obj.Field(0)
	if got := f0.StrOr(); got != "loc99_unknown" {
		t.Fatalf("未知水域 f0=%q", got)
	}
	cells := obj.Field(1).Val
	if cells == nil || cells.Kind != proto.KindNull {
		// 客户端 4.0.25034 不认识 15004，空网格也必须是 null 而不是 args(15004)。
		t.Fatalf("未知水域应回 f1=null: %#v", cells)
	}

	body, err := proto.EncodeValue(obj, wiregen.Table())
	if err != nil {
		t.Fatal(err)
	}
	decoded, _, err := proto.DecodeValue(body, 0, wiregen.Table())
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Field(1).Val.ArgCount() != 0 {
		t.Fatalf("未知水域线缆解码后格数=%d", decoded.Field(1).Val.ArgCount())
	}
}

func TestSonarEchoSamplingRanges(t *testing.T) {
	rng := rand.New(rand.NewSource(19))
	profile := &sonarProfile{depthMin: 2, depthMax: 6, sizeMed: 10, sizeP90: 30}
	for i := 0; i < 5000; i++ {
		sizeCM, depthM := sampleSonarEcho(rng, profile)
		if sizeCM < 5 || sizeCM > 97 || depthM < 2 || depthM > 6 {
			t.Fatalf("档案采样越界: size=%f depth=%f", sizeCM, depthM)
		}
	}

	rng = rand.New(rand.NewSource(23))
	for i := 0; i < 5000; i++ {
		sizeCM, depthM := sampleSonarEcho(rng, nil)
		if sizeCM < 12 || sizeCM > 130 || depthM < 0.8 || depthM > 5.5 {
			t.Fatalf("无档案采样越界: size=%f depth=%f", sizeCM, depthM)
		}
	}
}

func TestSonarHandlerReturnsEmbeddedGrid(t *testing.T) {
	srv, _ := newRigTestServer(t)
	// 默认 sonar_echo=0 只回空网格（客户端不认识 15004 会断线），这里显式打开
	// 回声以验证网格生成逻辑本身。
	if err := srv.DB.TunableSet(context.Background(), "sonar_echo", "1", "test"); err != nil {
		t.Fatal(err)
	}
	registerDynamicProtocolHandlers(srv.dispatch)
	body, ok := hDynamicSonar(dynamicTestCtx(srv, rigRequestTree(t, proto.StrVal("loc12_01"))))
	if !ok {
		t.Fatal("1/10 声呐未处理")
	}
	decoded, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Class != sonarResponseClass || decoded.TypeID != 15003 {
		t.Fatalf("1/10 响应=%s/%d", decoded.Class, decoded.TypeID)
	}
	f0 := decoded.Field(0)
	if got := f0.StrOr(); got != "loc12_01" {
		t.Fatalf("1/10 f0=%q", got)
	}
	cells := decoded.Field(1).Val
	if cells == nil || cells.ArgCodeStr() != sonarCellArgCode || cells.ArgCount() != 522 {
		t.Fatalf("1/10 应回 loc12_01 的 522 格: %#v", cells)
	}
	if reason := srv.dispatch.checkRegisteredResponse(1, 10, body, srv.Table()); reason != "" {
		t.Fatalf("1/10 合同守卫误拒: %s", reason)
	}
}
func TestSonarProfileLookup(t *testing.T) {
	db := connectTestDB(t)
	if _, err := db.SQL.Exec(`
		INSERT INTO sonar_profile(system_id, cells, depth_min, depth_max, size_med, size_p90)
		VALUES(?,?,?,?,?,?)`, "loc12_01", 12, 1.25, 7.75, 8.5, 21.0); err != nil {
		t.Fatal(err)
	}
	srv := &Server{DB: db}
	profile := srv.sonarProfileFor(context.Background(), "loc12_01")
	if profile == nil || *profile != (sonarProfile{1.25, 7.75, 8.5, 21.0}) {
		t.Fatalf("声呐档案=%+v", profile)
	}
	if got := srv.sonarProfileFor(context.Background(), "loc12_02"); got != nil {
		t.Fatalf("无档案水域应回 nil，实得 %+v", got)
	}
}

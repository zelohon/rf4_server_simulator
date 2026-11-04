package gameserver

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sync"
)

// 位置与水体距离判定。
//
// 客户端每 ~12 秒发一次 3/9，负载是一个 type_id=309 的对象，前三个 float 是
// 世界坐标（x, y, z）。服务端据此知道玩家站在哪；再结合按地点（loc）分表的
// 水体矩形，就能判断“这一杆够不够得到水”。

// waterRect 是一块水体矩形（世界坐标 XZ 平面，center/size）。
type waterRect struct {
	X  float64 `json:"x"`
	Z  float64 `json:"z"`
	SX float64 `json:"sx"`
	SZ float64 `json:"sz"`
}

var (
	waterOnce  sync.Once
	waterRects map[string][]waterRect
)

func waterTablePath() (string, bool) {
	var cands []string
	if p := os.Getenv("RF4_WATER_FILE"); p != "" {
		cands = append(cands, p)
	}
	cands = append(cands, "data/water_bounds.json", "server_data/water_bounds.json")
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		cands = append(cands,
			filepath.Join(dir, "data", "water_bounds.json"),
			filepath.Join(dir, "..", "server_data", "water_bounds.json"))
	}
	for _, p := range cands {
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
	}
	return "", false
}

func loadWaterTable() {
	waterRects = map[string][]waterRect{}
	path, ok := waterTablePath()
	if !ok {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var tbl map[string][]waterRect
	if err := json.Unmarshal(raw, &tbl); err != nil {
		return
	}
	waterRects = tbl
}

// waterWithin 判断 (x,z) 在 reach 米内是否有水体。第二个返回值表示该 loc 有没有数据。
func waterWithin(loc string, x, z, reach float64) (bool, bool) {
	waterOnce.Do(loadWaterTable)
	rects, ok := waterRects[loc]
	if !ok || len(rects) == 0 {
		return false, false
	}
	for _, r := range rects {
		dx := math.Max(math.Abs(x-r.X)-r.SX/2, 0)
		dz := math.Max(math.Abs(z-r.Z)-r.SZ/2, 0)
		if math.Hypot(dx, dz) <= reach {
			return true, true
		}
	}
	return false, true
}

// parsePositionBody 从 3/9 请求体里取世界坐标（obj type 309 的前三个 float）。
func parsePositionBody(body []byte) (x, z float64, ok bool) {
	marker := []byte{0x01, 0x35, 0x01, 0x00, 0x00}
	idx := -1
	for i := 0; i+len(marker) <= len(body); i++ {
		if string(body[i:i+len(marker)]) == string(marker) {
			idx = i
			break
		}
	}
	if idx < 0 || idx+5+12 > len(body) {
		return 0, 0, false
	}
	xf := math.Float32frombits(binary.LittleEndian.Uint32(body[idx+5:]))
	zf := math.Float32frombits(binary.LittleEndian.Uint32(body[idx+5+8:]))
	if math.IsNaN(float64(xf)) || math.IsNaN(float64(zf)) {
		return 0, 0, false
	}
	return float64(xf), float64(zf), true
}

// hPositionReport 处理 3/9：记录玩家世界坐标。
func hPositionReport(ctx *Ctx) ([]byte, bool) {
	if ctx.Sess == nil || ctx.Sess.Srv == nil {
		return nil, false
	}
	x, z, ok := parsePositionBody(ctx.Body)
	if !ok {
		return nil, false
	}
	st := ctx.Sess.Srv.stateFor(ctx.Sess.Userid)
	st.mu.Lock()
	st.PosX = x
	st.PosZ = z
	st.HasPos = true
	st.mu.Unlock()
	return nil, false
}

package store

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// 商店货架补齐：历史抓取的 shop_item 与内嵌 kind 模板都不完整，新商品
// （例如 2025 款限量产品）没有货架行，客户端就会显示"无库存"。
// 这里按“店内已有品类 +（品牌店）同品牌”把缺的商品补回货架，
// 价格缺失时用同类商品的中位价估价。

// partCategories 是装配零件品类，不单独在商店售卖。
var partCategories = map[int64]bool{
	4: true, 5: true, 7: true, 8: true, 9: true, 10: true,
	34: true, 36: true, 52: true, 63: true, 81: true,
}

type shopItemMeta struct {
	Category int64
	Kind     string
	Brand    string
	Name     string
}

var (
	shopSuppOnce     sync.Once
	shopSuppErr      error
	shopItemMetas    map[string]shopItemMeta
	shopItemPrices   map[string]*ShopPrice
	shopMedianByKind map[string]int64
	shopMedianByCat  map[int64]int64
	shopSeriesMax    map[string]int64
)

func (d *DB) loadShopSupplement(ctx context.Context) error {
	shopSuppOnce.Do(func() {
		shopItemMetas = make(map[string]shopItemMeta, 4096)
		shopItemPrices = make(map[string]*ShopPrice, 4096)

		rows, err := d.SQL.QueryContext(ctx, `
			SELECT d.item_id, d.category, COALESCE(d.brand,''), COALESCE(c.kind,''), COALESCE(c.name,'')
			FROM item_def d
			LEFT JOIN item_catalog c ON c.item_id = d.item_id`)
		if err != nil {
			shopSuppErr = err
			return
		}
		for rows.Next() {
			var id, brand, kind, name string
			var cat int64
			if err := rows.Scan(&id, &cat, &brand, &kind, &name); err != nil {
				continue
			}
			shopItemMetas[id] = shopItemMeta{Category: cat, Kind: kind, Brand: brand, Name: name}
		}
		rows.Close()

		prows, perr := d.SQL.QueryContext(ctx,
			`SELECT item_id, silver, gold, stock FROM item_price`)
		if perr == nil {
			for prows.Next() {
				p := &ShopPrice{}
				if err := prows.Scan(&p.ItemID, &p.Silver, &p.Gold, &p.Stock); err != nil {
					continue
				}
				shopItemPrices[p.ItemID] = p
			}
			prows.Close()
		}

		// shop_item 是真实货架价，覆盖 item_price 里没有的整件商品（竿/轮等）。
		srows, serr := d.SQL.QueryContext(ctx,
			`SELECT item_id, silver, gold, stock FROM shop_item`)
		if serr == nil {
			for srows.Next() {
				p := &ShopPrice{}
				if err := srows.Scan(&p.ItemID, &p.Silver, &p.Gold, &p.Stock); err != nil {
					continue
				}
				cur, ok := shopItemPrices[p.ItemID]
				if !ok || (cur.Silver <= 0 && p.Silver > 0) {
					shopItemPrices[p.ItemID] = p
				}
			}
			srows.Close()
		}

		// 中位价：先按 item_catalog.kind，再按 item_def.category 兜底。
		byKind := map[string][]int64{}
		byCat := map[int64][]int64{}
		for id, p := range shopItemPrices {
			if p.Silver <= 0 {
				continue
			}
			m, ok := shopItemMetas[id]
			if !ok {
				continue
			}
			if m.Kind != "" {
				byKind[m.Kind] = append(byKind[m.Kind], p.Silver)
			}
			byCat[m.Category] = append(byCat[m.Category], p.Silver)
		}
		shopMedianByKind = medianMap(byKind)
		shopMedianByCat = medianMapInt64(byCat)

		// 系列最高价：同 kind + 同系列首词 + 同尺寸数字。
		// 估价时取“同类同级里最贵的那个”，保证只高不低。
		shopSeriesMax = make(map[string]int64, len(shopItemPrices))
		for id, p := range shopItemPrices {
			eff := p.Silver
			if eff <= 0 && p.Gold > 0 {
				eff = p.Gold * 100
			}
			if eff <= 0 {
				continue
			}
			m, ok := shopItemMetas[id]
			if !ok {
				continue
			}
			key := shopSeriesKey(m.Kind, m.Name)
			if key == "" {
				continue
			}
			if eff > shopSeriesMax[key] {
				shopSeriesMax[key] = eff
			}
		}
	})
	return shopSuppErr
}

func medianMap(in map[string][]int64) map[string]int64 {
	out := make(map[string]int64, len(in))
	for k, v := range in {
		if len(v) == 0 {
			continue
		}
		sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
		out[k] = v[len(v)/2]
	}
	return out
}

func medianMapInt64(in map[int64][]int64) map[int64]int64 {
	out := make(map[int64]int64, len(in))
	for k, v := range in {
		if len(v) == 0 {
			continue
		}
		sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
		out[k] = v[len(v)/2]
	}
	return out
}

func brandMatches(brand string, brands []string) bool {
	b := strings.ToLower(strings.TrimSpace(brand))
	if b == "" {
		return false
	}
	for _, want := range brands {
		w := strings.ToLower(strings.TrimSpace(want))
		if w != "" && strings.HasPrefix(b, w) {
			return true
		}
	}
	return false
}

// augmentShopShelf 补齐货架，返回补齐后的货架与新增条数。
func (d *DB) augmentShopShelf(ctx context.Context, def shopStoreDefinition, shelf []*ShopPrice) ([]*ShopPrice, int, error) {
	if len(shelf) == 0 {
		return shelf, 0, nil
	}
	if err := d.loadShopSupplement(ctx); err != nil {
		return shelf, 0, err
	}
	have := make(map[string]bool, len(shelf))
	cats := make(map[int64]bool)
	for _, p := range shelf {
		if p == nil {
			continue
		}
		have[p.ItemID] = true
		if m, ok := shopItemMetas[p.ItemID]; ok && !partCategories[m.Category] {
			cats[m.Category] = true
		}
	}
	if len(cats) == 0 {
		return shelf, 0, nil
	}
	add := make([]*ShopPrice, 0, 1024)
	for id, m := range shopItemMetas {
		if have[id] || !cats[m.Category] || partCategories[m.Category] {
			continue
		}
		if len(def.Brands) > 0 && !brandMatches(m.Brand, def.Brands) {
			continue
		}
		if p, ok := shopItemPrices[id]; ok && (p.Silver > 0 || p.Gold > 0) {
			cp := *p
			if cp.Stock <= 0 {
				cp.Stock = 1000
			}
			add = append(add, &cp)
			continue
		}
		// 优先用“同类同级最贵”的价格，其次同类型中位价。
		silver := shopSeriesMax[shopSeriesKey(m.Kind, m.Name)]
		if silver <= 0 {
			silver = shopMedianByKind[m.Kind]
		}
		if silver <= 0 {
			silver = shopMedianByCat[m.Category]
		}
		if silver <= 0 {
			silver = 1000
		}
		// 价格随机上浮 0~10%（按 item_id 稳定，不会每次刷新都变）
		silver = silver + silver*int64(stableHash(id)%11)/100
		add = append(add, &ShopPrice{ItemID: id, Silver: silver, Stock: 1000})
	}
	if len(add) == 0 {
		return shelf, 0, nil
	}
	sort.Slice(add, func(i, j int) bool { return add[i].ItemID < add[j].ItemID })
	out := make([]*ShopPrice, 0, len(shelf)+len(add))
	out = append(out, shelf...)
	out = append(out, add...)
	return out, len(add), nil
}

// shopSeriesKey 生成"同类型+同系列首词+同尺寸"的键，例如
// rod_spinning|venga|10000。取不到尺寸时返回空串。
func shopSeriesKey(kind, name string) string {
	tokens := strings.Fields(strings.ToLower(name))
	if len(tokens) == 0 || kind == "" {
		return ""
	}
	size := ""
	for i := len(tokens) - 1; i >= 0; i-- {
		if isAllDigits(tokens[i]) {
			size = tokens[i]
			break
		}
	}
	if size == "" {
		return ""
	}
	return kind + "|" + tokens[0] + "|" + size
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
// stableHash 是 item_id 的稳定散列，用于价格微浮动。
func stableHash(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}
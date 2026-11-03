package store

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// shop_kind_defs.json 保存商店定义与 7 类基础货架数据。它只提供默认货架，
// 不替代玩家库中的门店货架或 shop_item_override 覆盖数据。
//
//go:embed assets/shop_kind_defs.json
var embeddedShopKindDefs []byte

type shopCatalogSKU struct {
	ItemID string `json:"item_id"`
	Silver int64  `json:"silver"`
	Gold   int64  `json:"gold"`
	Stock  int64  `json:"stock"`
}

type shopStoreDefinition struct {
	Kind   int      `json:"kind"`
	Brands []string `json:"brands"`
	MapKey string   `json:"map_key"`
}

type shopKindCatalog struct {
	Stores  map[string]shopStoreDefinition `json:"stores"`
	Kinds   map[string][]shopCatalogSKU    `json:"kinds"`
	Sources map[string]string              `json:"sources"`
}

var (
	shopCatalogOnce sync.Once
	shopCatalogData *shopKindCatalog
	shopCatalogErr  error
)

func embeddedShopKindCatalog() (*shopKindCatalog, error) {
	shopCatalogOnce.Do(func() {
		var catalog shopKindCatalog
		if err := json.Unmarshal(embeddedShopKindDefs, &catalog); err != nil {
			shopCatalogErr = fmt.Errorf("解析内嵌商店目录: %w", err)
			return
		}
		if len(catalog.Stores) == 0 {
			shopCatalogErr = fmt.Errorf("内嵌商店目录没有 stores")
			return
		}
		shopCatalogData = &catalog
	})
	return shopCatalogData, shopCatalogErr
}

type shopOverrideValue struct {
	Silver sql.NullInt64
	Gold   sql.NullInt64
	Stock  sql.NullInt64
}

const (
	shopShelfSourceRecorded = "recorded"
	shopShelfSourceBrand    = "brand_reuse"
	shopShelfSourceKind     = "kind_template"
	shopShelfSourceMissing  = "missing_evidence"
	shopShelfSourceUnknown  = "unknown_store"
)

type shopShelfResolution struct {
	Base         []*ShopPrice
	Effective    []*ShopPrice
	Overrides    map[string]shopOverrideValue
	Definition   shopStoreDefinition
	CatalogKnown bool
	Source       string
}

func (d *DB) loadRecordedShopShelves(ctx context.Context) (map[string][]*ShopPrice, error) {
	rows, err := d.SQL.QueryContext(ctx, `
		SELECT store_id, item_id, silver, gold, stock
		FROM shop_item ORDER BY store_id, item_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string][]*ShopPrice)
	for rows.Next() {
		var storeID string
		p := &ShopPrice{}
		if err := rows.Scan(&storeID, &p.ItemID, &p.Silver, &p.Gold, &p.Stock); err != nil {
			return nil, err
		}
		out[storeID] = append(out[storeID], p)
	}
	return out, rows.Err()
}

func (d *DB) loadShopOverrides(ctx context.Context, storeID string) (map[string]shopOverrideValue, error) {
	rows, err := d.SQL.QueryContext(ctx, `
		SELECT item_id, silver, gold, stock
		FROM shop_item_override WHERE store_id=?`, storeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]shopOverrideValue)
	for rows.Next() {
		var itemID string
		var v shopOverrideValue
		if err := rows.Scan(&itemID, &v.Silver, &v.Gold, &v.Stock); err != nil {
			return nil, err
		}
		out[itemID] = v
	}
	return out, rows.Err()
}

func (d *DB) loadAllShopOverrides(ctx context.Context) (map[string]map[string]shopOverrideValue, error) {
	rows, err := d.SQL.QueryContext(ctx, `
		SELECT store_id, item_id, silver, gold, stock FROM shop_item_override`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]map[string]shopOverrideValue)
	for rows.Next() {
		var storeID, itemID string
		var v shopOverrideValue
		if err := rows.Scan(&storeID, &itemID, &v.Silver, &v.Gold, &v.Stock); err != nil {
			return nil, err
		}
		m := out[storeID]
		if m == nil {
			m = make(map[string]shopOverrideValue)
			out[storeID] = m
		}
		m[itemID] = v
	}
	return out, rows.Err()
}

func shopBrandKey(brands []string) string {
	if len(brands) == 0 {
		return ""
	}
	cp := append([]string(nil), brands...)
	sort.Strings(cp)
	return strings.Join(cp, "\x00")
}

func copyShopPrices(rows []*ShopPrice) []*ShopPrice {
	out := make([]*ShopPrice, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			continue
		}
		v := *row
		out = append(out, &v)
	}
	return out
}

func catalogShopPrices(rows []shopCatalogSKU) []*ShopPrice {
	out := make([]*ShopPrice, 0, len(rows))
	for _, row := range rows {
		out = append(out, &ShopPrice{
			ItemID: row.ItemID,
			Silver: row.Silver,
			Gold:   row.Gold,
			Stock:  row.Stock,
		})
	}
	return out
}

// resolveShopBase selects the immutable base shelf using the evidence order:
// exact recorded store -> same kind + same brand-set recorded store ->
// same-kind representative recording -> empty. Per-store overrides are applied
// only after this selection.
func resolveShopBase(storeID string, catalog *shopKindCatalog, recorded map[string][]*ShopPrice) ([]*ShopPrice, string, shopStoreDefinition, bool) {
	def, known := catalog.Stores[storeID]
	if rows := recorded[storeID]; len(rows) > 0 {
		return copyShopPrices(rows), shopShelfSourceRecorded, def, known
	}
	if !known {
		return nil, shopShelfSourceUnknown, def, false
	}

	kindKey := strconv.Itoa(def.Kind)
	brandKey := shopBrandKey(def.Brands)

	// Unbranded stores are a single franchise family. Prefer the explicitly
	// documented witness store for this kind when it has a recording.
	if len(def.Brands) == 0 {
		if source := catalog.Sources[kindKey]; source != "" {
			if rows := recorded[source]; len(rows) > 0 {
				return copyShopPrices(rows), shopShelfSourceBrand, def, true
			}
		}
	}

	recordedStores := make([]string, 0, len(recorded))
	for sid := range recorded {
		recordedStores = append(recordedStores, sid)
	}
	sort.Strings(recordedStores)
	for _, sid := range recordedStores {
		other, ok := catalog.Stores[sid]
		if !ok || other.Kind != def.Kind {
			continue
		}
		if shopBrandKey(other.Brands) != brandKey {
			continue
		}
		if rows := recorded[sid]; len(rows) > 0 {
			return copyShopPrices(rows), shopShelfSourceBrand, def, true
		}
	}

	if rows := catalog.Kinds[kindKey]; len(rows) > 0 {
		return catalogShopPrices(rows), shopShelfSourceKind, def, true
	}
	return nil, shopShelfSourceMissing, def, true
}

func applyShopOverrides(base []*ShopPrice, overrides map[string]shopOverrideValue) []*ShopPrice {
	out := make([]*ShopPrice, 0, len(base))
	for _, row := range base {
		if row == nil {
			continue
		}
		v := *row
		if override, ok := overrides[v.ItemID]; ok {
			if override.Silver.Valid {
				v.Silver = override.Silver.Int64
			}
			if override.Gold.Valid {
				v.Gold = override.Gold.Int64
			}
			if override.Stock.Valid {
				v.Stock = override.Stock.Int64
			}
		}
		out = append(out, &v)
	}
	return out
}

func (d *DB) effectiveShopShelf(ctx context.Context, storeID string) (*shopShelfResolution, error) {
	storeID = strings.TrimSpace(storeID)
	catalog, err := embeddedShopKindCatalog()
	if err != nil {
		return nil, err
	}
	recorded, err := d.loadRecordedShopShelves(ctx)
	if err != nil {
		return nil, err
	}
	base, source, def, known := resolveShopBase(storeID, catalog, recorded)
	overrides := map[string]shopOverrideValue{}
	if len(base) > 0 {
		overrides, err = d.loadShopOverrides(ctx, storeID)
		if err != nil {
			return nil, err
		}
	}
	effective := applyShopOverrides(base, overrides)
	// 补齐货架：历史抓取/内嵌模板缺的新商品（例如 2025 款）在客户端看来是
	// “无库存”，这里按“同店内已有品类 +（品牌店）同品牌”补上，价格缺失时
	// 用同类商品中位价估价。
	effective, _, err = d.augmentShopShelf(ctx, def, effective)
	if err != nil {
		return nil, err
	}
	return &shopShelfResolution{
		Base:         base,
		Effective:    effective,
		Overrides:    overrides,
		Definition:   def,
		CatalogKnown: known,
		Source:       source,
	}, nil
}

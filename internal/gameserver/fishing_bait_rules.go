package gameserver

import (
	_ "embed"
	"encoding/json"
	"strings"
	"unicode"
)

//go:embed fishing_bait_rules.txt
var fishBaitRuleText string

type fishBaitRule struct {
	allow, deny []string
}

var fishBaitRules = parseFishBaitRules(fishBaitRuleText)

func ruleName(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\ufeff', '(', ')', '（', '）':
			return -1
		case '鯡':
			return '鲱'
		case '鮭':
			return '鲑'
		case '鰻':
			return '鳗'
		case '鳌':
			return '螯'
		}
		if unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, s)
}

func parseFishBaitRules(raw string) map[string]fishBaitRule {
	out := map[string]fishBaitRule{}
	for _, line := range strings.Split(raw, "\n") {
		name, tail, ok := strings.Cut(line, "  吃 ")
		if !ok {
			continue
		}
		bait, _, ok := strings.Cut(tail, "  前置条件 ")
		if !ok {
			continue
		}
		bait = strings.NewReplacer("（", "(", "）", ")").Replace(bait)
		allowed, extra, _ := strings.Cut(bait, "(")
		r := fishBaitRule{allow: strings.Split(strings.TrimSpace(allowed), "/")}
		if _, denied, ok := strings.Cut(extra, "不吃"); ok {
			denied = strings.FieldsFunc(denied, func(c rune) bool { return c == ')' || c == '；' || c == ';' })[0]
			r.deny = strings.Split(denied, "/")
		}
		out[ruleName(name)] = r
	}
	return out
}

type ruleBait struct {
	id, name                     string
	category, lureType, baitType int64
}

func (b ruleBait) matches(term string) bool {
	t := ruleName(term)
	n := ruleName(b.name)
	aliases := map[string][]string{
		"muckworm":    {"粪便蠕虫", "红蚯蚓"},
		"dough_sweet": {"甜的面团"},
		"caddisfly":   {"毛翅目幼虫"},
		"baitfrog":    {"青蛙"},
	}
	for _, alias := range aliases[b.id] {
		if t == ruleName(alias) {
			return true
		}
	}
	if t == "各类饵" {
		return true
	}
	if t == "拟饵" {
		return b.category == 17
	}
	if t == "活饵类" {
		return b.category == 27 && b.baitType == 4
	}
	if t == "饵鱼类" || t == "饵鱼" {
		return b.category == 27 && (b.baitType == 4 || b.baitType == 15)
	}
	if b.category == 27 {
		switch t {
		case "蠕虫", "蠕虫类":
			return b.baitType == 6
		case "昆虫幼虫":
			return b.baitType == 2
		case "昆虫":
			return b.baitType == 7
		case "鱼肉类":
			return b.baitType == 14 || b.id == "fish_flesh"
		}
	}
	if b.category == 17 {
		switch t {
		case "亮片", "亮片类", "亮片系列饵":
			return b.lureType == 1 || b.lureType == 2
		case "旋转饵", "旋转饵类", "旋转亮片路亚":
			return b.lureType == 1
		case "软饵类", "拟真软饵":
			return b.lureType == 3
		}
	}
	for _, suffix := range []string{"系列饵", "系列", "类"} {
		if strings.HasSuffix(t, suffix) {
			stem := strings.TrimSuffix(t, suffix)
			if strings.HasPrefix(n, stem) {
				// 型号 1 不能匹配型号 1000；颜色编号通常由连字符分隔。
				tail := strings.TrimPrefix(n, stem)
				if len(stem) > 0 && len(tail) > 0 && stem[len(stem)-1] >= '0' && stem[len(stem)-1] <= '9' && tail[0] >= '0' && tail[0] <= '9' {
					return false
				}
				return true
			}
			return false
		}
	}
	if n == t {
		return true
	}
	if strings.ContainsAny(t, "abcdefghijklmnopqrstuvwxyz") && strings.HasPrefix(n, t) {
		tail := strings.TrimPrefix(n, t)
		return len(tail) > 0 && (tail[0] == '-' || tail[0] == '#' || (t[len(t)-1] < '0' || t[len(t)-1] > '9') && tail[0] >= '0' && tail[0] <= '9')
	}
	return false
}

// filterFishBaits 只保留有吃饵规则或实钓证据的鱼；明确排除的饵不被证据放行。
func (s *Server) filterFishBaits(loc, bait string, keys []string, weights []float64) ([]string, []float64) {
	if bait == "" {
		return nil, nil
	}
	ctx := s.dbCtx()
	b := ruleBait{id: bait}
	var raw string
	if err := s.DB.SQL.QueryRowContext(ctx, `SELECT COALESCE(c.name_zh,c.name,''),d.category,COALESCE(d.attrs_json,'{}')
		FROM item_def d LEFT JOIN item_catalog c ON c.item_id=d.item_id WHERE d.item_id=?`, bait).Scan(&b.name, &b.category, &raw); err != nil {
		return nil, nil
	}
	if b.category != 17 && b.category != 27 {
		return nil, nil
	}
	var attrs struct {
		LureType int64 `json:"mbemakijdeh"`
		BaitType int64 `json:"fopbhlmkcpe"`
	}
	if json.Unmarshal([]byte(raw), &attrs) != nil {
		return nil, nil
	}
	b.lureType, b.baitType = attrs.LureType, attrs.BaitType
	names := map[string]string{}
	// 后台可向水域添加鱼种，名称也须从其他水域解析，避免遗漏该鱼的禁饵。
	rows, err := s.DB.SQL.QueryContext(ctx, `SELECT fish_key,fish_zh FROM loc_wiki_fish
		UNION SELECT fish_key,name_zh FROM loc_fish`)
	if err != nil {
		return nil, nil
	}
	for rows.Next() {
		var key, name string
		if rows.Scan(&key, &name) == nil {
			if _, known := fishBaitRules[ruleName(name)]; known {
				names[key] = name
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil
	}
	evidence := s.DB.BaitTiers(ctx, bait)
	localEvidence := map[string]bool{}
	rows, err = s.DB.SQL.QueryContext(ctx, `SELECT fish_key FROM loc_bait_fish WHERE loc=? AND bait IN (?,?) AND catches>0`, loc, bait, b.name)
	if err == nil {
		for rows.Next() {
			var key string
			if rows.Scan(&key) == nil {
				localEvidence[key] = true
			}
		}
		if rows.Err() != nil {
			localEvidence = map[string]bool{}
		}
		rows.Close()
	}
	outKeys := make([]string, 0, len(keys))
	outWeights := make([]float64, 0, len(keys))
	for i, key := range keys {
		if weights[i] <= 0 {
			continue
		}
		r := fishBaitRules[ruleName(names[key])]
		denied, allowed := false, false
		for _, term := range r.deny {
			if b.matches(term) {
				denied = true
				break
			}
		}
		if denied {
			continue
		}
		for _, term := range r.allow {
			if b.matches(term) {
				allowed = true
				break
			}
		}
		if allowed || evidence[key] > 0 || localEvidence[key] {
			outKeys = append(outKeys, key)
			outWeights = append(outWeights, weights[i])
		}
	}
	return outKeys, outWeights
}

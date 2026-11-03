#!/usr/bin/env python
# gen_compat_parts.py - 生成 data/compat_parts.json 草稿
#
# 规则（近似，非官方数据）：对每个渔轮定义的每个零件槽位，
# 在同品类里找“命名前缀相近 + 编号相近”的型号，取最近的若干个。
# 生成后可以手工删改，服务端优先读这张表。
#
# 用法：python tools\gen_compat_parts.py [数据库路径] [输出路径] [每槽最多几个]
import json
import os
import re
import sqlite3
import sys

DB = sys.argv[1] if len(sys.argv) > 1 else r'J:\code\RF4S\data\rf4server.db'
OUT = sys.argv[2] if len(sys.argv) > 2 else r'J:\code\RF4S\server_data\compat_parts.json'
MAXN = int(sys.argv[3]) if len(sys.argv) > 3 else 6

SLOT_CATS = (7, 8, 9, 10, 34, 52, 63, 81)


def lcp(a, b):
    n = 0
    for x, y in zip(a, b):
        if x != y:
            break
        n += 1
    return n


def tailnum(s):
    m = re.search(r'(\d+)$', s)
    return int(m.group(1)) if m else None


def main():
    con = sqlite3.connect(DB)
    cur = con.cursor()
    by_cat = {}
    for cat in SLOT_CATS:
        by_cat[cat] = [r[0] for r in cur.execute(
            'select item_id from item_def where category=?', (cat,))]

    table = {}
    rows = cur.execute("select item_id, attrs_json from item_def where category=6").fetchall()
    for reel_id, attrs_json in rows:
        try:
            attrs = json.loads(attrs_json)
        except Exception:
            continue
        if not isinstance(attrs, dict):
            continue
        entry = {}
        for key, cfg in attrs.items():
            if not isinstance(cfg, dict):
                continue
            sub = cfg.get('nhjofmmmngl')
            cat = cfg.get('mgkfaekpnoj')
            if not sub or cat not in by_cat:
                continue
            base = tailnum(sub)
            cands = []
            for other in by_cat[cat]:
                if other == sub:
                    continue
                n = lcp(sub, other)
                if n < 6:
                    continue
                t = tailnum(other)
                if base is not None and t is not None:
                    d = abs(t - base)
                    if d > 150:
                        continue
                    cands.append((other, n, d))
                else:
                    cands.append((other, n, 10 ** 9))
            cands.sort(key=lambda x: (-x[1], x[2]))
            picked = [c[0] for c in cands[:MAXN]]
            if picked:
                entry[str(cat)] = picked
        if entry:
            table[reel_id] = entry

    os.makedirs(os.path.dirname(OUT), exist_ok=True)
    with open(OUT, 'w', encoding='utf-8') as f:
        json.dump(table, f, ensure_ascii=False, indent=1, sort_keys=True)
    total = sum(len(v) for v in table.values())
    print('reels=%d slots=%d -> %s' % (len(table), total, OUT))


if __name__ == '__main__':
    main()

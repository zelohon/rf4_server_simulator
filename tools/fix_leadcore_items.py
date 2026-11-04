#!/usr/bin/env python3
"""修复铅芯线（item_def.category=57）长度/刚性缺失导致客户端空白卡。

背景：客户端把铅芯线按钓线实例渲染（wire 类 ohciikgjghe / type 140），
实例长度来自 attrs_json 的 ljnnpokapig。上游静态库这两项为空，长度落 0，
客户端把铅芯线当成 0 米的废线过滤掉，钓组槽位只剩一张空白卡。

用法：
    python tools/fix_leadcore_items.py --db D:/RF4/dist/data/rf4server.db \
        --static D:/RF4/dist/server_data/static.sqlite3

脚本幂等：只补缺失/为零的字段，已有值不覆盖。
"""
import argparse
import json
import re
import sqlite3
import struct
import time


def load_defs(static_path):
    """从静态库读出 category=57 的长度/刚性；缺失时按品名 LB 估算。"""
    db = sqlite3.connect('file:%s?mode=ro' % static_path, uri=True)
    out = {}
    for item_id, attrs_json, name in db.execute(
            'select d.item_id, d.attrs_json, coalesce(c.name, \'\') '
            'from item_def d left join item_catalog c on c.item_id=d.item_id '
            'where d.category=57'):
        attrs = json.loads(attrs_json or '{}')
        length = attrs.get('ljnnpokapig') or 10
        rigid = attrs.get('lgjeahdobch')
        if not rigid:
            rigid = f32_bits(0.4 + lb_of(name or item_id) / 150.0)
        out[item_id] = (int(rigid), int(length))
    db.close()
    return out


def lb_of(text):
    """从 'Leadcore ... 80LB' 这类名称估一个 LB 值，仅作缺失时的兜底。"""
    m = re.search(r'(\d+)\s*LB', text, re.I)
    return int(m.group(1)) if m else 20


def f32_bits(v):
    return struct.unpack('>I', struct.pack('>f', v))[0]


def patch_defs(db, defs):
    changed = 0
    for nid, item_id, attrs_json in list(db.execute(
            'select numeric_id, item_id, attrs_json from item_def where category=57')):
        if item_id not in defs:
            continue
        attrs = json.loads(attrs_json or '{}')
        rigid, length = defs[item_id]
        if attrs.get('lgjeahdobch') == rigid and attrs.get('ljnnpokapig') == length:
            continue
        attrs['lgjeahdobch'] = rigid
        attrs['ljnnpokapig'] = length
        db.execute('update item_def set attrs_json=? where numeric_id=?',
                   (json.dumps(attrs), nid))
        changed += 1
    return changed


def patch_items(db, defs):
    changed = 0
    rows = list(db.execute(
        "select guid, item_id, fields_json from item "
        "where item_id like 'lc\\_%' escape '\\'"))
    for guid, item_id, fields_json in rows:
        if item_id not in defs:
            continue
        fields = json.loads(fields_json)
        rigid, length = defs[item_id]
        if len(fields) < 3:
            continue
        if fields[0].get('v') == rigid and fields[2].get('v') == length:
            continue
        fields[0]['v'] = rigid
        fields[2]['v'] = length
        # 刷新实例版本戳，让客户端重登后重新拉取该物品。
        if len(fields) > 9 and fields[9].get('f') == 'u64':
            fields[9]['v'] = int((time.time() + 62135596800) * 10_000_000)
        db.execute('update item set fields_json=? where guid=?',
                   (json.dumps(fields, separators=(',', ':')), guid))
        changed += 1
    return changed


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--db', required=True, help='玩家库 rf4server.db')
    ap.add_argument('--static', default='', help='静态库，缺省取玩家库上一级的 server_data/static.sqlite3')
    args = ap.parse_args()
    static_path = args.static
    if not static_path:
        base = args.db.replace('\\', '/').rsplit('/', 2)[0]
        static_path = base + '/server_data/static.sqlite3'

    defs = load_defs(static_path)
    if not defs:
        raise SystemExit('静态库没有 category=57 的定义: ' + static_path)
    db = sqlite3.connect(args.db)
    try:
        d = patch_defs(db, defs)
        i = patch_items(db, defs)
        db.commit()
    finally:
        db.close()
    print('item_def 更新 %d 条，库存实例修复 %d 条（定义 %d 个）' % (d, i, len(defs)))


if __name__ == '__main__':
    main()

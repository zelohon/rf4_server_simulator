#!/usr/bin/env python3
"""修复铅芯线（item_def.category=57）落成钓线类导致的空白卡片。

背景：客户端把铅芯线渲染成独立物品类 emfindpkead（wire 类 fmaajjlcdmc / type 167，
f0 为强度位）。早期 categoryWireClass 没有 57，实例被创建成钓线类 ohciikgjghe / 140，
客户端在背包和选择框里只能画出空白卡。新版 gameserver 启动修复会自动改写；
本脚本用于不能升级程序的离线库。

用法：
    python tools/fix_leadcore_items.py --db D:/RF4/dist/data/rf4server.db \
        --static D:/RF4/dist/server_data/static.sqlite3

脚本幂等：已经是对应类的实例不再改写。
"""
import argparse
import json
import re
import sqlite3
import struct
import time

TARGET_CLASS = 'fmaajjlcdmc'
TARGET_TYPE = 167


def f32_bits(v):
    return struct.unpack('>I', struct.pack('>f', float(v)))[0]


def f32(bits):
    try:
        return struct.unpack('>f', struct.pack('>I', int(bits)))[0]
    except Exception:
        return 0.0


def lb_of(text):
    m = re.search(r'(\d+)\s*LB', text or '', re.I)
    return int(m.group(1)) if m else 20


def load_defs(static_path):
    """category=57 的物理参数：强度位、状态位、长度/刚性（缺失时按品名 LB 估算）。"""
    db = sqlite3.connect('file:%s?mode=ro' % static_path, uri=True)
    out = {}
    for item_id, attrs_json, name in db.execute(
            "select d.item_id, d.attrs_json, coalesce(c.name, '') "
            "from item_def d left join item_catalog c on c.item_id=d.item_id "
            "where d.category=57"):
        attrs = json.loads(attrs_json or '{}')
        strength = attrs.get('kngaaefkpka')
        if not strength:
            strength = f32_bits(lb_of(name or item_id) * 0.4536)
        out[item_id] = {
            'strength': int(strength),
            'state': int(attrs.get('hgaijieklek') or 0),
            'length': int(attrs.get('ljnnpokapig') or 10),
            'rigidity': int(attrs.get('lgjeahdobch') or f32_bits(0.4 + lb_of(name or item_id) / 150.0)),
        }
    db.close()
    return out


def patch_defs(db, defs):
    """补齐长度/刚性（历史补丁，供描述与物理估算使用）。"""
    changed = 0
    for nid, item_id, attrs_json in list(db.execute(
            'select numeric_id, item_id, attrs_json from item_def where category=57')):
        if item_id not in defs:
            continue
        attrs = json.loads(attrs_json or '{}')
        d = defs[item_id]
        if attrs.get('lgjeahdobch') == d['rigidity'] and attrs.get('ljnnpokapig') == d['length']:
            continue
        attrs['lgjeahdobch'] = d['rigidity']
        attrs['ljnnpokapig'] = d['length']
        db.execute('update item_def set attrs_json=? where numeric_id=?',
                   (json.dumps(attrs), nid))
        changed += 1
    return changed


def old_field(fields, idx):
    try:
        return fields[idx].get('v')
    except Exception:
        return None


def patch_items(db, defs):
    changed = 0
    rows = list(db.execute(
        "select guid, item_id, numeric_id, wire_class, qty, fields_json, durability, workmanship "
        "from item where substr(item_id,1,3)='lc_'"))
    for guid, item_id, numeric, wire_class, qty, fields_json, durability, workmanship in rows:
        if item_id not in defs or wire_class == TARGET_CLASS:
            continue
        d = defs[item_id]
        try:
            old = json.loads(fields_json or '[]')
        except Exception:
            old = []
        now = time.time()
        created = old_field(old, 9) or int((now + 62135596800) * 10_000_000)
        updated = old_field(old, 10) or int((now + 11644473600) * 10_000_000)
        # 旧钓线类的 f1 是强度；没有就用定义强度 × 耐久。
        strength = old_field(old, 1) or f32_bits(f32(d['strength']) * (durability or 1.0))
        state = old_field(old, 11) or d['state']
        fields = [
            {'f': 'u32', 'v': int(strength)},
            {'f': 'guid', 'g': guid},
            {'f': 'optguid', 'v': None},
            {'f': 'byte', 'v': 0},
            {'f': 'u32', 'v': int(numeric)},
            {'f': 'u32', 'v': f32_bits(workmanship or 1.0)},
            {'f': 'u32', 'v': f32_bits(durability or 1.0)},
            {'f': 'u64', 'v': int(created)},
            {'f': 'u64', 'v': int(updated)},
            {'f': 'u32', 'v': int(state)},
            {'f': 'val', 'v': {'t': 'null'}},
            {'f': 'val', 'v': {'t': 'null'}},
            {'f': 'u32', 'v': 0},
            {'f': 'u16', 'v': 0},
        ]
        db.execute('update item set wire_class=?, type_id=?, fields_json=? where guid=?',
                   (TARGET_CLASS, TARGET_TYPE, json.dumps(fields, separators=(',', ':')), guid))
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
    print('item_def 更新 %d 条，库存实例改写 %d 条（定义 %d 个）' % (d, i, len(defs)))


if __name__ == '__main__':
    main()

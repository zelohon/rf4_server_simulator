#!/usr/bin/env python
"""改写 config_wire 里的票时长（客户端本地配置）。

blob 结构：13 字节头（props + dict_size + 8 字节未知字段，原样保留）
           + raw LZMA1 数据；1/5 用 index.json 的 SHA1 做缓存校验。

用法：
  python config_patch.py set ticket_boat_komar_24 2
  python config_patch.py list ticket_boat_komar_24
  python config_patch.py restore            # 从 .orig 备份还原
"""
import hashlib
import json
import lzma
import os
import shutil
import sys

BASE = r"J:\code\rf4_server_simulator\internal\gameserver\assets\config_wire"
INDEX = os.path.join(BASE, "index.json")


def decode(raw: bytes) -> tuple[bytes, bytes, list]:
    props = raw[0]
    ds = int.from_bytes(raw[1:5], "little")
    filt = [{
        "id": lzma.FILTER_LZMA1,
        "dict_size": ds,
        "lc": props % 9,
        "lp": (props // 9) % 5,
        "pb": props // 45,
    }]
    data = lzma.LZMADecompressor(format=lzma.FORMAT_RAW, filters=filt).decompress(raw[13:])
    return data, raw[:13], filt


def encode(data: bytes, header: bytes, filt: list) -> bytes:
    return header + lzma.compress(data, format=lzma.FORMAT_RAW, filters=filt)


def find_records(data: bytes, name: str):
    needle = name.encode()
    out = []
    i = -1
    while True:
        i = data.find(needle, i + 1)
        if i < 0:
            return out
        if i >= 10:
            dur = int.from_bytes(data[i - 10:i - 6], "little")
            ident = int.from_bytes(data[i - 6:i - 2], "little")
            out.append((i, dur, ident))


def rewrite_type(typ: int, mutator) -> None:
    blob_path = os.path.join(BASE, f"type_{typ}.bin")
    orig_path = blob_path + ".orig"
    if not os.path.exists(orig_path):
        shutil.copy2(blob_path, orig_path)
    raw = open(orig_path, "rb").read()
    data, header, filt = decode(raw)
    new_data = mutator(data)
    if new_data is None or new_data == data:
        print("没有改动")
        return
    blob = encode(new_data, header, filt)
    open(blob_path, "wb").write(blob)
    idx = json.load(open(INDEX, encoding="utf-8"))
    key = str(typ)
    idx["types"][key]["sha1"] = hashlib.sha1(blob).hexdigest()
    idx["types"][key]["bytes"] = len(blob)
    versions = idx.get("versions") or []
    if len(versions) >= typ:
        versions[typ - 1] = idx["types"][key]["sha1"]
    idx["versions"] = versions
    json.dump(idx, open(INDEX, "w", encoding="utf-8"), indent=1)
    print(f"type_{typ}.bin 已更新：{len(raw)} -> {len(blob)} 字节，sha1={idx['types'][key]['sha1']}")


def main():
    if len(sys.argv) < 2:
        print(__doc__)
        return
    cmd = sys.argv[1]
    if cmd == "restore":
        for n in os.listdir(BASE):
            if n.endswith(".bin.orig"):
                shutil.copy2(os.path.join(BASE, n), os.path.join(BASE, n[:-5]))
                print("restored", n[:-5])
        return
    name = sys.argv[2]
    if cmd == "list":
        for typ in range(1, 10):
            p = os.path.join(BASE, f"type_{typ}.bin")
            if not os.path.exists(p):
                continue
            try:
                data, _, _ = decode(open(p, "rb").read())
            except Exception:
                continue
            for off, dur, ident in find_records(data, name):
                print(f"type_{typ}.bin offset={off} duration={dur} id={ident}")
        return
    if cmd == "set":
        want = int(sys.argv[3])
        done = []

        def mutator(data):
            hits = find_records(data, name)
            if not hits:
                return None
            buf = bytearray(data)
            for off, dur, _ in hits:
                # dur == 0 处不是时长字段（例如 type_2 的物品索引表），跳过
                if dur <= 0:
                    continue
                buf[off - 10:off - 6] = want.to_bytes(4, "little")
                done.append((off, dur))
            return bytes(buf)

        for typ in range(1, 10):
            p = os.path.join(BASE, f"type_{typ}.bin")
            if not os.path.exists(p):
                continue
            try:
                decode(open(p, "rb").read())
            except Exception:
                continue
            before = len(done)
            rewrite_type(typ, mutator)
            if len(done) > before:
                print(f"  {name}: {done[before:]}")
        return


if __name__ == "__main__":
    main()

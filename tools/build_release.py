#!/usr/bin/env python3
"""生成客户分发包（fish-localserver）。

用法：
    python build_release.py --name 客户A
    python build_release.py --name 客户A --days 365 --out D:\\release

产物：
    <out>\\fish-localserver-<客户名>-<日期>.zip

设计要点：
  * 不打包源数据库 —— 用 seed.exe 现场生成干净库（无测试账号/存档）
  * 票据只含卡密哈希（lic），不含卡密明文
  * 敏感配置（管理密码、JWT 密钥、隧道 PSK）每客户随机
  * advertise_host 留 127.0.0.1，由客户按说明改成自己的内网 IP
"""
import argparse
import datetime
import os
import re
import secrets
import shutil
import subprocess
import sys
import zipfile
from pathlib import Path

MIN_DIR = Path(r"C:\Users\edge\Desktop\fish-localserver-min")
GO_BIN = Path(r"D:\rf4_src\bin")
PRIVATE_KEY = Path(r"D:\license-private.key")
DEFAULT_CODE = "FSODA6E2C41A6E23688AFE42860AFDD1"

COPY_FILES = [
    "LICENSE",
    "config.yaml",
    "启动服务器.cmd",
]
COPY_DIRS = ["certs", "server_data"]
BINARIES = ["console.exe", "gateway.exe", "admin.exe", "gameserver.exe", "seed.exe"]

QUICKSTART = """fish-localserver 使用说明
================================

一、部署前必须改一个地方：服务器地址
------------------------------------
用记事本打开 config.yaml，找到这一行：

    advertise_host: "127.0.0.1"

把 127.0.0.1 改成这台服务器在局域网里的 IP，例如：

    advertise_host: "192.168.1.50"

查看本机 IP：按 Win+R 输入 cmd，执行 ipconfig，看"IPv4 地址"。

⚠ 这一项不改，登录能成功，但进入游戏会提示"未连接到游戏服务器"。

二、启动
--------
1. 右键「启动服务器.cmd」→ 以管理员身份运行（需要绑定 443 端口）
2. 控制台打开后，点右上角「启动全部」
3. 三个服务卡片变绿即为就绪

三、管理账号
------------
管理用户名：admin
管理密码：见「客户信息.txt」

登录后请立即修改密码。

四、客户端怎么连
----------------
客户端机器需要：
1. 安装 certs\\server.crt 证书（双击 → 安装证书 → 受信任的根证书颁发机构）
2. hosts 文件中加入：<服务器IP> api.rf4game.ru

主机文件位置：C:\\Windows\\System32\\drivers\\etc\\hosts

五、创建游戏账号
----------------
控制台 → 账号管理 → 新建账号。
账号建立后，用该账号密码在游戏客户端登录。

六、常见问题
------------
* 服务起不来、控制台显示"未连接"：确认是以管理员身份运行
* 登录成功但进不去游戏：检查第一步的 advertise_host
* 提示端口被占用：检查是否有其他程序占用 443 / 9341 / 8080
"""


def fail(msg):
    print(f"[失败] {msg}", file=sys.stderr)
    sys.exit(1)


def run(cmd, cwd):
    p = subprocess.run(cmd, cwd=str(cwd), capture_output=True, text=True, encoding="utf-8", errors="replace")
    return p.returncode, (p.stdout or "") + (p.stderr or "")


def patch_config(text, admin_pass):
    jwt = secrets.token_hex(24)
    psk = secrets.token_hex(16)

    def sub1(pattern, repl, label):
        new, n = re.subn(pattern, repl, text, count=1, flags=re.M)
        if n != 1:
            fail(f"配置项替换失败：{label}")
        return new

    text = sub1(r"^env:\s*dev", "env: prod", "env")
    text = sub1(r'(advertise_host:\s*)"[^"]*"', r'\g<1>"127.0.0.1"', "advertise_host")
    text = sub1(r'(jwt_secret:\s*)"[^"]*"', rf'\g<1>"{jwt}"', "jwt_secret")
    text = sub1(r'(password:\s*)"[^"]*"', rf'\g<1>"{admin_pass}"', "admin password")

    # 隧道 PSK 在 gateway 与 game 两处，都要换且保持一致
    text, n = re.subn(r'(psk:\s*)"[^"]*"', rf'\g<1>"{psk}"', text, flags=re.M)
    if n < 1:
        fail("隧道 PSK 替换失败")
    return text


def write_zip(pkg: Path, zip_path: Path):
    """显式写 zip：保留 fish-localserver/ 顶层目录，中文名用 UTF-8。"""
    base = pkg.parent
    with zipfile.ZipFile(zip_path, "w", zipfile.ZIP_DEFLATED, compresslevel=6) as z:
        for root, dirs, files in os.walk(pkg):
            dirs.sort()
            for d in dirs:
                full = Path(root) / d
                z.writestr(str(full.relative_to(base)).replace("\\", "/") + "/", "")
            for f in sorted(files):
                full = Path(root) / f
                z.write(full, str(full.relative_to(base)).replace("\\", "/"))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--name", required=True, help="客户名，用于 zip 文件名和票据备注")
    ap.add_argument("--days", type=int, default=180, help="授权天数，默认 180")
    ap.add_argument("--code", default=DEFAULT_CODE, help="卡密（仅用于生成票据标识）")
    ap.add_argument("--out", default=r"D:\release", help="输出目录")
    ap.add_argument("--admin-pass", default="", help="管理密码，留空则随机生成")
    args = ap.parse_args()

    name = args.name.strip()
    if not name or re.search(r'[\\/:*?"<>|]', name):
        fail("客户名不能为空，也不能包含 \\ / : * ? \" < > |")

    if not MIN_DIR.is_dir():
        fail(f"找不到基准目录 {MIN_DIR}")
    if not PRIVATE_KEY.exists():
        fail(f"找不到私钥 {PRIVATE_KEY}")

    out_dir = Path(args.out)
    out_dir.mkdir(parents=True, exist_ok=True)
    today = datetime.date.today().strftime("%Y%m%d")

    admin_pass = args.admin_pass or ("Rf4-" + secrets.token_urlsafe(9))
    work = out_dir / "_build" / name
    if work.exists():
        shutil.rmtree(work)
    pkg = work / "fish-localserver"
    (pkg / "bin").mkdir(parents=True)
    (pkg / "data").mkdir()

    print(f"[1/6] 复制程序文件 → {pkg}")
    for f in COPY_FILES:
        src = MIN_DIR / f
        if not src.exists():
            fail(f"缺少 {f}")
        shutil.copy2(src, pkg / f)
    for d in COPY_DIRS:
        shutil.copytree(MIN_DIR / d, pkg / d)
    for b in BINARIES:
        src = MIN_DIR / "bin" / b
        if not src.exists():
            fail(f"缺少 bin/{b}")
        shutil.copy2(src, pkg / "bin" / b)

    print("[2/6] 改写配置（关闭调试、随机密钥、IP 待客户填写）")
    with open(pkg / "config.yaml", encoding="utf-8", newline="") as f:
        cfg = f.read()
    with open(pkg / "config.yaml", "w", encoding="utf-8", newline="") as f:
        f.write(patch_config(cfg, admin_pass))

    print("[3/6] 生成干净数据库（无测试账号与存档）")
    code, out = run(
        [str(pkg / "bin" / "seed.exe"), "-config", "config.yaml",
         "-static-db", "server_data/static.sqlite3",
         "-init-admin", "admin", "-init-admin-pass", admin_pass],
        pkg,
    )
    if code != 0 or "seed 完成" not in out:
        fail(f"seed 初始化失败：\n{out[-800:]}")

    print("[4/6] 签发票据（不含卡密明文）")
    code, out = run(
        [str(GO_BIN / "license-gen.exe"), "-key", str(PRIVATE_KEY),
         "-code", args.code, "-days", str(args.days),
         "-note", name, "-out", str(pkg / "data" / "auth.ticket")],
        pkg,
    )
    if code != 0:
        fail(f"票据签发失败：{out[-400:]}")
    lic = re.search(r"卡密标识:\s*(\w+)", out)
    expire = re.search(r"到期:\s*(.+)", out)

    print("[5/6] 写入说明文件")
    for fname, content in (
        ("快速开始.txt", QUICKSTART),
        ("客户信息.txt", f"""部署信息
================================
客户名称：{name}
管理员账号：admin
管理员密码：{admin_pass}

授权到期：{expire.group(1).strip() if expire else '见票据'}
卡密标识：{lic.group(1) if lic else '-'}（仅哈希，非卡密）

请妥善保存本文件，登录管理后台后立即修改密码。
"""),
    ):
        with open(pkg / fname, "w", encoding="utf-8", newline="\r\n") as f:
            f.write(content)

    print("[6/6] 打包 zip")
    zip_base = out_dir / f"fish-localserver-{name}-{today}"
    zip_path = zip_base.with_suffix(".zip")
    write_zip(pkg, zip_path)
    size_mb = zip_path.stat().st_size / 1024 / 1024
    shutil.rmtree(work, ignore_errors=True)

    print("\n完成")
    print(f"  包文件：{zip_path}  ({size_mb:.1f} MB)")
    print(f"  管理密码：{admin_pass}")
    print(f"  授权到期：{expire.group(1).strip() if expire else '-'}")
    print(f"  卡密标识：{lic.group(1) if lic else '-'}")


if __name__ == "__main__":
    main()

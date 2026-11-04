#!/usr/bin/env bash
set -euo pipefail

COMMIT_SHA="${1:-}"
if [[ ! "${COMMIT_SHA}" =~ ^[0-9a-f]{40}$ ]]; then
  echo "usage: $0 <40-character-commit-sha>" >&2
  exit 2
fi

SHORT_SHA="${COMMIT_SHA:0:7}"
BUILD_TS="$(date -u +%Y%m%dT%H%M%SZ)"
VERSION="preview-${BUILD_TS}-${SHORT_SHA}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT}"
export CGO_ENABLED=0

DIST="${ROOT}/dist/preview/${VERSION}"
PACKAGE_NAME="rf4_server_simulator-${VERSION}-windows-amd64"
STAGE="${DIST}/${PACKAGE_NAME}"
ASSET_NAME="${PACKAGE_NAME}.zip"
ASSET_PATH="${DIST}/${ASSET_NAME}"
RELEASE_NOTES="${DIST}/release-notes.md"

rm -rf "${DIST}"
mkdir -p "${STAGE}/bin" "${STAGE}/certs"

echo "Building ${VERSION} from ${COMMIT_SHA}"
LDFLAGS="-s -w -X 'rf4server/internal/buildinfo.Version=${VERSION}'"
for command in gateway gameserver seed; do
  GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "${LDFLAGS}" \
    -o "${STAGE}/bin/${command}.exe" "./cmd/${command}"
done
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "${LDFLAGS} -H=windowsgui" \
  -o "${STAGE}/bin/launcher.exe" ./cmd/launcher

openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -sha256 -nodes \
  -keyout "${STAGE}/certs/server.key" -out "${STAGE}/certs/server.crt" \
  -days 3650 -subj "/CN=api.rf4game.ru" \
  -addext "subjectAltName=DNS:api.rf4game.ru,DNS:localhost,IP:127.0.0.1" >/dev/null 2>&1
chmod 600 "${STAGE}/certs/server.key"

sed \
  -e 's/replace-with-shared-random-secret/local-development-tunnel-psk/g' \
  -e 's/replace-with-long-random-secret/local-development-jwt-secret/g' \
  -e 's/replace-with-strong-password/admin123/g' \
  config.example.yaml > "${STAGE}/config.yaml"

cp README.md LICENSE config.example.yaml "${STAGE}/"
cp -R docs server_data "${STAGE}/"

cat > "${STAGE}/快速开始.txt" <<EOF
rf4_server_simulator ${VERSION} 快速开始

Commit: ${COMMIT_SHA}
支持客户端版本：4.0.25034

1. 安装证书
   双击 certs\server.crt，选择“安装证书”。
   证书存储位置选择“当前用户”或“本地计算机”。
   将证书放入“受信任的根证书颁发机构”。
   本证书仅用于本机测试，请勿用于公网服务。

2. 修改 hosts
   以管理员权限编辑：
   C:\Windows\System32\drivers\etc\hosts
   添加：
   127.0.0.1 api.rf4game.ru
   127.0.0.1 api.rf4game.com

3. 启动
   双击“启动服务器.cmd”，或运行 bin\launcher.exe。
   统一控制中心：http://127.0.0.1:9500（运行控制 + 管理后台）
   初始管理员：admin / admin123

4. 注意
   - 首次启动会自动创建 data 目录并导入静态数据。
   - 不要上传或分享 config.yaml、certs、data。
   - Preview 版本基于 main 分支当前提交构建，仅供测试。
EOF

printf '@echo off\r\ncd /d "%%~dp0"\r\nstart "" "bin\\launcher.exe"\r\n' > "${STAGE}/启动服务器.cmd"

cat > "${RELEASE_NOTES}" <<EOF
# rf4_server_simulator ${VERSION}

- 分支：main
- Commit：${COMMIT_SHA}
- 短 Commit ID：${SHORT_SHA}
- 构建时间（UTC）：${BUILD_TS}
- 构建方式：GitHub Actions

## 包内内容

- Windows x64 可执行程序与 launcher
- config.yaml 和 config.example.yaml
- server_data 静态资产
- certs 本地开发证书
- README、LICENSE、docs
- 快速开始.txt、启动服务器.cmd

## 问题反馈

- QQ 群：379233540
- GitHub Issues：https://github.com/glance522/rf4_server_simulator/issues

## 最近提交

$(git log -20 --format='- @%h %s')
EOF
cp "${RELEASE_NOTES}" "${STAGE}/发布说明.md"

if [[ -e "${STAGE}/bin/admin.exe" ]]; then
  echo "refusing to package standalone admin.exe; admin is embedded in launcher" >&2
  exit 1
fi

if find "${STAGE}" -type f \( -name 'rf4server.db' -o -name '*.db-wal' -o -name '*.db-shm' \) | grep -q .; then
  echo "refusing to package runtime save files" >&2
  exit 1
fi

(cd "${DIST}" && zip -qr "${ASSET_NAME}" "${PACKAGE_NAME}")
ASSET_SHA256="$(sha256sum "${ASSET_PATH}" | awk '{print $1}')"

if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  {
    echo "version=${VERSION}"
    echo "asset_path=${ASSET_PATH}"
    echo "asset_name=${ASSET_NAME}"
    echo "asset_sha256=${ASSET_SHA256}"
    echo "release_notes=${RELEASE_NOTES}"
  } >> "${GITHUB_OUTPUT}"
fi

echo "PACKAGE=${ASSET_PATH}"
echo "SHA256=${ASSET_SHA256}"
#!/usr/bin/env bash
set -euo pipefail

# Builda o discovery-agent.exe em servidor Linux (Ubuntu) via cross-compile.
# Opcionalmente gera installer.json ao lado do exe para bootstrap de conexao.
# Este script e dedicado ao build do servidor/API e nao faz parte do fluxo de release do GitHub Actions.

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SRC_ROOT="$PROJECT_ROOT/src"
OUT_DIR="$SRC_ROOT/build/bin"
OUTPUT_NAME="discovery-agent.exe"
VERSION=""
SERVER_URL=""
API_KEY=""
DISCOVERY_ENABLED="1"
WRITE_INSTALLER_JSON="1"

usage() {
  cat <<'EOF'
Uso:
  build/server-api/linux/build-agent-server-linux.sh [opcoes]

Opcoes:
  --project-root <path>          Raiz do repositorio (default: auto)
  --out-dir <path>               Diretorio de saida do exe (default: src/build/bin)
  --output-name <name>           Nome do executavel (default: discovery-agent.exe)
  --version <semver>             Injeta versao em ldflags (ex: 1.2.3)
  --server-url <url>             Server URL para gerar installer.json
  --api-key <token>              API key para gerar installer.json
  --auto-provisioning <0|1>      autoProvisioning no installer.json (default: 1) (alias: --discovery-enabled)
  --write-installer-json <0|1>   Gera installer.json (default: 1)
  --help                         Mostra esta ajuda

Dependencias (Ubuntu):
  - go
  - x86_64-w64-mingw32-gcc
  - x86_64-w64-mingw32-windres

Exemplo:
  ./build/server-api/linux/build-agent-server-linux.sh \
    --version 1.4.0 \
    --server-url https://api.seu-servidor.com \
    --api-key mdz_xxxxx
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --project-root)
      PROJECT_ROOT="$2"
      SRC_ROOT="$PROJECT_ROOT/src"
      shift 2
      ;;
    --out-dir)
      OUT_DIR="$2"
      shift 2
      ;;
    --output-name)
      OUTPUT_NAME="$2"
      shift 2
      ;;
    --version)
      VERSION="$2"
      shift 2
      ;;
    --server-url)
      SERVER_URL="$2"
      shift 2
      ;;
    --api-key)
      API_KEY="$2"
      shift 2
      ;;
    --auto-provisioning|--discovery-enabled)
      DISCOVERY_ENABLED="$2"
      shift 2
      ;;
    --write-installer-json)
      WRITE_INSTALLER_JSON="$2"
      shift 2
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      echo "[erro] opcao desconhecida: $1" >&2
      usage
      exit 1
      ;;
  esac
done

if [[ "$DISCOVERY_ENABLED" != "0" && "$DISCOVERY_ENABLED" != "1" ]]; then
  echo "[erro] --auto-provisioning deve ser 0 ou 1" >&2
  exit 1
fi

if [[ "$WRITE_INSTALLER_JSON" != "0" && "$WRITE_INSTALLER_JSON" != "1" ]]; then
  echo "[erro] --write-installer-json deve ser 0 ou 1" >&2
  exit 1
fi

if ! command -v go >/dev/null 2>&1; then
  echo "[erro] go nao encontrado no PATH" >&2
  exit 1
fi

if ! command -v x86_64-w64-mingw32-gcc >/dev/null 2>&1; then
  echo "[erro] x86_64-w64-mingw32-gcc nao encontrado no PATH" >&2
  echo "[dica] sudo apt-get install -y gcc-mingw-w64-x86-64" >&2
  exit 1
fi

if ! command -v x86_64-w64-mingw32-windres >/dev/null 2>&1; then
  echo "[erro] x86_64-w64-mingw32-windres nao encontrado no PATH" >&2
  echo "[dica] sudo apt-get install -y binutils-mingw-w64-x86-64" >&2
  exit 1
fi

ICON_PATH="$SRC_ROOT/build/windows/icon.ico"
if [[ ! -f "$ICON_PATH" ]]; then
  echo "[erro] icon.ico nao encontrado em $ICON_PATH" >&2
  echo "[dica] gere/sincronize o icon.ico antes do build" >&2
  exit 1
fi

mkdir -p "$OUT_DIR"

TMP_RC="$(mktemp)"
TMP_SYSO="$SRC_ROOT/resource_windows_amd64.syso"

cleanup() {
  rm -f "$TMP_RC" "$TMP_SYSO"
}
trap cleanup EXIT

ICON_PATH_UNIX="${ICON_PATH//\\//}"
printf 'IDI_APP_ICON ICON "%s"\n' "$ICON_PATH_UNIX" > "$TMP_RC"

echo "[1/3] Gerando recurso de icone (.syso)..."
x86_64-w64-mingw32-windres --target=pe-x86-64 -i "$TMP_RC" -o "$TMP_SYSO"

echo "[2/3] Build do discovery-agent.exe (windows/amd64)..."
pushd "$SRC_ROOT" >/dev/null
export CGO_ENABLED=1
export GOOS=windows
export GOARCH=amd64
export CC=x86_64-w64-mingw32-gcc

LDFLAGS="-H=windowsgui"
if [[ -n "$VERSION" ]]; then
  LDFLAGS+=" -X discovery/app.Version=$VERSION"
  LDFLAGS+=" -X discovery/internal/buildinfo.Version=$VERSION"
fi

go build -tags "desktop,production" -ldflags "$LDFLAGS" -o "$OUT_DIR/$OUTPUT_NAME" .
popd >/dev/null

echo "[3/3] Finalizando artefatos..."

if [[ "$WRITE_INSTALLER_JSON" == "1" ]]; then
  INSTALLER_JSON_PATH="$OUT_DIR/installer.json"

  if [[ -n "$SERVER_URL" || -n "$API_KEY" ]]; then
    cat > "$INSTALLER_JSON_PATH" <<EOF
{
  "serverUrl": "${SERVER_URL}",
  "apiKey": "${API_KEY}",
  "autoProvisioning": $([[ "$DISCOVERY_ENABLED" == "1" ]] && echo true || echo false),
  "p2p": {
    "enabled": $([[ "$DISCOVERY_ENABLED" == "1" ]] && echo true || echo false)
  }
}
EOF
    echo "installer.json gerado em: $INSTALLER_JSON_PATH"
  else
    echo "[aviso] server-url/api-key nao informados; installer.json nao foi gerado"
  fi
fi

echo "Build concluido: $OUT_DIR/$OUTPUT_NAME"

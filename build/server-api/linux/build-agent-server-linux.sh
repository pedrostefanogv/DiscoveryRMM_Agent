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
  --version <semver>             Injeta versao em ldflags (ex: 1.2.3). Se omitido, detecta automaticamente do src/build/config.yml (info.version)
  --server-url <url>             Server URL para gerar installer.json
  --api-key <token>              API key para gerar installer.json
  --auto-provisioning <0|1>      autoProvisioning no installer.json (default: 1) (alias: --discovery-enabled)
  --write-installer-json <0|1>   Gera installer.json (default: 1)
  --help                         Mostra esta ajuda

Dependencias (Ubuntu):
  - go
  - x86_64-w64-mingw32-gcc
  - x86_64-w64-mingw32-windres
  - wails3 (opcional — auto-instalado via go install se ausente; usado para regenerar bindings)

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

# Auto-detect version from build/config.yml (Wails v3 schema: info.version).
# Fallback legado: src/wails.json (v2: info.productVersion) — removido na migracao v3.
if [[ -z "$VERSION" ]]; then
  CONFIG_YML="$SRC_ROOT/build/config.yml"
  if [[ -f "$CONFIG_YML" ]]; then
    # Parse simples com python3: extrai info.version do YAML.
    # O config.yml do Wails v3 usa `info:\n  version: "1.2.0"`.
    VERSION=$(python3 -c "
import sys, re
with open(sys.argv[1], encoding='utf-8') as f:
    content = f.read()
m = re.search(r'(?ms)^\s*info:\s*\n(?:.*\n)*?\s+version:\s*[\"\\']?([^\"\\'\\s#]+)[\"\\']?', content)
print(m.group(1) if m else '')
" "$CONFIG_YML" 2>/dev/null) || true
    if [[ -n "$VERSION" ]]; then
      echo "[info] versao detectada do build/config.yml: $VERSION"
    fi
  fi
  if [[ -z "$VERSION" ]]; then
    echo "[aviso] versao nao definida e nao detectada do config.yml; binario ficara com buildinfo.Version='0.0.0' (self-update loop pode ocorrer)"
  fi
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
  echo "[dica] sudo apt-get install -y binutils-mingw-w64-x-64" >&2
  exit 1
fi

# ── Wails v3 CLI (wails3) ──
# O build Linux usa `go build` direto (cross-compile com MinGW), mas o wails3
# é necessário para regenerar bindings (frontend/bindings) e assets quando há
# mudanças na API Go exposta ao frontend. Se não estiver instalado, instala
# automaticamente via `go install`.
WAILS3_BIN="${WAILS3_BIN:-$(command -v wails3 2>/dev/null) || true}"
if [[ -z "$WAILS3_BIN" ]]; then
  echo "[info] wails3 nao encontrado no PATH; instalando via go install..."
  # GOBIN defaults to $GOPATH/bin or $HOME/go/bin; garantimos que fica acessível.
  if ! go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.3 2>/dev/null; then
    echo "[aviso] falha ao instalar wails3; continuando (bindings nao serao regenerados)"
    echo "[dica] instale manualmente: go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.3"
  else
    # Resolve o caminho do binário instalado (GOBIN ou GOPATH/bin).
    WAILS3_BIN="$(go env GOBIN 2>/dev/null)"
    if [[ -z "$WAILS3_BIN" ]]; then
      WAILS3_BIN="$(go env GOPATH 2>/dev/null)/bin/wails3"
    fi
    if [[ -f "$WAILS3_BIN" ]]; then
      echo "[info] wails3 instalado em: $WAILS3_BIN"
      export PATH="$(dirname "$WAILS3_BIN"):$PATH"
      WAILS3_BIN="$WAILS3_BIN"
    else
      echo "[aviso] wails3 instalado mas binario nao encontrado; continuando sem regenerar bindings"
      WAILS3_BIN=""
    fi
  fi
fi

# Regenera bindings do frontend se wails3 estiver disponível e houver mudanças
# na API Go (detecção simples: compara timestamp dos bindings com arquivos .go).
if [[ -n "$WAILS3_BIN" ]]; then
  BINDINGS_DIR="$SRC_ROOT/frontend/bindings"
  NEED_REGEN=0
  if [[ ! -d "$BINDINGS_DIR" ]]; then
    NEED_REGEN=1
  else
    # Verifica se algum .go em app/ é mais recente que os bindings gerados.
    NEWEST_GO=$(find "$SRC_ROOT/app" -name '*.go' -newer "$BINDINGS_DIR" -print -quit 2>/dev/null)
    if [[ -n "$NEWEST_GO" ]]; then
      NEED_REGEN=1
    fi
  fi
  if [[ "$NEED_REGEN" -eq 1 ]]; then
    echo "[info] regenerando bindings do frontend com wails3..."
    pushd "$SRC_ROOT" >/dev/null
    "$WAILS3_BIN" generate bindings -b -clean=true -d frontend/bindings ./... || {
      echo "[aviso] falha ao regenerar bindings; continuando com bindings existentes"
    }
    popd >/dev/null
  fi
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
  LDFLAGS+=" -X discovery/app/core/buildinfo.Version=$VERSION"
fi

# Resolve commit hash (short) for buildinfo.Commit and agent-version.json
COMMIT="unknown"
if GIT_COMMIT=$(git -C "$PROJECT_ROOT" rev-parse --short=8 HEAD 2>/dev/null); then
  COMMIT="$GIT_COMMIT"
  LDFLAGS+=" -X discovery/app/core/buildinfo.Commit=$COMMIT"
  echo "buildinfo.Commit = $COMMIT"
else
  echo "[aviso] git rev-parse falhou; buildinfo.Commit ficara 'unknown'"
fi

go build -tags "desktop,production" -ldflags "$LDFLAGS" -o "$OUT_DIR/$OUTPUT_NAME" .
popd >/dev/null

echo "[3/3] Finalizando artefatos..."

# Generate agent-version.json for API post-build commit resolution
if [[ -n "$VERSION" ]]; then
  VER_JSON="$OUT_DIR/agent-version.json"
  cat > "$VER_JSON" <<EOF
{
  "version": "$VERSION",
  "commitHash": "$COMMIT"
}
EOF
  echo "agent-version.json gerado em: $VER_JSON"
fi

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

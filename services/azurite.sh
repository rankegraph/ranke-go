#!/usr/bin/env bash
# services/azurite.sh — an easy, ephemeral Azure Blob service (Azurite, Microsoft's
# storage emulator) for the azure matrix row / perf suite, in one of two modes:
#
#   pod      run Azurite in a podman pod (needs podman); mirrors the redis/neo4j pods.
#   native   run Azurite from npm directly in this container (no podman, no root);
#            reached at 127.0.0.1 — use this where there is no podman.
#
# Both serve the emulator's well-known account (devstoreaccount1) the tests expect,
# wait until it answers, and print the RANKE_AZURE_* env to point the tests at it.
# The tests create their own blob container per run, so this only has to serve.
#
# Usage:
#   services/azurite.sh pod    {up|down|status|env}
#   services/azurite.sh native {up|down|status|env|purge}
#
# Shared overrides: RANKE_AZURE_{ACCOUNT,KEY,PORT,READY_TIMEOUT}.
# Pod:    RANKE_AZURE_{NAME,IMAGE}.   Native: RANKE_AZURE_{DIR,VERSION}.
set -euo pipefail

# ── shared config ──────────────────────────────────────────────────────
# The emulator's account and key, published by Microsoft and served by every
# Azurite instance; the tests default to the same pair.
ACCOUNT="${RANKE_AZURE_ACCOUNT:-devstoreaccount1}"
KEY="${RANKE_AZURE_KEY:-Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==}"
PORT="${RANKE_AZURE_PORT:-10000}"
READY_TIMEOUT="${RANKE_AZURE_READY_TIMEOUT:-60}"
ENDPOINT="http://127.0.0.1:${PORT}/${ACCOUNT}"

test_env_hint() {
  cat <<EOF

  Point the tests at it:
    RANKE_AZURE_ENDPOINT=${ENDPOINT} \\
      go test ./tests/matrix/ -run TestMatrix -v
EOF
}

# is_serving asks for a listing without a signature: 403 is Azurite answering, and
# anything it answers means the service is up. A dead port gives curl nothing.
is_serving() {
  local code
  code="$(curl -s -o /dev/null -w '%{http_code}' "${ENDPOINT}?comp=list" 2>/dev/null || true)"
  [ -n "$code" ] && [ "$code" != "000" ]
}

wait_ready() {
  local deadline=$(( SECONDS + READY_TIMEOUT ))
  echo "waiting for Azurite to serve (up to ${READY_TIMEOUT}s)..."
  while [ "$SECONDS" -lt "$deadline" ]; do
    if is_serving; then
      echo "Azurite is serving."
      return 0
    fi
    sleep 1
  done
  echo "error: Azurite did not become ready within ${READY_TIMEOUT}s" >&2
  return 1
}

# ── pod mode ───────────────────────────────────────────────────────────
POD_NAME="${RANKE_AZURE_NAME:-ranke-azurite}"
POD_IMAGE="${RANKE_AZURE_IMAGE:-mcr.microsoft.com/azure-storage/azurite:latest}"

pod_need_podman() { command -v podman >/dev/null 2>&1 || { echo "error: podman not on PATH" >&2; exit 1; }; }
pod_is_running()  { [ "$(podman inspect -f '{{.State.Running}}' "$POD_NAME" 2>/dev/null)" = "true" ]; }

pod_print_env() {
  cat <<EOF

  Azurite pod '${POD_NAME}' is up.  account: ${ACCOUNT}
  Reach it from the host:  ${ENDPOINT}
EOF
  test_env_hint
}

pod_up() {
  pod_need_podman
  if pod_is_running; then
    echo "Azurite '${POD_NAME}' already running; reusing it."
  else
    podman rm -f "$POD_NAME" >/dev/null 2>&1 || true
    echo "starting Azurite '${POD_NAME}'..."
    podman run -d --rm --name "$POD_NAME" -p "127.0.0.1:${PORT}:10000" \
      "$POD_IMAGE" azurite-blob --blobHost 0.0.0.0 --blobPort 10000 >/dev/null
    wait_ready
  fi
  pod_print_env
}

pod_down()   { pod_need_podman; podman rm -f "$POD_NAME" >/dev/null 2>&1 && echo "removed '${POD_NAME}'." || echo "'${POD_NAME}' was not running."; }
pod_status() { pod_need_podman; pod_is_running && echo "'${POD_NAME}' running on ${ENDPOINT}" || echo "'${POD_NAME}' not running"; }
pod_env()    { pod_need_podman; pod_is_running && pod_print_env || { echo "'${POD_NAME}' not running" >&2; exit 1; }; }

# ── native mode ────────────────────────────────────────────────────────
NAT_DIR="${RANKE_AZURE_DIR:-$HOME/.ranke-azurite}"
NAT_VERSION="${RANKE_AZURE_VERSION:-3.37.0}"
DATA_DIR="$NAT_DIR/data"
PIDFILE="$NAT_DIR/azurite.pid"
LOGFILE="$NAT_DIR/azurite.log"
BIN="$NAT_DIR/node_modules/.bin/azurite-blob"

nat_ensure_azurite() {
  [ -x "$BIN" ] && return 0
  command -v npm >/dev/null 2>&1 || { echo "error: npm not on PATH (Azurite ships as an npm package)" >&2; exit 1; }
  echo "installing azurite@${NAT_VERSION} from npm..."
  mkdir -p "$NAT_DIR"
  ( cd "$NAT_DIR" && npm install --no-fund --no-audit "azurite@${NAT_VERSION}" >/dev/null )
  echo "installed into ${NAT_DIR}"
}

nat_print_env() {
  cat <<EOF

  Azurite (native, in-container) is up.  account: ${ACCOUNT}
  Reach it (same container → localhost):  ${ENDPOINT}
  Install dir: ${NAT_DIR}
EOF
  test_env_hint
}

nat_up() {
  nat_ensure_azurite
  mkdir -p "$DATA_DIR"
  if is_serving; then
    echo "Azurite already serving on ${ENDPOINT}; reusing it."
  else
    echo "starting Azurite..."
    nohup "$BIN" --blobHost 127.0.0.1 --blobPort "$PORT" --location "$DATA_DIR" \
      >"$LOGFILE" 2>&1 &
    echo $! >"$PIDFILE"
    wait_ready
  fi
  nat_print_env
}

nat_down() {
  if [ -f "$PIDFILE" ] && kill "$(cat "$PIDFILE")" 2>/dev/null; then
    rm -f "$PIDFILE"
    echo "stopped."
  else
    rm -f "$PIDFILE"
    echo "not running."
  fi
}
nat_status() { is_serving && echo "running — ${ENDPOINT}" || echo "not running"; }
nat_env()    { is_serving && nat_print_env || { echo "not running" >&2; exit 1; }; }
nat_purge()  { nat_down || true; rm -rf "$NAT_DIR"; echo "purged ${NAT_DIR}"; }

# ── dispatch ───────────────────────────────────────────────────────────
usage() {
  cat <<EOF
usage: $0 <pod|native> <command>

  pod     run Azurite in a podman pod (needs podman)
            up | down | status | env
  native  run Azurite from npm in this container (no podman/root)
            up | down | status | env | purge
EOF
}

mode="${1:-}"
cmd="${2:-up}"
case "$mode" in
  pod)
    case "$cmd" in
      up) pod_up ;; down) pod_down ;; status) pod_status ;; env) pod_env ;;
      *) usage; exit 2 ;;
    esac ;;
  native)
    case "$cmd" in
      up) nat_up ;; down) nat_down ;; status) nat_status ;; env) nat_env ;; purge) nat_purge ;;
      *) usage; exit 2 ;;
    esac ;;
  ""|-h|--help|help) usage ;;
  *) echo "unknown mode: '${mode}'" >&2; usage; exit 2 ;;
esac

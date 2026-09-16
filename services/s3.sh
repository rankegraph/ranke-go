#!/usr/bin/env bash
# services/s3.sh — an easy, ephemeral S3 object store (MinIO) for the s3 matrix
# row / perf suite, in one of two modes:
#
#   pod      run MinIO in a podman pod (needs podman); mirrors the redis/neo4j pods.
#   native   run the MinIO binary directly in this container (no podman, no root);
#            reached at 127.0.0.1 — use this where there is no podman.
#
# Both start MinIO with the credentials the tests expect, wait until it serves,
# and print the RANKE_S3_* env to point the tests at it. The tests create their
# own bucket per run, so this only has to serve.
#
# Usage:
#   services/s3.sh pod    {up|down|status|env}
#   services/s3.sh native {up|down|status|env|purge}
#
# Shared overrides: RANKE_S3_{KEY,SECRET,PORT,READY_TIMEOUT}.
# Pod:    RANKE_S3_{NAME,IMAGE}.   Native: RANKE_S3_{DIR,CONSOLE_PORT}.
set -euo pipefail

# ── shared config ──────────────────────────────────────────────────────
KEY="${RANKE_S3_KEY:-minioadmin}"       # matches the test default
SECRET="${RANKE_S3_SECRET:-minioadmin}" # matches the test default
PORT="${RANKE_S3_PORT:-9000}"
READY_TIMEOUT="${RANKE_S3_READY_TIMEOUT:-60}"
ENDPOINT="http://127.0.0.1:${PORT}"

test_env_hint() {
  cat <<EOF

  Point the tests at it:
    RANKE_S3_ENDPOINT=${ENDPOINT} RANKE_S3_KEY=${KEY} RANKE_S3_SECRET=${SECRET} \\
      go test ./tests/matrix/ -run TestMatrix -v
EOF
}

# wait_ready polls MinIO's readiness endpoint until it answers.
wait_ready() {
  local deadline=$(( SECONDS + READY_TIMEOUT ))
  echo "waiting for MinIO to serve (up to ${READY_TIMEOUT}s)..."
  while [ "$SECONDS" -lt "$deadline" ]; do
    if curl -sf -o /dev/null "${ENDPOINT}/minio/health/ready"; then
      echo "MinIO is serving."
      return 0
    fi
    sleep 1
  done
  echo "error: MinIO did not become ready within ${READY_TIMEOUT}s" >&2
  return 1
}

is_serving() { curl -sf -o /dev/null "${ENDPOINT}/minio/health/ready"; }

# ── pod mode ───────────────────────────────────────────────────────────
POD_NAME="${RANKE_S3_NAME:-ranke-minio}"
# quay.io is where MinIO is published; docker.io/minio/minio answers 404.
POD_IMAGE="${RANKE_S3_IMAGE:-quay.io/minio/minio:latest}"

pod_need_podman() { command -v podman >/dev/null 2>&1 || { echo "error: podman not on PATH" >&2; exit 1; }; }
pod_is_running()  { [ "$(podman inspect -f '{{.State.Running}}' "$POD_NAME" 2>/dev/null)" = "true" ]; }

pod_print_env() {
  cat <<EOF

  MinIO pod '${POD_NAME}' is up.  key: ${KEY}   secret: ${SECRET}
  Reach it from the host:  ${ENDPOINT}
EOF
  test_env_hint
}

pod_up() {
  pod_need_podman
  if pod_is_running; then
    echo "MinIO '${POD_NAME}' already running; reusing it."
  else
    podman rm -f "$POD_NAME" >/dev/null 2>&1 || true
    echo "starting MinIO '${POD_NAME}'..."
    podman run -d --rm --name "$POD_NAME" -p "127.0.0.1:${PORT}:9000" \
      -e "MINIO_ROOT_USER=${KEY}" -e "MINIO_ROOT_PASSWORD=${SECRET}" \
      "$POD_IMAGE" server /data >/dev/null
    wait_ready
  fi
  pod_print_env
}

pod_down()   { pod_need_podman; podman rm -f "$POD_NAME" >/dev/null 2>&1 && echo "removed '${POD_NAME}'." || echo "'${POD_NAME}' was not running."; }
pod_status() { pod_need_podman; pod_is_running && echo "'${POD_NAME}' running on ${ENDPOINT}" || echo "'${POD_NAME}' not running"; }
pod_env()    { pod_need_podman; pod_is_running && pod_print_env || { echo "'${POD_NAME}' not running" >&2; exit 1; }; }

# ── native mode ────────────────────────────────────────────────────────
NAT_DIR="${RANKE_S3_DIR:-$HOME/.ranke-minio}"
CONSOLE_PORT="${RANKE_S3_CONSOLE_PORT:-9001}"
DATA_DIR="$NAT_DIR/data"
PIDFILE="$NAT_DIR/minio.pid"
LOGFILE="$NAT_DIR/minio.log"
BIN="$NAT_DIR/minio"
NAT_URL="https://dl.min.io/server/minio/release/linux-amd64/minio"

nat_ensure_minio() {
  [ -x "$BIN" ] && return 0
  echo "downloading the MinIO server binary (~110MB)..."
  mkdir -p "$NAT_DIR"
  curl -fSL "$NAT_URL" -o "$BIN"
  chmod +x "$BIN"
  echo "installed $("$BIN" --version | head -1) into ${NAT_DIR}"
}

nat_print_env() {
  cat <<EOF

  MinIO (native, in-container) is up.  key: ${KEY}   secret: ${SECRET}
  Reach it (same container → localhost):  ${ENDPOINT}   console: http://127.0.0.1:${CONSOLE_PORT}
  Install dir: ${NAT_DIR}
EOF
  test_env_hint
}

nat_up() {
  nat_ensure_minio
  mkdir -p "$DATA_DIR"
  if is_serving; then
    echo "MinIO already serving on ${ENDPOINT}; reusing it."
  else
    echo "starting MinIO..."
    MINIO_ROOT_USER="$KEY" MINIO_ROOT_PASSWORD="$SECRET" \
      nohup "$BIN" server "$DATA_DIR" \
      --address "127.0.0.1:${PORT}" --console-address "127.0.0.1:${CONSOLE_PORT}" \
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

  pod     run MinIO in a podman pod (needs podman)
            up | down | status | env
  native  run the MinIO binary in this container (no podman/root)
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

#!/usr/bin/env bash
#
# Start the lazy-vllm Windows monitoring stack from Git Bash:
#   1. Docker Compose exporters  (docker-compose.win.yml, detached)
#   2. The native Core Temp Prometheus exporter, in the background
#
# Idempotent: re-running will not start a second exporter.
# Usage:   ./scripts/start-monitoring.sh [PORT]   (default PORT=9184)
#
# One-time firewall rule so Prometheus can scrape it over the network
# (run once in an elevated shell):
#   netsh advfirewall firewall add rule name=coretemp-exporter \
#     dir=in action=allow protocol=TCP localport=9184

set -euo pipefail

PORT="${1:-9184}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(dirname "$SCRIPT_DIR")"
COMPOSE_FILE="$REPO_ROOT/docker-compose.win.yml"
EXPORTER_DIR="$REPO_ROOT/coretemp-exporter"
EXPORTER_EXE="$EXPORTER_DIR/coretemp-exporter.exe"
LOG_DIR="$REPO_ROOT/logs"

step() { echo -e "\033[36m==> $*\033[0m"; }
warn() { echo -e "\033[33mWARN: $*\033[0m" >&2; }

mkdir -p "$LOG_DIR"

# --- 1. Docker Compose -------------------------------------------------------
step "Starting Docker Compose exporters ($COMPOSE_FILE)"
docker compose -f "$COMPOSE_FILE" up -d

# --- 2. Core Temp must be running for shared memory --------------------------
if ! tasklist | grep -iq "Core Temp"; then
  warn "Core Temp does not appear to be running."
  warn "Start Core Temp first, otherwise the exporter reports coretemp_up=0."
fi

# --- 3. Build the exporter if missing ----------------------------------------
if [ ! -f "$EXPORTER_EXE" ]; then
  step "Building coretemp-exporter.exe"
  if ! command -v go >/dev/null 2>&1; then
    echo "ERROR: Go not found. Install Go or build the exe manually (see coretemp-exporter/README.md)." >&2
    exit 1
  fi
  ( cd "$EXPORTER_DIR" && go build -o coretemp-exporter.exe . )
fi

# --- 4. Start exporter in the background (idempotent) ------------------------
if tasklist | grep -iq "coretemp-exporter.exe"; then
  step "coretemp-exporter already running — leaving it."
else
  step "Starting coretemp-exporter in background on :$PORT"
  nohup "$EXPORTER_EXE" --listen ":$PORT" \
    > "$LOG_DIR/coretemp-exporter.out.log" \
    2> "$LOG_DIR/coretemp-exporter.err.log" &
  disown
  echo "    PID $!, logs in $LOG_DIR"
fi

# --- 5. Verify ---------------------------------------------------------------
sleep 2
if up=$(curl -s --max-time 5 "http://localhost:$PORT/metrics" | grep -oE '^coretemp_up [0-9]'); then
  if [ "$up" = "coretemp_up 1" ]; then
    echo -e "\033[32mOK: coretemp_up=1 — exporter is reading Core Temp.\033[0m"
  else
    warn "coretemp_up=0 — exporter is up but can't read Core Temp shared memory (is Core Temp running?)."
  fi
else
  warn "Could not scrape http://localhost:$PORT/metrics yet. Check $LOG_DIR/coretemp-exporter.err.log"
fi

echo
echo -e "\033[32mMonitoring stack started. Metrics: http://localhost:$PORT/metrics\033[0m"
echo "Stop the exporter with:  taskkill //IM coretemp-exporter.exe //F"

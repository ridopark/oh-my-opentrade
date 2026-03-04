#!/usr/bin/env bash
# debug-chrome.sh — Launch Chrome with remote debugging for DevTools MCP.
# Usage: ./scripts/debug-chrome.sh [--headless]
set -euo pipefail

DEBUG_PORT="${CHROME_DEBUG_PORT:-9222}"
USER_DATA_DIR="/tmp/omo-chrome-debug"
DASHBOARD_URL="${DASHBOARD_URL:-http://localhost:8000}"

# Find Chrome binary
CHROME=""
for candidate in google-chrome google-chrome-stable chromium-browser chromium; do
    if command -v "${candidate}" &>/dev/null; then
        CHROME="${candidate}"
        break
    fi
done

if [ -z "${CHROME}" ]; then
    echo "ERROR: No Chrome/Chromium found. Install with: sudo apt install google-chrome-stable"
    exit 1
fi

# Check if debug port is already in use
if ss -tlnp 2>/dev/null | grep -q ":${DEBUG_PORT} " || lsof -i ":${DEBUG_PORT}" &>/dev/null; then
    echo "Chrome debug port ${DEBUG_PORT} already in use."
    echo "DevTools MCP can connect at: http://127.0.0.1:${DEBUG_PORT}"
    exit 0
fi

HEADLESS=""
if [ "${1:-}" = "--headless" ]; then
    HEADLESS="--headless=new"
    echo "Launching Chrome in headless mode..."
else
    echo "Launching Chrome in headed mode (requires WSLg or X server)..."
fi

echo "  Binary:     ${CHROME}"
echo "  Debug port: ${DEBUG_PORT}"
echo "  Profile:    ${USER_DATA_DIR}"
echo "  Dashboard:  ${DASHBOARD_URL}"
echo ""

"${CHROME}" \
    --remote-debugging-port="${DEBUG_PORT}" \
    --remote-debugging-address=127.0.0.1 \
    --remote-allow-origins="*" \
    --user-data-dir="${USER_DATA_DIR}" \
    --no-first-run \
    --no-default-browser-check \
    --disable-default-apps \
    ${HEADLESS} \
    "${DASHBOARD_URL}" &

CHROME_PID=$!
echo "Chrome started (PID: ${CHROME_PID})"
echo "DevTools MCP can connect at: http://127.0.0.1:${DEBUG_PORT}"
echo ""
echo "To stop: kill ${CHROME_PID}"

wait "${CHROME_PID}" 2>/dev/null || true

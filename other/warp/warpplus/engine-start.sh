#!/bin/bash
# watashi v12.3.40dev: the old project name was taken out of this file (names, paths and links only)
# watashi v12.3.33dev: warp-plus launcher for watashi-warp.service
#
# What changed from v12.2.130:
#   - warp-plus is no longer exec'd (so this script stays alive as the
#     systemd-tracked process and can manage the watchdog alongside it).
#   - watchdog.sh is started in the background to provide healthcheck,
#     auto endpoint rotation, and WireGuard keepalive.
#   - SIGTERM/SIGINT are forwarded to both child processes so the service
#     stops cleanly without leaving orphan processes.

cd /opt/watashi-manager/other/warp/warpplus || exit 1

ARGS_FILE="engine.args"
WATCHDOG="./watchdog.sh"

if [ ! -f "$ARGS_FILE" ]; then
    echo "warp: engine.args not found — run.sh must run before the service starts." >&2
    exit 1
fi
if [ ! -x "./warp-plus" ]; then
    echo "warp: ./warp-plus is missing or not executable." >&2
    exit 1
fi

# watashi v12.3.73dev: MODE decides the binary. masque runs the separate
# usque-based engine and has no endpoint watchdog (nothing to rotate: the
# identity lives in its config file and the endpoint comes from it).
BIN="./warp-plus"
MODE=$(grep -E '^MODE=' engine.conf 2>/dev/null | tail -n 1 | cut -d= -f2 | tr -d ' "'"'"'\r')
if [ "$MODE" = "masque" ]; then
    BIN="./watashi-masque"
    WATCHDOG=""
    if [ ! -x "$BIN" ]; then
        echo "masque: ./$BIN is missing or not executable — install it with the Watashi-Warp kit (bash build-masque.sh --install)." >&2
        exit 1
    fi
fi

mapfile -t ARGS < "$ARGS_FILE"

# ── start the engine in the background ───────────────────────────────────────
"$BIN" "${ARGS[@]}" &
WARP_PID=$!

# ── start watchdog alongside it (warp-plus modes only) ───────────────────────
WATCHDOG_PID=0
if [ -n "$WATCHDOG" ] && [ -x "$WATCHDOG" ]; then
    bash "$WATCHDOG" "$WARP_PID" &
    WATCHDOG_PID=$!
else
    echo "warp: watchdog.sh not found or not executable — running without auto-rotation." >&2
fi

# ── forward stop signals to both children ────────────────────────────────────
_stop() {
    [ "$WARP_PID"     -gt 0 ] && kill -TERM "$WARP_PID"     2>/dev/null
    [ "$WATCHDOG_PID" -gt 0 ] && kill -TERM "$WATCHDOG_PID" 2>/dev/null
}
trap _stop SIGTERM SIGINT

# ── wait for warp-plus; exit with its code ───────────────────────────────────
wait "$WARP_PID"
WP_EXIT=$?

# Clean up watchdog (it may have already exited after rotating)
[ "$WATCHDOG_PID" -gt 0 ] && kill -TERM "$WATCHDOG_PID" 2>/dev/null
wait "$WATCHDOG_PID" 2>/dev/null

exit "$WP_EXIT"

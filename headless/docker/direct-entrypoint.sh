#!/bin/sh
set -eu

MODE="${CREATOR_MODE:-vk}"
RESOURCES="${RESOURCES:-default}"
BINS_DIR="${BINS_DIR:-/opt/wlb/bin}"
SECRETS_DIR="${SECRETS_DIR:-/run/secrets/wlb}"
LINK_FILE="${LINK_FILE:-/data/session-link.txt}"
EXISTING_LINK="${EXISTING_LINK:-}"
DISPLAY_NAME="${DISPLAY_NAME:-Headless}"

case "$RESOURCES" in
    moderate|default|unlimited) ;;
    *)
        echo "[FATAL] RESOURCES must be moderate, default, or unlimited" >&2
        exit 64
        ;;
esac

case "$MODE" in
    vk)
        BINARY="$BINS_DIR/headless-vk-creator"
        DEFAULT_COOKIE_FILE="$SECRETS_DIR/cookies-vk.json"
        ;;
    telemost)
        BINARY="$BINS_DIR/headless-telemost-creator"
        DEFAULT_COOKIE_FILE="$SECRETS_DIR/cookies-yandex.json"
        ;;
    wbstream)
        BINARY="$BINS_DIR/headless-wbstream-creator"
        DEFAULT_COOKIE_FILE="$SECRETS_DIR/cookies-wbstream.json"
        ;;
    dion)
        BINARY="$BINS_DIR/headless-dion-creator"
        DEFAULT_COOKIE_FILE="$SECRETS_DIR/cookies-dion.json"
        ;;
    *)
        echo "[FATAL] CREATOR_MODE must be vk, telemost, wbstream, or dion" >&2
        exit 64
        ;;
esac

COOKIE_FILE="${COOKIE_FILE:-$DEFAULT_COOKIE_FILE}"
if [ ! -s "$COOKIE_FILE" ]; then
    echo "[FATAL] Cookie file is missing or empty: $COOKIE_FILE" >&2
    echo "[FATAL] Export cookies from the desktop Creator and place them in $SECRETS_DIR" >&2
    exit 66
fi

mkdir -p "$(dirname "$LINK_FILE")"
rm -f "$LINK_FILE"

set -- \
    --cookies "$COOKIE_FILE" \
    --resources "$RESOURCES" \
    --write-file "$LINK_FILE"

case "$MODE" in
    vk)
        [ -n "$EXISTING_LINK" ] && set -- "$@" --vk-link "$EXISTING_LINK"
        [ -n "${VK_PEER_ID:-}" ] && set -- "$@" --peer-id "$VK_PEER_ID"
        set -- "$@" --video-reliability "${VIDEO_RELIABILITY:-auto}"
        ;;
    telemost)
        [ -n "$EXISTING_LINK" ] && set -- "$@" --tm-link "$EXISTING_LINK"
        ;;
    wbstream|dion)
        [ -n "$EXISTING_LINK" ] && set -- "$@" --room "$EXISTING_LINK"
        set -- "$@" --name "$DISPLAY_NAME"
        ;;
esac

[ -n "${UPSTREAM_SOCKS:-}" ] && set -- "$@" --upstream-socks "$UPSTREAM_SOCKS"
[ -n "${UPSTREAM_USER:-}" ] && set -- "$@" --upstream-user "$UPSTREAM_USER"
[ -n "${UPSTREAM_PASS:-}" ] && set -- "$@" --upstream-pass "$UPSTREAM_PASS"

echo "[config] direct creator mode=$MODE resources=$RESOURCES"
echo "[config] active join link will also be stored in $LINK_FILE"
exec "$BINARY" "$@"

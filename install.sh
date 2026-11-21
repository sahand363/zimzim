#!/usr/bin/env bash
# ZimZim installer: bash <(curl -Ls https://raw.githubusercontent.com/sahand363/zimzim/main/install.sh)
set -o pipefail

REPO="sahand363/zimzim"
DIR="/usr/local/zimzim"
CONF="/etc/zimzim/config.json"
LOG="/tmp/zimzim-install.log"
: > "$LOG"

C0=$'\e[0m'; DIM=$'\e[2m'; B=$'\e[1m'; OR=$'\e[38;5;209m'; GR=$'\e[38;5;114m'; RD=$'\e[38;5;203m'

fail(){ printf "\n%s✗ %s%s\n  See %s\n" "$RD" "$1" "$C0" "$LOG"; exit 1; }

bar(){ # bar PERCENT "message"
  local p=$1 w=30 f i a="" b=""
  f=$(( p * w / 100 ))
  for ((i=0;i<w;i++)); do if ((i<f)); then a+="█"; else b+="░"; fi; done
  printf "\r\e[K  %s%s%s%s%s %3d%%  %s%s%s" "$OR" "$a" "$DIM" "$b" "$C0" "$p" "$DIM" "$2" "$C0"
}

[ "$(id -u)" -eq 0 ] || { echo "Please run as root (sudo -i)."; exit 1; }

clear
printf "\n  %s%sZimZim%s %s· walkie-talkie for your server%s\n\n" "$B" "$OR" "$C0" "$DIM" "$C0"

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  armv7l|armv7) ARCH=armv7 ;;
  *) echo "Unsupported CPU: $(uname -m)"; exit 1 ;;
esac

# --- port question (before the progress bar) ---
OLD_PORT=""
if [ -f "$CONF" ]; then OLD_PORT=$(grep -o '"port": *[0-9]*' "$CONF" | grep -o '[0-9]*$'); fi
DEF=${OLD_PORT:-2096}
while true; do
  printf "  Web UI port %s[%s]%s: " "$DIM" "$DEF" "$C0"
  read -r PORT </dev/tty
  PORT=${PORT:-$DEF}
  if ! [[ "$PORT" =~ ^[0-9]+$ ]] || [ "$PORT" -lt 1 ] || [ "$PORT" -gt 65535 ]; then echo "  Enter a number from 1 to 65535."; continue; fi
  if [ "$PORT" != "$OLD_PORT" ] && ss -ltnH "( sport = :$PORT )" 2>/dev/null | grep -q .; then echo "  Port $PORT is busy, pick another."; continue; fi
  break
done
echo

bar 5 "Installing tools"
if command -v apt-get >/dev/null; then
  NEED=""; for t in curl tar openssl; do command -v $t >/dev/null || NEED="$NEED $t"; done
  command -v ss >/dev/null || NEED="$NEED iproute2"
  if [ -n "$NEED" ]; then DEBIAN_FRONTEND=noninteractive apt-get update -y >>"$LOG" 2>&1; DEBIAN_FRONTEND=noninteractive apt-get install -y $NEED >>"$LOG" 2>&1 || fail "Could not install:$NEED"; fi
fi

bar 20 "Finding latest release"
TAG=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" 2>>"$LOG" | grep -o '"tag_name": *"[^"]*"' | head -1 | cut -d'"' -f4)
[ -n "$TAG" ] || fail "Could not reach GitHub releases."

bar 35 "Downloading ZimZim $TAG"
TMP=$(mktemp -d)
curl -fsSL "https://github.com/$REPO/releases/download/$TAG/zimzim-linux-$ARCH.tar.gz" -o "$TMP/z.tgz" >>"$LOG" 2>&1 || fail "Download failed."

bar 55 "Unpacking"
tar -xzf "$TMP/z.tgz" -C "$TMP" >>"$LOG" 2>&1 || fail "Archive is broken."
systemctl stop zimzim >>"$LOG" 2>&1
mkdir -p "$DIR" /etc/zimzim
install -m 755 "$TMP/zimzim-server" "$DIR/zimzim-server" || fail "Could not install binary."
install -m 755 "$TMP/menu.sh" /usr/local/bin/zimzim || fail "Could not install menu."
echo "$TAG" > "$DIR/version"
rm -rf "$TMP"

bar 70 "Creating config"
FRESH=0
if [ ! -f "$CONF" ]; then FRESH=1; fi
OUT=$(ZIMZIM_CONFIG=$CONF "$DIR/zimzim-server" init -port "$PORT" 2>>"$LOG") || fail "Could not write config."
ADMIN_PASS=$(echo "$OUT" | grep '^pass=' | cut -d= -f2)
chmod 600 "$CONF"

bar 82 "Setting up service"
cat > /etc/systemd/system/zimzim.service <<EOF
[Unit]
Description=ZimZim walkie-talkie
After=network-online.target
Wants=network-online.target

[Service]
Environment=ZIMZIM_CONFIG=$CONF
ExecStart=$DIR/zimzim-server serve
Restart=always
RestartSec=2
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload >>"$LOG" 2>&1
systemctl enable zimzim >>"$LOG" 2>&1

bar 92 "Opening firewall"
if command -v ufw >/dev/null && ufw status 2>/dev/null | grep -q "Status: active"; then ufw allow "$PORT"/tcp >>"$LOG" 2>&1; fi

bar 96 "Starting"
systemctl restart zimzim >>"$LOG" 2>&1
sleep 1
systemctl is-active --quiet zimzim || fail "Service did not start (journalctl -u zimzim)."
bar 100 "Done"
echo; echo

if [ "$FRESH" = 1 ] && [ -n "$ADMIN_PASS" ]; then
  mkdir -p /etc/zimzim && printf "user=admin\npass=%s\n" "$ADMIN_PASS" > /etc/zimzim/first-login && chmod 600 /etc/zimzim/first-login
fi

printf "  %s✓ ZimZim %s is running.%s Opening the menu…\n" "$GR" "$TAG" "$C0"
sleep 1
exec /usr/local/bin/zimzim </dev/tty

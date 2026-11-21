#!/usr/bin/env bash
# ZimZim server menu
REPO="sahand363/zimzim"
DIR="/usr/local/zimzim"
BIN="$DIR/zimzim-server"
export ZIMZIM_CONFIG="/etc/zimzim/config.json"
CERTS="/etc/zimzim/certs"

C0=$'\e[0m'; DIM=$'\e[2m'; B=$'\e[1m'; OR=$'\e[38;5;209m'; GR=$'\e[38;5;114m'; RD=$'\e[38;5;203m'; CY=$'\e[38;5;152m'

[ "$(id -u)" -eq 0 ] || { echo "Please run as root."; exit 1; }
[ -x "$BIN" ] || { echo "ZimZim is not installed."; exit 1; }

IP_CACHE=""
public_ip(){
  [ -n "$IP_CACHE" ] && { echo "$IP_CACHE"; return; }
  IP_CACHE=$(curl -s4 --max-time 4 https://api.ipify.org || curl -s4 --max-time 4 https://ifconfig.me || hostname -I | awk '{print $1}')
  echo "$IP_CACHE"
}
info(){ "$BIN" info 2>/dev/null | grep "^$1=" | cut -d= -f2-; }
pause(){ printf "\n  %sPress Enter to go back…%s" "$DIM" "$C0"; read -r _; }
ok(){ printf "  %s✓ %s%s\n" "$GR" "$1" "$C0"; }
err(){ printf "  %s✗ %s%s\n" "$RD" "$1" "$C0"; }
ask(){ printf "  %s: " "$1"; read -r REPLY; }
restart(){ systemctl restart zimzim; sleep 1; systemctl is-active --quiet zimzim && ok "Restarted" || err "Service failed: journalctl -u zimzim -n 30"; }
# stop the service while editing config so it cannot overwrite the change
zz(){ systemctl stop zimzim; "$BIN" "$@"; }
port_busy(){ ss -ltnH "( sport = :$1 )" 2>/dev/null | grep -q .; }

header(){
  clear
  local ver st url user
  ver=$(cat "$DIR/version" 2>/dev/null || "$BIN" version)
  if systemctl is-active --quiet zimzim; then st="${GR}● running${C0}"; else st="${RD}● stopped${C0}"; fi
  url=$("$BIN" url "$(public_ip)")
  user=$(info user)
  printf "\n  %s%sZimZim%s %s%s%s   %s\n" "$B" "$OR" "$C0" "$DIM" "$ver" "$C0" "$st"
  printf "  %s──────────────────────────────────────────────%s\n" "$DIM" "$C0"
  printf "  %sAdmin panel%s  %s%s%s\n" "$DIM" "$C0" "$CY" "$url" "$C0"
  printf "  %sAdmin user%s   %s" "$DIM" "$C0" "$user"
  if [ -f /etc/zimzim/first-login ]; then
    printf "   %spass%s %s %s(change it with 1)%s" "$DIM" "$C0" "$(grep '^pass=' /etc/zimzim/first-login | cut -d= -f2)" "$DIM" "$C0"
  fi
  echo
  if [ "$(info tls)" != "true" ]; then
    printf "  %s! No SSL yet: phones only allow the mic over HTTPS. Use 2 or 3.%s\n" "$OR" "$C0"
  fi
  printf "  %s──────────────────────────────────────────────%s\n\n" "$DIM" "$C0"
}

change_admin(){
  local u p p2
  ask "New admin username [$(info user)]"; u=${REPLY:-$(info user)}
  printf "  New password (6+ chars): "; read -rs p; echo
  printf "  Repeat password: "; read -rs p2; echo
  [ "$p" = "$p2" ] || { err "Passwords don't match"; return; }
  zz set-admin "$u" "$p" && rm -f /etc/zimzim/first-login
  restart
}

maybe_443(){
  if [ "$(info port)" != "443" ] && ! port_busy 443; then
    ask "Move the panel to port 443 so links have no port? (y/N)"
    if [[ "$REPLY" =~ ^[Yy] ]]; then zz set-port 443 >/dev/null && ok "Port set to 443"; fi
  fi
}

get_ssl(){
  local d email
  ask "Domain pointing to this server (e.g. talk.example.com)"; d=$REPLY
  [ -n "$d" ] || return
  local rip; rip=$(getent ahostsv4 "$d" | awk '{print $1; exit}')
  if [ -n "$rip" ] && [ "$rip" != "$(public_ip)" ]; then
    printf "  %s%s points to %s, this server is %s (fine if it's behind a CDN with proxy off).%s\n" "$OR" "$d" "$rip" "$(public_ip)" "$C0"
  fi
  if port_busy 80; then err "Port 80 is busy. Let's Encrypt needs it free for a minute."; return; fi
  if [ ! -x ~/.acme.sh/acme.sh ]; then
    ask "Email for Let's Encrypt (optional)"; email=${REPLY:-zimzim@$d}
    echo "  Installing acme.sh…"
    curl -fsSL https://get.acme.sh | sh -s email="$email" >/tmp/zimzim-acme.log 2>&1 || { err "acme.sh install failed (/tmp/zimzim-acme.log)"; return; }
  fi
  command -v socat >/dev/null || { command -v apt-get >/dev/null && DEBIAN_FRONTEND=noninteractive apt-get install -y socat >/dev/null 2>&1; }
  echo "  Requesting certificate…"
  ~/.acme.sh/acme.sh --set-default-ca --server letsencrypt >/dev/null 2>&1
  if ! ~/.acme.sh/acme.sh --issue -d "$d" --standalone --keylength ec-256 >>/tmp/zimzim-acme.log 2>&1; then
    grep -q "Domains not changed" /tmp/zimzim-acme.log || { err "Certificate failed. Check DNS and port 80 (/tmp/zimzim-acme.log)"; return; }
  fi
  mkdir -p "$CERTS"
  ~/.acme.sh/acme.sh --install-cert -d "$d" --ecc --key-file "$CERTS/key.pem" --fullchain-file "$CERTS/cert.pem" \
    --reloadcmd "systemctl restart zimzim" >>/tmp/zimzim-acme.log 2>&1 || { err "Install cert failed"; return; }
  zz set-tls "$d" "$CERTS/cert.pem" "$CERTS/key.pem" >/dev/null && ok "SSL ready for $d (auto-renews)"
  maybe_443
  restart
}

self_signed(){
  local ip; ip=$(public_ip)
  mkdir -p "$CERTS"
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 3650 \
    -keyout "$CERTS/self-key.pem" -out "$CERTS/self-cert.pem" -subj "/CN=$ip" \
    -addext "subjectAltName=IP:$ip" >/dev/null 2>&1 || { err "openssl failed"; return; }
  zz set-tls "" "$CERTS/self-cert.pem" "$CERTS/self-key.pem" >/dev/null
  ok "Self-signed SSL on $ip. Browsers show a warning once; tap Advanced, then Proceed."
  restart
}

remove_ssl(){ zz clear-tls >/dev/null && ok "SSL removed"; restart; }

change_port(){
  ask "New port [$(info port)]"; local p=$REPLY
  [[ "$p" =~ ^[0-9]+$ ]] && [ "$p" -ge 1 ] && [ "$p" -le 65535 ] || { err "Invalid port"; return; }
  [ "$p" = "$(info port)" ] && return
  port_busy "$p" && { err "Port $p is busy"; return; }
  zz set-port "$p" >/dev/null && ok "Port set to $p"
  command -v ufw >/dev/null && ufw status 2>/dev/null | grep -q "Status: active" && ufw allow "$p"/tcp >/dev/null
  restart
}

update(){
  echo "  Updating…"
  bash <(curl -fsSL "https://raw.githubusercontent.com/$REPO/main/install.sh") </dev/tty
  exit 0
}

uninstall(){
  ask "Type DELETE to remove ZimZim and all walkies"
  [ "$REPLY" = "DELETE" ] || return
  systemctl disable --now zimzim >/dev/null 2>&1
  rm -f /etc/systemd/system/zimzim.service; systemctl daemon-reload
  rm -rf "$DIR" /etc/zimzim /usr/local/bin/zimzim
  ok "ZimZim removed"; exit 0
}

while true; do
  header
  cat <<EOF
  ${OR}1${C0}  Change admin username & password
  ${OR}2${C0}  Get SSL with a domain (Let's Encrypt)
  ${OR}3${C0}  SSL without a domain (self-signed, by IP)
  ${OR}4${C0}  Remove SSL
  ${OR}5${C0}  Change port
  ${OR}6${C0}  New secret admin link
  ${OR}7${C0}  Restart
  ${OR}8${C0}  Live logs ${DIM}(Ctrl+C to stop)${C0}
  ${OR}9${C0}  Update ZimZim
  ${OR}10${C0} Uninstall
  ${OR}0${C0}  Exit

EOF
  printf "  Choose: "; read -r c
  echo
  case "$c" in
    1) change_admin; pause ;;
    2) get_ssl; pause ;;
    3) self_signed; pause ;;
    4) remove_ssl; pause ;;
    5) change_port; pause ;;
    6) zz new-admin-path >/dev/null && ok "New admin link made"; restart; pause ;;
    7) restart; pause ;;
    8) journalctl -u zimzim -f -n 50 ;;
    9) update ;;
    10) uninstall; pause ;;
    0|q|"") clear; exit 0 ;;
  esac
done

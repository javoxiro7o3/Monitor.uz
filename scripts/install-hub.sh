#!/bin/sh
# Monitor.uz hub o'rnatish (internetsiz). Paket ichidan ishga tushiring:
#   sudo ./install-hub.sh --url https://10.0.0.5:8443
# Qayta ishga tushirish xavfsiz: binary yangilanadi, ma'lumotlar saqlanadi.
set -eu
cd "$(dirname "$0")"

URL=''
LISTEN=':8443'
SAN=''
die() { echo "XATO: $*" >&2; exit 1; }
while [ $# -gt 0 ]; do
	case "$1" in
	--url) URL="${2:-}"; shift 2 ;;
	--listen) LISTEN="${2:-}"; shift 2 ;;
	--san) SAN="${2:-}"; shift 2 ;;
	*) die "noma'lum parametr: $1" ;;
	esac
done
[ "$(id -u)" -eq 0 ] || die "root huquqi kerak"
[ -f bin/monitor-hub ] || die "bin/monitor-hub topilmadi: skriptni paket katalogidan ishga tushiring"

if ! id monitor-hub >/dev/null 2>&1; then
	useradd --system --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin monitor-hub
fi

if systemctl is-active --quiet monitor-hub; then systemctl stop monitor-hub; fi
install -m 0755 bin/monitor-hub /usr/local/bin/monitor-hub
install -d -m 0750 -o monitor-hub -g monitor-hub /var/lib/monitor-hub /var/lib/monitor-hub/dist
# Agent binaries offered to hosts by the install script.
install -m 0644 -o monitor-hub -g monitor-hub dist/* /var/lib/monitor-hub/dist/
install -d -m 0755 /usr/local/share/doc/monitor-uz
install -m 0644 docs/* /usr/local/share/doc/monitor-uz/

install -d -m 0755 /etc/monitor-hub
if [ ! -f /etc/monitor-hub/hub.env ]; then
	[ -n "$URL" ] || die "birinchi o'rnatishda --url majburiy (masalan --url https://10.0.0.5:8443)"
	cat >/etc/monitor-hub/hub.env <<ENV
# Monitor.uz hub sozlamalari. O'zgartirgandan keyin: systemctl restart monitor-hub
MONITOR_HUB_URL=$URL
MONITOR_HUB_LISTEN=$LISTEN
MONITOR_HUB_SAN=$SAN
MONITOR_HUB_HISTORY=168h
ENV
	chmod 0644 /etc/monitor-hub/hub.env
elif [ -n "$URL" ]; then
	sed -i "s|^MONITOR_HUB_URL=.*|MONITOR_HUB_URL=$URL|" /etc/monitor-hub/hub.env
fi

install -m 0644 systemd/monitor-hub.service /etc/systemd/system/monitor-hub.service
systemctl daemon-reload
systemctl enable --now monitor-hub
sleep 3
systemctl --no-pager --lines=5 status monitor-hub || true
. /etc/monitor-hub/hub.env
echo
echo "Hub ishlayapti: $MONITOR_HUB_URL"
echo "Pin: $(/usr/local/bin/monitor-hub pin --data /var/lib/monitor-hub)"
if [ -f /var/lib/monitor-hub/setup-code ]; then
	echo
	echo "Birinchi admin uchun o'rnatish kodi: $(cat /var/lib/monitor-hub/setup-code)"
	echo "Brauzerda $MONITOR_HUB_URL ni oching va shu kod bilan admin yarating."
fi
echo "Agentlar: panel -> Sozlamalar -> Agentlar -> Token yaratish."

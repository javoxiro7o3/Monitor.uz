#!/bin/bash
# Test-only: start hub + 3 simulated Proxmox hosts + 1 guest.
set -eu
D=${SIM_DIR:-/tmp/monitor-sim}; mkdir -p "$D"; cp "$(dirname "$0")/sim.py" "$D/"
B=$D/bin
H=$D/hosts
HUB=https://127.0.0.1:18443
unset HTTPS_PROXY HTTP_PROXY https_proxy http_proxy

pkill -f "monitor-hub serve" || true
pkill -f "monitor-agent run" || true
pkill -f "sim.py run" || true
sleep 1
rm -rf "$D/hub" "$D/agents" "$D/webhook.log" "$H"
mkdir -p "$D/hub/dist" "$D/agents"
cp "$B/monitor-agent" "$D/hub/dist/monitor-agent-linux-amd64"
(cd "$D/hub/dist" && sha256sum monitor-agent-linux-amd64 > SHA256SUMS && echo 0.9.0 > VERSION)
touch /dev/ipmi0

python3 "$D/sim.py" setup
nohup python3 "$D/sim.py" run > "$D/sim.log" 2>&1 &
nohup "$B/monitor-hub" serve --data "$D/hub" --listen 127.0.0.1:18443 --url "$HUB" > "$D/hub.log" 2>&1 &
sleep 2
CODE=$(cat "$D/hub/setup-code")
J=$D/cookies
rm -f "$J"
c() { curl -sk -b "$J" -c "$J" -H 'Content-Type: application/json' -H 'X-Requested-With: monitor' "$@"; }
c -X POST "$HUB/api/v1/auth/setup" -d "{\"code\":\"$CODE\",\"username\":\"admin\",\"password\":\"Monitor-2026!\"}" >/dev/null
c -X PUT "$HUB/api/v1/alert-settings" -d '{"webhook":{"enabled":true,"url":"http://127.0.0.1:18065/hooks/secret-key-123","channel":"monitoring"}}' >/dev/null
PIN=$("$B/monitor-hub" pin --data "$D/hub")

start_agent() { # name mode
	local name=$1 mode=$2 st=$D/agents/$1
	mkdir -p "$st/lib"
	cp "$B/monitor-agent" "$st/lib/monitor-agent"
	local tok
	tok=$(c -X POST "$HUB/api/v1/tokens" -d "{\"note\":\"$name\",\"mode\":\"$mode\"}" | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')
	local binds
	if [ "$mode" = host ]; then
		binds="mount --bind $H/$name/pve /etc/pve; mount --bind $H/$name/net /sys/class/net; mkdir -p /sys/fs/cgroup/qemu.slice /sys/fs/cgroup/lxc; mount --bind $H/$name/cg/qemu.slice /sys/fs/cgroup/qemu.slice; mount --bind $H/$name/cg/lxc /sys/fs/cgroup/lxc; mount --bind $H/$name/tools /usr/local/sbin"
	else
		binds="mkdir -p /sys/fs/cgroup/system.slice; mount --bind $H/guest/cg/system.slice /sys/fs/cgroup/system.slice; mount --bind /run/mzsim /var/run"
	fi
	mkdir -p /etc/pve
	unshare -m -u --propagation private bash -c "hostname $name; $binds; \
		$st/lib/monitor-agent enroll --hub $HUB --pin '$PIN' --token $tok --mode $mode --state-dir $st >/dev/null; \
		while true; do $st/lib/monitor-agent run --mode=$mode --state-dir $st; sleep 1; done" > "$st/agent.log" 2>&1 &
}
start_agent srv-01 host
start_agent srv-02 host
start_agent srv-03 host
start_agent vm-app-1 guest
sleep 4
echo "hub: $HUB  login admin / Monitor-2026!"
c "$HUB/api/v1/agents" | python3 -c 'import json,sys; [print(a["id"],a["name"],a["mode"],a["status"],list(a["reports"].keys())) for a in json.load(sys.stdin)]'

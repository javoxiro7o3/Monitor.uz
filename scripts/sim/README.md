# Simulyatsiya (faqat sinov uchun)

Real serversiz butun zanjirni sinash: 3 ta soxta Proxmox host (srv-01..03) va
Docker bor 1 ta guest. Har "host" alohida mount/UTS namespace'da ishlaydi:
soxta `/etc/pve`, `/sys/class/net`, cgroup fayllari va `ssacli`/`smartctl`/`ipmitool`
skriptlari. Docker API soxta unix socket orqali. Webhook qabul qiluvchi
127.0.0.1:18065 da (`webhook.log`). srv-03 da RAID 5 degraded, bay 5 nosoz.

Talab: root, `unshare`, python3, Linux. Ishlab chiqarish serverida ishga tushirmang:
u `/dev/ipmi0` faylini yaratadi va `/run/mzsim` dan foydalanadi.

```sh
# binary'lar: $SIM_DIR/bin/{monitor-hub,monitor-agent,monitor-agent-0.9.1}
export SIM_DIR=/tmp/monitor-sim; mkdir -p $SIM_DIR/bin
CGO_ENABLED=0 go build -mod=vendor -ldflags "-X main.version=0.9.0" -o $SIM_DIR/bin/monitor-hub ./cmd/hub
CGO_ENABLED=0 go build -mod=vendor -ldflags "-X main.version=0.9.0" -o $SIM_DIR/bin/monitor-agent ./cmd/agent
scripts/sim/run.sh            # panel: https://127.0.0.1:18443  admin / Monitor-2026!
node scripts/sim/shots.js /tmp/shots "#/|overview,#/disks|disks"   # Playwright skrinshotlar
```

To'xtatish: `pkill -f monitor-hub; pkill -f "monitor-agent run"; pkill -f "sim.py run"; rm -f /dev/ipmi0`.

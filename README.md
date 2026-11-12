# Monitor.uz

Korxonaning ichki monitoring tizimi. U mustaqil Proxmox VE serverlarini, ulardagi VM/CT larni va ular ichidagi Docker konteynerlarni bitta o'zbekcha web panelda kuzatadi. Nosozlik bo'lsa, ichki messenjerga (Mattermost webhook) xabar yuboradi. Tizim to'liq internetsiz ishlaydi.

**Versiya 0.9.0.** TZ dagi 6 bosqichning hammasi amalga oshirilgan. Real HPE serverlarda hali sinalmagan: tekshirish ro'yxati [docs/HISOBOT.md](docs/HISOBOT.md) da.

## Nimalar bor

| Bo'lim | Nima ko'rinadi | Manba |
|---|---|---|
| Umumiy ko'rinish | serverlar kartochkalari (holat, CPU/RAM/disk, sparkline), VM/CT soni, faol alertlar, disklar, Docker top-5 | hammasi |
| Server | CPU (yadrolar bo'yicha ham), RAM/swap/ZFS ARC, load, PSI, tarmoq, disk I/O, fayl tizimlari, jonli va 1 haftalik grafiklar, VM/CT ro'yxati, disk/RAID, harorat/fan/PSU | host agent: `/proc`, `/sys`, ssacli, smartctl, ipmitool |
| VM va CT | hamma VM/CT: holat, CPU, RAM, I/O, tarmoq, uptime, "kuzatiladi" belgisi; VM sahifasida grafiklar, NIC/VLAN/IP, ichki holat | `/etc/pve/*.conf`, cgroup v2; guest agent |
| Docker | barcha konteynerlar: qaysi VM da, CPU/RAM/tarmoq/I/O, restartlar; saralash va qidiruv | guest agent: Docker socket (faqat GET), cgroup |
| Disklar | fizik disklar (bay, model, seriya, harorat, SMART, ishlagan soat), RAID lar va a'zolari, kontroller/batareya; nosozlari yuqorida | ssacli, smartctl, mdstat, zpool |
| Tarmoq chizmasi | fizik tarmoq → server → bridge → VLAN → VM/CT, holat shakllari, trafik, bosilganda tafsilot | `/sys/class/net`, Proxmox konfiglari, guest IP lari |
| Alertlar | faol va 1 haftalik tarix, sukut (1/8/24 soat), qoidalarni sozlash | hub |
| Sozlamalar | agentlar (token, bekor qilish, yangilash/qaytarish), kuzatiladigan VM lar, webhook, foydalanuvchilar, parol, 2FA | hub |

## Tarkib

- `monitor-agent` — bitta statik binary, ikki rejimda ishlaydi:
  - `run --mode=host` — Proxmox hostda;
  - `run --mode=guest` — Docker bor VM/CT ichida.

  Agent faqat hub'ga chiquvchi HTTPS ulanish ochadi, o'zida port ochmaydi. Hub o'chiq bo'lsa, ma'lumotni buferda saqlab turadi.
- `monitor-hub` — bitta statik binary. Agentlardan ma'lumot qabul qiladi, SQLite'da saqlaydi, alertlarni tekshiradi va panelni beradi (panel binary ichiga o'rnatilgan, jonli yangilanish SSE orqali).

## O'rnatish (qisqa)

```sh
sudo ./install-hub.sh --url https://HUB_IP:8443   # hub; chiqqan o'rnatish kodi bilan brauzerda admin yarating
# Panel → Sozlamalar → Agentlar → Token yaratish → chiqqan buyruqni Proxmox hostda (root) ishga tushiring
```

Bosqichma-bosqich qo'llanma: [docs/QOLLANMA.md](docs/QOLLANMA.md). Ma'lumotnoma: [docs/ORNATISH.md](docs/ORNATISH.md).

## Yig'ish (build)

Go 1.24 yoki undan yangisi kerak. Bog'liqliklar `vendor/` ichida, shuning uchun build internetsiz ishlaydi.

```sh
./scripts/build.sh                 # dist/monitor-uz-<versiya>-linux-amd64.tar.gz
GOARCH=arm64 ./scripts/build.sh
go test ./...
```

## Repozitoriy tuzilishi

```
cmd/agent, cmd/hub      dasturlar
internal/collector      /proc, /sys, cgroup, Proxmox konfig, ssacli, smartctl, ipmitool, Docker parserlari
internal/agent          yig'ish/yuborish sikli, bufer, pin'langan HTTPS, o'z-o'zini yangilash
internal/store          SQLite: agentlar, tarix (1 soat xom + 10 s o'rtacha 1 hafta), alertlar, foydalanuvchilar, sirlar
internal/alert          qoida dvigateli, guruhlash/eslatish/tiklandi, webhook (Slack formati)
internal/auth           Argon2id, TOTP, urinishlarni cheklash
internal/api            HTTP: agent API, panel API, SSE, login
internal/tlsutil        self-signed sertifikat va SPKI pin
internal/model          agent va hub o'rtasidagi umumiy turlar
deploy/                 systemd unitlar, agent o'rnatish skripti
web/                    panel (HTML/JS/CSS, shriftlar, i18n/uz.json)
scripts/                build va hub o'rnatish
```

## Bog'liqliklar

- `modernc.org/sqlite` v1.46.0 — sof Go'dagi SQLite;
- `golang.org/x/crypto` v0.45.0 — Argon2id uchun;
- Inter va Space Grotesk shriftlari (SIL OFL 1.1).

Grafiklar, tarmoq chizmasi va TOTP uchun tashqi kutubxona ishlatilmagan.

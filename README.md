# Monitor.uz

Korxonaning ichki monitoring tizimi: mustaqil Proxmox VE serverlarini, ulardagi VM/CT larni va Docker konteynerlarni bitta o'zbekcha web panelda kuzatadi. To'liq internetsiz ishlaydi.

**Holat: 1-bosqich (poydevor) tayyor.** Host agent, hub, SQLite saqlash va server grafiklari jonli yangilanadigan panel.

## Tarkib

| Qism | Vazifasi |
|---|---|
| `monitor-agent` | Har Proxmox hostda ishlaydi. `/proc` dan 1 soniyada o'qiydi va hub'ga HTTPS orqali, token bilan yuboradi. Hub o'chiq bo'lsa, ma'lumotni buferda saqlab turadi. Kiruvchi port ochmaydi. |
| `monitor-hub` | Ma'lumotni qabul qiladi, SQLite'da saqlaydi, panelni beradi (SSE orqali jonli oqim). UI binary ichiga o'rnatilgan. |

Ikkalasi ham statik Go binary (CGO yo'q). Debian 12 va 13'da bir xil ishlaydi.

## Tez boshlash

```sh
# 1. Hub (1-serverdagi Debian VM/CT da), paket katalogidan:
sudo ./install-hub.sh --url https://HUB_IP:8443

# 2. Token (hub'da):
sudo -u monitor-hub monitor-hub token --note srv-01

# 3. Proxmox hostda (root), 2-qadam chiqargan buyruq:
curl -fsS --insecure --pinnedpubkey 'sha256//PIN' https://HUB_IP:8443/install.sh | sh -s -- --token TOKEN

# 4. Brauzerda: https://HUB_IP:8443
```

Batafsil: [docs/ORNATISH.md](docs/ORNATISH.md). 1-bosqich hisoboti: [docs/HISOBOT-1-bosqich.md](docs/HISOBOT-1-bosqich.md).

## Yig'ish (build)

Go 1.24 yoki undan yangisi kerak. Bog'liqliklar `vendor/` da turadi, shuning uchun internet kerak emas.

```sh
./scripts/build.sh                 # dist/monitor-uz-<versiya>-linux-amd64.tar.gz
GOARCH=arm64 ./scripts/build.sh    # boshqa arxitektura uchun
go test ./...                      # testlar
```

Serverlar (HPE DL380 Gen10) x86_64 bo'lgani uchun o'rnatishga `linux-amd64` paketi kerak. Bu build qaysi kompyuterda qilinishidan qat'i nazar shunday: Go boshqa arxitektura uchun ham yig'a oladi.

## Repozitoriy tuzilishi

```
cmd/agent, cmd/hub      ishga tushiriladigan dasturlar
internal/collector      /proc parserlari (har metrika alohida faylda) va host kollektori
internal/agent          bufer, pin'langan HTTPS mijoz, yuborish sikli
internal/store          SQLite: agentlar, tokenlar, xom ma'lumot (1 soat) va 10 s o'rtachalar (1 hafta)
internal/api            qabul API, panel API, SSE, o'rnatish skripti
internal/tlsutil        self-signed sertifikat va SPKI pin (promptdagi tuzilmaga qo'shimcha)
internal/model          agent va hub o'rtasidagi umumiy turlar (qo'shimcha)
deploy/                 systemd unitlar va agent o'rnatish skripti shabloni
web/                    panel: HTML/JS/CSS, shriftlar, i18n/uz.json
scripts/                build va hub o'rnatish skriptlari
vendor/                 Go bog'liqliklari (offline build uchun)
```

## Bog'liqliklar

- `modernc.org/sqlite` v1.46.0 — sof Go'da yozilgan SQLite (Go 1.24 bilan mos keladigan eng yangi versiya).
- Shriftlar: Inter va Space Grotesk (SIL OFL 1.1), `web/static/fonts/` ichida.
- Grafiklar uchun tashqi kutubxona yo'q, hammasi `web/static/charts.js` da.

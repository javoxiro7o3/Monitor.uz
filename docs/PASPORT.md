# Monitor.uz — Loyiha pasporti (Handover)

Holat sanasi: **2026-11-13**. Bu hujjat yangi chatda ishni to'xtagan joydan davom ettirish uchun. Yangi chat avval shu faylni, keyin `docs/HISOBOT.md` ni o'qishi kerak.

- Repozitoriy: `javoxiro7o3/Monitor.uz`, ishchi branch: **`claude/new-session-hz54sa`** (`main` hali yo'q, PR ochilmagan).
- Oxirgi kommitlar: `ce04b50` 1-bosqich → `236520b` 2–6-bosqichlar → `243c417` o'rnatish qo'llanmasi → shu pasport.
- Versiya: **0.9.0** (`VERSION` fayli). Paket: `dist/monitor-uz-0.9.0-linux-amd64.tar.gz` (`dist/` gitda yo'q, `scripts/build.sh` bilan qayta yig'iladi).

---

## 1. Loyiha maqsadi va joriy holati

### Maqsad

Korxonaning ichki monitoringi:

- 5–6 ta mustaqil **Proxmox VE** serveri (HPE ProLiant DL380 Gen10, klaster yo'q), ulardagi ~90 VM/CT va VM ichidagi **Docker** konteynerlari kuzatiladi;
- hammasi bitta o'zbekcha web panelda;
- nosozlikda ichki messenjerga (**Mattermost webhook**) xabar yuboriladi;
- **internet yo'q**: hamma narsa offline o'rnatiladi;
- faqat monitoring: VM boshqaruvi yo'q.

### Kelishilgan qarorlar (o'zgartirilmaydi)

Manba: foydalanuvchi bergan "AI uchun PROMPT", TZ va "logo va dizayn" fayllari, hamda chatdagi javoblar.

- **Arxitektura.** Bitta Go binary agent, ikki rejimda: `--mode=host` (Proxmox hostda) va `--mode=guest` (Docker bor VM/CT da). Hub alohida: srv-01 dagi VM/CT, Debian 12 yoki 13.
- **Aloqa.** Faqat agent → hub, HTTPS, port 8443. Hub agentlarga ulanmaydi.
- **TLS.** Hub o'z self-signed sertifikatidan foydalanadi (foydalanuvchi: "CA bor, lekin hub'niki alohida bo'laversin"). Agentlar sertifikatni SPKI **pin** bo'yicha tekshiradi.
- **Saqlash.** SQLite. Oxirgi 1 soat asl chastotada, keyin 10 soniyalik o'rtachalar, jami 1 hafta.
- **Alertlar.** Panel va webhook (Slack formati). **Email/SMTP yo'q.** TZ da SMTP qolib ketgan edi; foydalanuvchi bilan kelishib olib tashlandi.
- **Qamrovdan tashqari:**
  - VM start/stop, terminal;
  - loglar;
  - Windows agent;
  - OPNsense API, iLO Redfish, SNMP/LLDP;
  - 1 haftadan uzoq tarix.
- **Til.** Panel matnlari faqat o'zbekcha (lotin, `oʻ` uchun U+02BB). Kod, commit'lar va nomlar inglizcha. Foydalanuvchi bilan o'zbekcha gaplashiladi.
- **Dizayn.**
  - Qorong'u mavzu: fon `#0A1620`, kartochka `#101F2C`, chegara `#243B4E`, urg'u `#2BD9C4`, matn `#E8F1F5` / `#8FA6B5`.
  - Yorug' mavzu: urg'u `#0A776D`, fon `#F4F8FA`.
  - Holat ranglari: soz `#5BD58A`, ogohlantirish `#F5B544`, xavfli `#FF6B6B`, aloqa yo'q `#7C93A3`. Holat rang bilan birga **shakl** bilan ham ko'rsatiladi: doira, romb, uchburchak.
  - Shriftlar: Space Grotesk (sarlavha, raqam) va Inter (matn), panel ichiga joylangan.
- **Ish uslubi.** Dastlab bosqichma-bosqich tasdiqlash kelishilgan edi. Foydalanuvchi buni o'zgartirdi: "to'liq yakuniy holatga olib kel, ertaga sinab, o'zgartirishlarni aytaman". Shu sababli 2–6-bosqichlar bir yo'la qilindi.
- **Build.** Go 1.24 bilan mos bo'lishi kerak: bog'liqliklar shunga qarab tanlangan. Ishxona build PC si Ubuntu 26, arxitekturasi noma'lum, Go hali o'rnatilmagan. Serverlar amd64 bo'lgani uchun paket amd64 uchun yig'ilgan.

### Joriy holat: qaysi bosqichdamiz?

| Bosqich | Kod | Sinov |
|---|---|---|
| 1. Poydevor | ✅ | ✅ ishlab chiqish mashinasidagi real `/proc` da |
| 2. Proxmox qatlami, tarmoq chizmasi | ✅ | ⚠️ faqat simulyatsiya |
| 3. Disklar/RAID, harorat | ✅ | ⚠️ faqat simulyatsiya; ssacli va ipmitool parserlari **hujjatdagi formatdan** yozilgan |
| 4. Guest agent, Docker | ✅ | ⚠️ simulyatsiya (soxta Docker API) |
| 5. Alertlar, webhook | ✅ | ✅ simulyatsiya: offline 30 s, RAID, VM, "tiklandi" |
| 6. Login, 2FA, yangilash, paket, hujjatlar | ✅ | ✅ unit va simulyatsiya |

**Xulosa: kod 6 bosqich bo'yicha to'liq yozilgan (v0.9.0). Endi real serverda qabul sinovi bosqichidamiz.** Foydalanuvchi 2026-11-13 kuni ishxonada real HPE/Proxmox'ga o'rnatib sinashi kerak edi. Natijalar hali chatga kelmagan.

---

## 2. Fayllar va funksiyalar tuzilmasi (oxirgi versiya, 0.9.0)

### Ildiz

| Fayl | Vazifa |
|---|---|
| `go.mod` | modul `monitor.uz`, `go 1.24.0`. Bog'liqliklar: `modernc.org/sqlite v1.46.0` (sof Go), `golang.org/x/crypto v0.45.0` (argon2). Ikkalasi ham Go 1.24 bilan mos keladigan eng yangi versiyalar |
| `vendor/` | barcha bog'liqliklar (~233 MB), offline build uchun. `go mod vendor` bilan yangilanadi |
| `VERSION` | `0.9.0` |
| `scripts/build.sh` | vet + test + statik build (CGO=0, `-mod=vendor`), so'ng `dist/monitor-uz-<v>-linux-<arch>.tar.gz` va `.sha256` |
| `scripts/install-hub.sh` | hub o'rnatish: `monitor-hub` foydalanuvchisi, `/usr/local/bin`, `/var/lib/monitor-hub`, `/etc/monitor-hub/hub.env`, systemd; oxirida setup kodini chiqaradi |
| `scripts/sim/` | **faqat sinov uchun** simulyatsiya: 3 ta soxta host va 1 guest (namespace'lar bilan). `README.md` ichida |

### `cmd/`

- **`cmd/hub/main.go`** — buyruqlar:
  - `serve`: sertifikat yaratish yoki yuklash, `secret.key`, `api.New`, `RegisterBundledAgent`, TLS server;
  - `token`, `agents`, `revoke`, `pin`;
  - `user list|add|passwd|reset-2fa|del`.

  CLI root sifatida servis bazasini ochishdan bosh tortadi (`openForCLI`): uni `sudo -u monitor-hub` bilan ishga tushirish kerak.
- **`cmd/agent/main.go`** — buyruqlar:
  - `run --mode=host|guest` (intervallar uchun bayroqlar bor; `GOMAXPROCS=2`, xotira chegarasi 40 MB host / 25 MB guest);
  - `enroll`;
  - `collect` (diagnostika: hamma ma'lumotni JSON qilib chiqaradi, hub'ga yubormaydi);
  - `version`.

### `internal/model` — agent ↔ hub turlari

- **`model.go`:**
  - `Info` (`Arch`, `Ifaces` — guest MAC lari, `Tools` bilan);
  - `Sample` (1 s namuna): `CPU`, `Load`, `Mem` (`ARC` bilan), `NetIface`, `DiskIO`, `FS`, `PSI`;
  - `IngestRequest{Info, Samples, Reports}`, `IngestResponse{Update *UpdateOrder}`, `Enroll*`;
  - `Sample.Flatten()` — seriya kalitlari: `cpu.usage`, `cpu.core[i]` (faqat xom holatda), `net[eno1].rx_bps`, `net.rx_bps` (fizik interfeyslar yig'indisi), `disk[sda].util`, `fs[/].used_pct`, `psi.*`, `load.*`, `mem.*`.
- **`sections.go`** — sekin hisobotlar:
  - `VMReport/VM/VMNet`, `HostNetReport/Bridge`, `StorageReport/Controller/LogicalDrv/PhysDisk`, `SensorReport/Sensor`, `ProcReport/Proc`, `DockerReport/Container`;
  - `Flatten` funksiyalari: `vm[ID].cpu|mem|...`, `ct[nom].cpu|...`, `temp[...]`, `fan[...]`.

### `internal/collector` — o'qish (faqat o'qiydi)

Umumiy qoida: parserlar sof funksiyalar (testlanadi); xato bo'lsa bo'lim tashlab yuboriladi va `na` ga yoziladi.

| Fayl | Asosiy funksiyalar |
|---|---|
| `counter.go` | `counterDelta` (32-bit wrap, reset), `rate`, `pct` |
| `cpu.go`, `mem.go`, `load.go`, `net.go`, `disk.go`, `psi.go`, `fs.go` | `/proc` parserlari: `parseStat`, `cpuUsage`, `parseMeminfo`, `parseARC`, `parseLoadavg`, `parseNetDev`, `skipIface` (tap/veth/fw* — host darajasida emas), `ifaceKind`, `parseDiskstats`, `wholeDisk`, `parsePSI`, `parseMountinfo`, `selectMounts` (lokal FS; NFS ataylab chiqarilgan; ZFS da faqat `/` va pool ildizi) |
| `host.go` | `Host.Collect(now)`: 1 s namuna, delta **monotonic** vaqtga bo'linadi; `ReadInfo` |
| `proxmox.go` | `Proxmox.Collect`: `/etc/pve/{qemu-server,lxc}/*.conf` (`parseVMConf`, `parseNetConf`) va cgroup v2 (`qemu.slice/<id>.scope`, `lxc/<id>`): `cpu.stat`, `memory.current`, `io.stat`. VM tarmog'i host tomondagi `tap<ID>i*` / `veth<ID>i*` dan. Uptime: cgroup ichidagi eng eski jarayon |
| `hostnet.go` | `HostNet` (bridge/bond/port/IP), `GuestIfaces` (MAC lar) |
| `ssacli.go` | `parseSSACLI` (`ssacli ctrl all show config detail` → kontroller, mantiqiy diskning a'zolari va zaxirasi, fizik disklar), `logicalState`, `physState`, `parseSize`. **Real chiqishda tekshirilmagan** |
| `smart.go` | `parseSmartJSON` (`smartctl -a -j`; "LOGICAL VOLUME" ni aniqlaydi) |
| `storage.go` | `Storage.Collect`: ssacli → smartctl `-d cciss,N` (seriya raqami bo'yicha ishlagan soatlar, 10 daqiqalik kesh) → ssacli bo'lmasa smartctl → `/proc/mdstat` → `zpool list` |
| `sensors.go` | `Sensors.Collect`: `/dev/ipmi0` va ipmitool bo'lsa `ipmitool -S sdr.cache -c sdr list` (`parseIPMICSV`), aks holda `readHwmon` |
| `procs.go` | `Procs.Collect`: `/proc/<pid>/stat` har 5 s, CPU bo'yicha top-15 va RAM bo'yicha top-5 |
| `docker.go` | `Docker.Collect`: socket orqali faqat GET (`/containers/json?all=1`, `/containers/<id>/json`); resurslar cgroup v2 dan (`system.slice/docker-<id>.scope` yoki `docker/<id>`), cgroup v1 da `stats` API; tarmoq `/proc/<pid>/net/dev` dan |
| `exec.go` | `ExecRunner` (60 s timeout, `LC_ALL=C`), `LookTool` |

### `internal/agent`

- **`agent.go`:**
  - `Run`: 1 s (guest: 5 s) namunalar bufferga tushadi;
  - sekin bo'limlar alohida goroutine'larda: VM 5 s, sensorlar 10 s, disklar 60 s, tarmoq 60 s, guest jarayonlari va Docker 5 s;
  - `pending` — oxirgi hisobotlar;
  - `sendLoop` — backoff 1 s → 10 s (401 bo'lsa 5 daqiqa);
  - `BuildInfo`.
- **`buffer.go`** — 1800 namunalik halqa bufer, TS bo'yicha `Ack`, `Save`/`Load` (gzip) qayta ishga tushganda.
- **`client.go`** — pin'langan HTTPS (`tlsutil.PinnedConfig`), proksi ishlatilmaydi; `Enroll`, `Ingest`, `Download`; `state.json` (0600).
- **`update.go`** — `applyUpdate`: yuklash → SHA-256 → `binary version` tekshiruvi → `ReplaceBinary` (`.prev` saqlanadi) → chiqish, systemd qayta ishga tushiradi.

### `internal/store` (SQLite, WAL)

- **`store.go`** — `Open` (sxema + `migrate2`), `Setting`, `SetSetting`.
- **`agents.go`** — bir martalik install token (xesh, 24 soat), `Enroll` (bir xil hostname+mode qayta o'rnatilsa ID saqlanadi), `AgentByToken`, `RevokeAgent`.
- **`metrics.go`:**
  - jadvallar: `series(id, agent_id, key)`, `raw(series_id, ts, v)` (75 daqiqa), `rollup(series_id, hour, blob 360×float32)` (1 hafta);
  - funksiyalar: `insertFlat`, `Maintain` (dirty oraliqni rollup qiladi, keyin purge; kechikib kelgan ma'lumot ham to'g'ri), `History` (1 soatgacha xom ma'lumot, undan uzog'i rollup; bucket bo'yicha o'rtacha);
  - `cpu.core[...]` rollup qilinmaydi.
- **`extra.go`:**
  - `agent_reports` (oxirgi hisobotlar), `alerts`, `users`, `sessions`, `agent_versions`, `agents.desired_version`;
  - `Box` — AES-256-GCM, kalit `secret.key` da; webhook URL va TOTP kalitlari shu bilan shifrlanadi;
  - `DeleteAgent`.

### `internal/alert`

- **`rules.go`:**
  - `Rules`, `DefaultRules`: CPU/RAM 90% 5 daqiqa; disk 85/95%; disk harorati 55 °C; qayta eslatish 60 daqiqa;
  - `Overrides`: kuzatiladigan VM (standart: `onboot` bo'lsa) va konteynerlar (standart: hammasi);
  - `Evaluate` → `Condition`. Qoidalar: `offline`, `cpu`, `ram`, `disk_usage`, `controller`, `controller_battery`, `raid`, `phys_disk`, `disk_temp`, `vm_stopped`, `container_stopped` (faqat avval ishlab turgan konteyner uchun).
- **`engine.go`** — `Engine.Tick`:
  - "for" davomiyligi, ochish, eskalatsiya, eslatish, "tiklandi";
  - agent offline bo'lsa, uning boshqa alertlari yopilmaydi;
  - `Silence`, `Health`.
- **`webhook.go`** — `Notifier`: navbat, qayta urinishlar (5 s → 30 daqiqa), `Payload` (Slack/Mattermost attachments), `Test`; xatoda URL siri yashiriladi.

### `internal/auth`

`HashPassword`/`CheckPassword` (Argon2id: m=64 MiB, t=3, p=2), `ValidatePassword` (≥10 belgi), `NewTOTPSecret`/`CheckTOTP` (RFC 6238, ±1 qadam), `Limiter`.

### `internal/api` (hub HTTP)

- **`server.go`:**
  - `New`, `Handler` (marshrutlar pastda), `Background`: offline tekshiruvi har 2 s, alertlar har 5 s (hub ishga tushgandan 35 s keyin boshlanadi), saqlash 15 s, maintenance 1 daqiqa;
  - `handleIngest` (namunalar, `ingestReports`, `updateOrder`), `handleInstallScript`, `serveDist`, `securityHeaders` (CSP `default-src 'self'`).
- **`live.go`** — `Live` (xotiradagi holat: oxirgi namuna, hisobotlar, online/offline 30 s, health), `AgentView` (+`Reports`, `Link` guest→VM MAC bo'yicha, `Desired`).
- **`sse.go`** — broker. Hodisalar: `snapshot`, `sample`, `report`, `status`, `health`, `agent`, `removed`, `backfill`, `alert`, `monitoring`.
- **`authh.go`:**
  - seans: cookie `mz_session`, 12 soat, 2 soat harakatsizlik;
  - `requireAuth`: GET bo'lmagan so'rovlar uchun `X-Requested-With: monitor` shart;
  - setup kodi (`<data>/setup-code`), login va bloklash (IP 20 ta, login 5 ta / 15 daqiqa), parol, TOTP, foydalanuvchilar.
- **`admin.go`** — tokenlar, agentni bekor qilish/o'chirish/yangilash, versiyalarni yuklash (ELF tekshiruvi) va "o'rnatish uchun" qilish, alertlar va sukut, alert sozlamalari va webhook (`urlHint`), `monitoring` overrides (copy-on-write).

**API marshrutlari:**

- Ochiq:
  - `POST /api/v1/enroll`, `POST /api/v1/ingest` (agent tokeni bilan);
  - `GET /install.sh`, `GET /dl/{name}`, `GET /dl/agent/{version}/{name}`;
  - `GET /api/v1/auth/state`, `POST /api/v1/auth/setup|login|logout`.
- Seans bilan:
  - `GET /api/v1/hub`;
  - `GET /api/v1/agents`, `GET /api/v1/agents/{id}`, `GET /api/v1/agents/{id}/history?key=..&from=&to=&points=`, `GET /api/v1/agents/{id}/keys`, `GET /api/v1/stream` (SSE);
  - `POST /api/v1/auth/password`, `POST /api/v1/auth/totp/begin|enable|disable`;
  - `GET|POST /api/v1/users`, `DELETE /api/v1/users/{id}`;
  - `POST /api/v1/tokens`, `POST /api/v1/agents/{id}/revoke|update`, `DELETE /api/v1/agents/{id}`;
  - `GET|POST /api/v1/agent-versions`, `POST /api/v1/agent-versions/{v}/default`;
  - `GET /api/v1/alerts[?state=history]`, `POST /api/v1/alerts/{id}/silence`;
  - `GET|PUT /api/v1/alert-settings`, `POST /api/v1/alert-settings/test`;
  - `GET|PUT /api/v1/monitoring`.

### `internal/tlsutil`

`EnsureSelfSigned` (ECDSA P-256, 10 yil, SAN), `SPKIPin`, `PinFromFile`, `PinnedConfig`.

### `deploy/`

- **`deploy.go`** — embed: `AgentUnit`, `GuestUnit`, `InstallAgentTmpl`.
- **`systemd/monitor-agent.service`** (host) — **root** + cheklangan capability'lar (`SYS_RAWIO`, `SYS_ADMIN`, `DAC_READ_SEARCH`, `DAC_OVERRIDE`), `ProtectSystem=full`, `ReadWritePaths=/usr/local/lib/monitor-agent`.
- **`systemd/monitor-agent-guest.service`** — `User=monitor-agent`, `SupplementaryGroups=docker`: o'rnatish skripti `{{SUPPLEMENTARY}}` o'rniga qo'yadi.
- **`systemd/monitor-hub.service`** — `EnvironmentFile=/etc/monitor-hub/hub.env`.
- **`install-agent.sh.tmpl`** — `--token`, `--mode host|guest`:
  - binary'ni hub'dan yuklaydi va SHA256 bilan tekshiradi;
  - `/usr/local/lib/monitor-agent/monitor-agent` va `/usr/local/bin` ga symlink qo'yadi;
  - `enroll` qiladi va unitni yozadi.

### `web/` (panel, `web.Files()` bilan binary ichiga embed)

| Fayl | Vazifa |
|---|---|
| `index.html` | login ekrani, layout (sidebar, topbar, `#view`), toastlar |
| `static/core.js` | `M`: `t()` (i18n), `fmt`, holat (`state.agents/alerts/overrides`), `api()` (CSRF sarlavhasi bilan), SSE `connect`, router (`#/`, `#/servers`, `#/server/:id`, `#/vms`, `#/vm/:host/:vmid`, `#/guest/:id`, `#/docker`, `#/disks`, `#/network`, `#/alerts`, `#/settings/:tab`), `M.table` (saralash), bildirishnomalar (ovoz, brauzer), mavzu |
| `static/pages-main.js` | login/setup, `chartPanel` (Jonli 5 daqiqa / 1 soat / 24 soat / 7 kun), `serverCard`, `baysHTML`, Umumiy ko'rinish, Serverlar, Server sahifasi |
| `static/pages-more.js` | `vmTable`, VM va CT, VM sahifasi, `guestSection`, Docker, Disklar, Tarmoq chizmasi (SVG, VLAN guruhlari, 6 tadan keyin yig'ib qo'yiladi), Alertlar (Faol / Tarix / Qoidalar) |
| `static/pages-settings.js` | Sozlamalar: Agentlar va versiyalar, Kuzatiladigan VM lar, Bildirishnomalar, Foydalanuvchilar, Mening hisobim (parol, 2FA), Panel |
| `static/charts.js` | `MChart.LineChart` (canvas, tooltip, bo'shliqlar), `sparkline` |
| `static/style.css` | tokenlar (qorong'u/yorug'), komponentlar, responsive. **Inline style ishlatilmaydi:** CSP tufayli kenglik `data-w` / `data-bg` + `M.applyStyles` orqali beriladi |
| `i18n/uz.json` | 456 ta kalit; barcha panel matnlari shu yerda |
| `static/fonts/` | Inter, Space Grotesk (woff2, OFL) |

### `docs/`

| Fayl | Mazmuni |
|---|---|
| `QOLLANMA.md` | to'liq bosqichma-bosqich o'rnatish (PDF versiyasi foydalanuvchiga berilgan) |
| `ORNATISH.md` | qisqa ma'lumotnoma |
| `HISOBOT.md` | sinov hisoboti, cheklovlar, real serverda tekshiriladigan narsalar |
| `PASPORT.md` | shu hujjat |

### Serverdagi yo'llar

- **Hub:** `/var/lib/monitor-hub/` ichida `hub.db`, `hub.crt`, `hub.key`, `secret.key`, `setup-code` (admin yaratilguncha), `dist/` (agent fayllari, `SHA256SUMS`, `VERSION`, `agent/<ver>/`); sozlamalar `/etc/monitor-hub/hub.env`.
- **Agent:** `/var/lib/monitor-agent/` ichida `state.json`, `buffer.json.gz`, `sdr.cache`.

---

## 3. Qayerda to'xtadik / mavjud muammolar

**To'xtagan joy.** Foydalanuvchiga quyidagilar berilgan:

- v0.9.0 paketi (eng oxirgisining SHA256: `9102b81265f7168079c9ba004be4b71ed7fded9c7dbe98d3d8e9d6bab7be06ed`, ichida `docs/QOLLANMA.md` bor);
- PDF qo'llanma.

U ishxonada real serverlarga o'rnatib sinashi kerak edi. **Sinov natijalari hali kelmagan.** Ochiq bug ham yo'q: barcha testlar o'tgan.

**Real serverda tekshirilmagan (xavf tartibida):**

1. **`parseSSACLI`** — haqiqiy `ssacli ctrl all show config detail` chiqishi kerak (firmware/versiyaga qarab format farq qilishi mumkin). Tekshirish: Disklar sahifasini haqiqiy bay/RAID holati bilan solishtirish.
2. **`parseIPMICSV`** — HPE iLO 5 dagi `ipmitool -c sdr list` formati; `/dev/ipmi0` borligi (`ipmi_si`, `ipmi_devintf` modullari).
3. **smartctl `-d cciss,N`** Gen10 Smart Array (hpsa) da ishlashi va seriya raqami ssacli dagi bilan mos kelishi.
4. **systemd unitlari** (`ProtectSystem=full` + capability'lar) ssacli/smartctl/ipmitool ga to'sqinlik qilmasligi. Bu muhitda systemd yo'q edi, `install-hub.sh` va `install.sh` real tizimda ishga tushirilmagan.
5. **Proxmox VM RAM:** cgroup `memory.current` ham QEMU overhead'ini o'z ichiga oladi, Proxmox UI bilan farq qilishi mumkin.
6. **Guest → VM bog'lanishi:** MAC bo'yicha; bond yoki macvlan bo'lsa, bog'lanmasligi mumkin.
7. **Resurs sarfi:** ko'p yadroli real hostda o'lchanmagan. Simulyatsiyada: host agent ~0.25% CPU va ~13 MB, guest 0.11%, hub ~28 MB.
8. **Baza hajmi** 6 host va ~90 VM uchun 1 haftada: taxminan bir necha GB, real o'lchanmagan.

**Ma'lum cheklovlar (ataylab qilingan, foydalanuvchiga aytilgan):**

- 2FA da QR kod yo'q (kalit qo'lda kiritiladi);
- hub o'chiq paytda VM/Docker hisobotlari buferlanmaydi, faqat 1 s namunalar buferlanadi;
- cgroup v1 dagi VM larda konteynerning tarmoq va I/O tezligi ko'rinmaydi;
- webhook matnlari Go kodida o'zbekcha yozilgan;
- fayl tanlash tugmasi brauzerning o'z yozuvi ("Choose File").

**TZ da so'ralmagan, lekin qo'shilgan narsalar** (foydalanuvchiga aytilgan, e'tiroz bildirilmagan): ZFS/mdadm holati, kontroller batareyasi uchun alert, agentning `collect` diagnostika buyrug'i, `scripts/sim/`.

---

## 4. Next steps

### Yangi chatda birinchi xabar (nusxa olib yuboring)

> Monitor.uz loyihasini davom ettiramiz. Repozitoriy: javoxiro7o3/Monitor.uz, branch `claude/new-session-hz54sa`. Avval `docs/PASPORT.md` va `docs/HISOBOT.md` ni o'qi, keyin `go test ./...` ni ishga tushirib, holatni tasdiqla. Men bilan o'zbekcha gaplash; kod, nomlar va commit'lar inglizcha. Hujjatlardagi kelishilgan qarorlarni o'zboshimchalik bilan o'zgartirma.
>
> Real serverdagi sinov natijalari: [bu yerga yozing: nima ishladi, nima ishlamadi, xato matnlari]. Ilova qilingan fayllar: ssacli.txt, ipmi.txt, collect.json, agent.log [bor bo'lsa].
>
> Vazifa: shu natijalarga qarab parserlar va o'rnatishdagi muammolarni tuzat, testlarni real namunalar bilan yangila, versiyani 0.9.1 qilib yangi paket yig'.

Agar sinov hali o'tkazilmagan bo'lsa, ikkinchi xatboshi o'rniga quyidagini yozing: "Hali sinamadim; [bajariladigan yangi vazifa]".

### Yangi chatga olib boriladigan fayllar

Faqat qo'lingizda bo'lganlari. Hammasini QOLLANMA, 9-bo'limdagi buyruqlar beradi:

- `ssacli.txt`, `smart0.json`, `ipmi.txt`, `collect.json`, `agent.log`, `hub.log`;
- `pveversion` va `lspci` natijalari;
- muammoli ekranlar skrinshoti.

Asl topshiriq fayllari (PROMPT.docx, TZ.docx, dizayn PDF) repozitoriyda yo'q. Ularning asosiy qarorlari yuqoridagi 1-bo'limda jamlangan. Shunga qaramay, zarur bo'lsa ularni ham yangi chatga yuklang.

### Keyingi ish tartibi (yangi chat uchun)

1. Real chiqishlarni `internal/collector/testdata/` ga qo'shib, parser testlarini real namunalar bilan yangilash (ayniqsa `ssacli`, `ipmitool`).
2. O'rnatishda topilgan xatolarni tuzatish (systemd, skriptlar).
3. Resurs sarfini real hostda o'lchab, `HISOBOT.md` ga yozish.
4. Versiyani `0.9.1` qilish: `VERSION`, `./scripts/build.sh`, paket.
   - Agentlarni panel orqali yangilash: Sozlamalar → Agentlar → Agent versiyalari.
   - Hub'ni yangilash: `./install-hub.sh` ni qayta ishga tushirish.
5. Qabul mezonlari (QOLLANMA, 6-bo'lim) to'liq bajarilgach: `main` branchga PR (foydalanuvchi so'raganda).

### Foydali buyruqlar

```sh
go test ./...                           # barcha testlar (vendor bilan, internetsiz)
./scripts/build.sh                      # paket (dist/)
SIM_DIR=/tmp/monitor-sim scripts/sim/run.sh   # simulyatsiya: avval scripts/sim/README.md ga qarang
```

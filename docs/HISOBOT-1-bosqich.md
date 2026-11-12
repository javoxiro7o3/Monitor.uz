# 1-bosqich (poydevor): hisobot

Versiya 0.1.0. Sana: 2026-11-12.

## Nima qilindi

- **Host agent** (`monitor-agent run --mode=host`). Har 1 soniyada quyidagi manbalardan o'qiydi:
  - `/proc/stat` — CPU umumiy va har yadro bo'yicha;
  - `/proc/meminfo` — RAM va swap; ZFS bo'lsa ARC ham (`/proc/spl/kstat/zfs/arcstats`);
  - `/proc/loadavg`, `/proc/uptime`;
  - `/proc/net/dev` — trafik, paket, xato, tashlangan;
  - `/proc/diskstats` — o'qish/yozish, IOPS, bandlik, kutish;
  - `/proc/pressure/*` — PSI;
  - `statvfs` — fayl tizimlari bandligi, har 10 s.
- **Delta hisoblash.** Hisoblagichlar farqi haqiqiy o'tgan vaqtga bo'linadi: Go'ning monotonic soati, `time.Now()` bilan. 32-bitli counter aylanib ketishi hisobga olinadi, 64-bitli counter'ning qaytishi esa "reset" deb hisoblanib, o'sha interval tashlab yuboriladi.
- **Xatoga chidamlilik.** Biror fayl o'qilmasa, agent yiqilmaydi: o'sha bo'lim namunada bo'lmaydi va `na` ro'yxatiga yoziladi. Panelda "Mavjud emas: …" deb ko'rinadi.
- **Filtrlar.**
  - Proxmox'ning VM/CT interfeyslari (`tap*`, `veth*`, `fw*`) host darajasida ko'rsatilmaydi, ular 2-bosqichda VM/CT ga bog'lanadi.
  - Faqat butun disklar olinadi (partition, LVM/dm, zvol, loop emas).
  - Fayl tizimlaridan faqat lokallari olinadi. NFS/CIFS ataylab o'tkazib yuboriladi: o'lik NFS'da `statvfs` agentni osib qo'yishi mumkin.
  - ZFS'da faqat `/` va pool ildizlari olinadi (hostdagi har bir CT subvolume emas).
- **Hub.**
  - Bir martalik o'rnatish tokeni orqali ro'yxatdan o'tkazish. Token xeshlangan holda saqlanadi, 24 soat amal qiladi va bir marta ishlatiladi.
  - Har agentga alohida doimiy token beriladi va uni bekor qilish mumkin.
  - Qabul API (gzip JSON), SQLite (1 soat xom + 1 hafta 10 s o'rtacha), oxirgi qiymatlar va tarix API'si, SSE jonli oqim.
  - 30 soniya ma'lumot kelmasa, agent "offline" deb belgilanadi.
- **Xavfsizlik.**
  - Hub o'zining self-signed sertifikatini yaratadi. Agentlar uni SPKI pin orqali tekshiradi, o'rnatish skripti ham `curl --pinnedpubkey` bilan olinadi.
  - Panelga CSP qo'yilgan (`default-src 'self'`): brauzer tashqi manzilga umuman murojaat qila olmaydi.
- **Panel.**
  - Umumiy ko'rinish (server kartochkalari) va server sahifasi: KPI'lar; CPU, xotira, tarmoq, disk I/O, PSI va load grafiklari; yadrolar; interfeyslar, disklar va fayl tizimlari jadvallari.
  - Vaqt oralig'i: Jonli (5 daq) / 1 soat / 24 soat / 7 kun.
  - Qorong'u va yorug' mavzu, telefon ko'rinishi.
  - Aloqa uzilsa, "Aloqa uzildi" belgisi chiqadi; panel o'zi qayta ulanib, tarixni qayta yuklaydi.
  - Barcha matnlar `web/i18n/uz.json` da.
- **O'rnatish.** systemd unitlar, `install-hub.sh` va hub beradigan `install.sh`. Hammasi internetsiz ishlaydi.

## Tekshirilgan

Sinov ishlab chiqish mashinasida o'tkazildi (Ubuntu 24.04, 4 vCPU, kernel 6.18), **Proxmox hostda emas**.

| Tekshiruv | Natija |
|---|---|
| `go vet`, `gofmt` | toza |
| Unit testlar | parserlar real `/proc` namunalarida; delta/wrap; saqlash/rollup/retention; kechikkan ma'lumot; bufer; API (TLS + pin + enroll + ingest + SSE + offline) |
| Pin | to'g'ri pin bilan skript yuklanadi; noto'g'ri pin bilan curl ham, agent ham ulanishni rad etadi |
| Binary yaxlitligi | hub bergan agent fayli `SHA256SUMS` ga mos |
| Jonli grafiklar | SSE orqali har 1 soniyada, sahifa qayta yuklanmaydi |
| Offline holat | agent to'xtatilgandan **31 s** keyin panelga "offline" keldi |
| Hub 20 s o'chdi | agent buferladi va hub qaytgach yetkazdi: 90 s oraliqda 90/90 nuqta, bo'shliq yo'q |
| Hub ham, agent ham o'chdi | agent buferni diskka saqladi (15 namuna), qayta ishga tushgach tikladi va yetkazdi |
| Panel ochilishi | ~30–100 ms (lokal), tashqi so'rov 0 ta, JS xatolari yo'q |
| **Agent resursi** | 173 s davomida CPU o'rtacha **0.20%** (bitta yadrodan), RSS **12.4 MB**, 7 ta thread |

## Tekshirilmadi (ertaga ishxonada)

- Proxmox VE hostida ishlashi: real `/proc/diskstats`, HPE Smart Array (`hpsa`) disk nomlari, `vmbr`/bond interfeyslarining turi, ZFS ARC qiymatlari.
- Ko'p yadroli serverda agent resursi. Maqsad: CPU < 1%, RAM < 50 MB.
- systemd unitlari Debian 12/13'da: bu muhitda systemd yo'q edi, shuning uchun unitlar va `install-*.sh` skriptlari ishga tushirib ko'rilmadi.
- 6 host bilan bir haftalik ishlash va baza hajmi.

## Ertangi sinov tartibi

1. Hub VM/CT da: `sudo ./install-hub.sh --url https://HUB_IP:8443`.
2. `sudo -u monitor-hub monitor-hub token --note srv-01` → chiqqan buyruqni bitta Proxmox hostda bajaring.
3. Brauzerda panelni oching va grafiklar har soniyada yangilanayotganini tekshiring.
4. Hostda `systemctl stop monitor-agent` → ~30 s ichida panelda "Aloqa yo'q" chiqishi kerak. Keyin `start` qiling.
5. Agent resursini o'lchang (15–30 daqiqa ishlagandan keyin):
   ```sh
   PID=$(systemctl show -p MainPID --value monitor-agent)
   ps -o pid,etime,time,rss,nlwp -p $PID
   systemctl status monitor-agent | grep -E "Memory|CPU"
   ```
6. Agent nimani ko'rayotganini tekshirish va menga yuborish uchun:
   ```sh
   pveversion; uname -r
   monitor-agent collect > /tmp/collect.json
   cat /proc/diskstats; cat /proc/net/dev; ls /sys/class/net; ls /sys/block
   ```

### 3-bosqich (disklar) uchun hozirdan to'plab qo'yish mumkin

Bu buyruqlar faqat o'qiydi:

```sh
dmidecode -s system-product-name
lspci | grep -i -E 'raid|smart array|sas'
ssacli ctrl all show config          # ssacli o'rnatilgan bo'lsa
smartctl --scan
ls -l /dev/ipmi* 2>/dev/null; lsmod | grep -E 'ipmi|hpilo'
```

## Ochiq masalalar

- Panel loginsiz (login 6-bosqichda). Hozircha 8443-portni faqat adminlar tarmog'idan oching.
- Agent'ni paneldan yangilash yo'q (6-bosqich). Hozircha o'rnatish buyrug'ini qayta bajarish kerak.
- Server holati hozircha faqat "Aloqada / Aloqa yo'q". "Soz / ogohlantirish / xavfli" alert qoidalari bilan birga 5-bosqichda qo'shiladi.

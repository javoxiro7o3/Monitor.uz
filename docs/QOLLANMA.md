# Monitor.uz — o'rnatish qo'llanmasi

Versiya 0.9.0. Bu qo'llanma tizimni noldan, internetsiz o'rnatish uchun: hub, har bir Proxmox hostdagi agent va Docker bor VM/CT lardagi guest agentlar. Buyruqlar ketma-ket berilgan; ularni tartib bilan bajaring.

---

## 0. Umumiy sxema

```
                 ┌──────────────────────────────┐
  Admin brauzeri │  Hub (srv-01 dagi VM yoki CT) │◄── Mattermost (webhook, keyinroq)
  https://HUB:8443│  monitor-hub, SQLite, panel   │──► webhook xabarlari
                 └──────────────▲───────────────┘
                                │ HTTPS 8443 (faqat agentdan hub'ga)
        ┌───────────────────────┼─────────────────────────┐
  Proxmox srv-01 … srv-06       │              Docker bor VM / CT lar
  host agent (root)  ───────────┘              guest agent (monitor-agent user)
  /proc, /etc/pve, cgroup, ssacli,             /proc, Docker socket (faqat o'qish)
  smartctl, ipmitool
```

- Ulanishlarni faqat agentlar ochadi: ular hub'ga **8443/TCP** orqali ulanadi. Hub agentlarga ulanmaydi, hostlarda yangi port ochilmaydi.
- Agent faylini, o'rnatish skriptini va sertifikat tekshiruvini (pin) hammasini hub beradi, internet kerak emas.
- O'rnatish tartibi: **1 → Hub**, **2 → Host agentlar**, **3 → Guest agentlar**, **4 → Bildirishnomalar**, **5 → Yakuniy tekshiruv**.

---

## 1. Tayyorgarlik (uyda, internet bor joyda)

### 1.1. Olib boriladigan fayllar

| Fayl | Qayerdan | Nima uchun |
|---|---|---|
| `monitor-uz-0.9.0-linux-amd64.tar.gz` | Claude yuborgan | hub, agentlar, hujjatlar |
| `ssacli-*.deb` | HPE Support Center yoki HPE MCP ombori (downloads.linux.hpe.com → SDR → mcp → Debian) | RAID va disklar (Smart Array) |
| `ipmitool` va bog'liqliklari (`.deb`) | Proxmox bilan bir xil Debian versiyasidagi internetli mashinada (pastga qarang) | harorat, fan, quvvat bloki (ixtiyoriy) |

Paketning SHA-256 xeshini ham yozib oling: u Claude yuborgan xabarda va paket bilan birga keladigan `.sha256` faylida bo'ladi. Ishxonada `sha256sum` natijasi shu xesh bilan bir xil bo'lishi kerak.

**ipmitool `.deb` larini yuklab olish.** Proxmox VE 8 Debian 12 (bookworm) ustida, Proxmox VE 9 esa Debian 13 (trixie) ustida ishlaydi. Shu versiyadagi internetli Debian mashinada (yoki vaqtinchalik VM'da):

```sh
mkdir -p ~/debs/partial && cd ~/debs
apt-get update
apt-get install --download-only -y -o Dir::Cache::archives=$PWD ipmitool
ls *.deb        # shu fayllarning hammasini USB ga ko'chiring
```

> `--download-only` faqat shu mashinada **o'rnatilmagan** bog'liqliklarni yuklaydi. Shuning uchun toza, minimal Debian (Proxmox bilan bir xil versiya) ishlatgan ma'qul. Ishxonada `dpkg -i` biror bog'liqlik yo'qligini aytsa, o'sha paketni ham shu usul bilan yuklab oling.

> `smartctl` (smartmontools) va `curl` Proxmox'da odatda o'rnatilgan bo'ladi. Ishxonada `smartctl --version` va `curl --version` bilan tekshiring. Yo'q bo'lsa, ularni ham yuqoridagi usulda yuklab oling.

### 1.2. Manzillar jadvali (oldindan to'ldiring)

| Nima | Qiymat (namuna) | Sizniki |
|---|---|---|
| Hub IP manzili | 10.0.0.5 | |
| Hub porti | 8443 | |
| Hub VM/CT ID (srv-01 da) | 150 | |
| srv-01 … srv-06 IP | 10.0.0.11 … | |
| Docker bor VM/CT lar | vm-app-1 (srv-02, ID 200) … | |
| Admin loginlari (2–3 ta) | admin, … | |
| Mattermost webhook manzili | (keyinroq) | |

### 1.3. Tarmoq talablari

- Har bir Proxmox host va har bir guest VM/CT **hub IP:8443** ga TCP orqali yeta olishi kerak.
- VLAN lar orasida bo'lsa, OPNsense'da ruxsat qoidasi kerak: manba VLAN 20/30 → hub IP, port 8443/TCP.
- Adminlar kompyuteri hub:8443 ga yeta olishi kerak.
- Hamma serverlarda **vaqt to'g'ri** bo'lishi kerak (NTP, masalan OPNsense orqali). Vaqt noto'g'ri bo'lsa, grafiklar siljiydi va 2FA kodi ishlamaydi.

---

## 2. Hub o'rnatish (srv-01 da)

### 2.1. VM yoki CT yaratish

Proxmox web UI da srv-01 ga yangi **Debian 12 yoki 13** VM yoki CT yarating. Mavjud shablon yoki ISO'dan foydalaning (internetdan yuklab bo'lmaydi):

- 2 vCPU, 2 GB RAM, 20 GB disk;
- statik IP (1.2-jadvaldagi), to'g'ri gateway va DNS;
- CT bo'lsa, **unprivileged** yetarli, `nesting` kerak emas;
- "Start at boot" yoqilgan bo'lsin.

> Disk hajmi taxminan shunday baholangan: 6 host va ~90 VM uchun 1 haftalik tarix bir necha GB atrofida. Real hajmni 1 haftadan keyin `du -sh /var/lib/monitor-hub` bilan tekshiring.

### 2.2. Paketni hub'ga ko'chirish

**A usul: CT uchun, Proxmox host orqali.** Fleshkani srv-01 ga ulang:

```sh
# srv-01 da (root):
lsblk                                   # fleshka qurilmasini toping, masalan /dev/sdc1
mount /dev/sdc1 /mnt
sha256sum /mnt/monitor-uz-0.9.0-linux-amd64.tar.gz   # yozib olingan xesh bilan solishtiring
pct push 150 /mnt/monitor-uz-0.9.0-linux-amd64.tar.gz /root/monitor-uz-0.9.0-linux-amd64.tar.gz
umount /mnt
pct enter 150                           # CT ichiga kirish
```

**B usul: VM yoki boshqa kompyuterdan.** `scp monitor-uz-0.9.0-linux-amd64.tar.gz root@10.0.0.5:/root/`

### 2.3. O'rnatish

Hub ichida (root):

```sh
cd /root
tar xzf monitor-uz-0.9.0-linux-amd64.tar.gz
cd monitor-uz-0.9.0-linux-amd64
./install-hub.sh --url https://10.0.0.5:8443
```

`--url` — agentlar va brauzer hub'ga murojaat qiladigan manzil, ya'ni hub'ning IP manzili. Agar hub'ga DNS nom orqali ham kirmoqchi bo'lsangiz, `--san monitor.lan` qo'shing.

Natijada oxirida shunga o'xshash yozuvlar chiqadi:

```
Hub ishlayapti: https://10.0.0.5:8443
Pin: 7WH2OoP4qicw...=
Birinchi admin uchun o'rnatish kodi: 4K7QX2...
```

Tekshirish:

```sh
systemctl status monitor-hub          # "active (running)" bo'lishi kerak
ss -tlnp | grep 8443                  # hub 8443 ni tinglayapti
journalctl -u monitor-hub -n 30
```

### 2.4. Birinchi admin

1. Admin kompyuterida brauzerda `https://10.0.0.5:8443` ni oching.
2. Brauzer sertifikat haqida ogohlantiradi: **"Qo'shimcha" → "Baribir davom etish"** ni bosing. Bu kutilgan holat, chunki hub o'z sertifikatidan foydalanadi.
3. "Birinchi adminni yaratish" oynasida o'rnatish kodini kiriting. Kodni keyin ham ko'rish mumkin: `cat /var/lib/monitor-hub/setup-code`.
4. Login va kamida 10 belgili parol kiriting, so'ng **Yaratish va kirish** ni bosing.
5. Ikkinchi va uchinchi adminlarni qo'shing: **Sozlamalar → Foydalanuvchilar**.
6. Har bir admin o'zi uchun 2FA ni yoqishi tavsiya etiladi: **Sozlamalar → Mening hisobim → 2FA ni yoqish**.
   - Telefondagi authenticator ilovada (Google Authenticator, FreeOTP va hokazo) "kalitni qo'lda kiritish" ni tanlang va ko'rsatilgan kalitni yozing.
   - So'ng ilovadagi 6 raqamli kodni panelga kiriting.

### 2.5. Zaxira nusxa (birinchi kunning o'zida)

```sh
systemctl stop monitor-hub
tar czf /root/monitor-hub-backup-$(date +%F).tar.gz /var/lib/monitor-hub /etc/monitor-hub
systemctl start monitor-hub
```

Arxivni hub'dan tashqariga ko'chiring. Undagi eng muhim fayllar:

- `hub.key` / `hub.crt` — yo'qolsa, barcha agentlarni qayta o'rnatish kerak bo'ladi;
- `secret.key` — webhook manzili va 2FA kalitlari shu kalit bilan shifrlangan.

---

## 3. Host agent: har bir Proxmox serverga (srv-01 … srv-06)

Har bir server uchun quyidagi **3.1 → 3.4** qadamlarni takrorlang. Avval bitta serverda (masalan, srv-01) to'liq bajaring va tekshiring, keyin qolganlariga o'ting.

### 3.1. Vositalarni o'rnatish (bir marta)

Fleshkani hostga ulang (`mount /dev/sdX1 /mnt`) va quyidagilarni bajaring.

**ssacli** — RAID va disklar uchun:

```sh
dpkg -i /mnt/ssacli-*.deb
ssacli ctrl all show status            # "Controller Status: OK" chiqishi kerak
```

**ipmitool** — harorat, fan va quvvat bloki uchun (ixtiyoriy):

```sh
dpkg -i /mnt/debs/*.deb
modprobe ipmi_si ipmi_devintf          # in-band IPMI drayverlari
ls -l /dev/ipmi0                       # fayl paydo bo'lishi kerak
printf 'ipmi_si\nipmi_devintf\n' > /etc/modules-load.d/ipmi.conf   # qayta yuklanganda ham
ipmitool sdr list | head               # harorat qiymatlari chiqishi kerak
```

Agar `/dev/ipmi0` paydo bo'lmasa yoki `ipmitool` xato bersa, bu bo'limni o'tkazib yuboring. Panel hwmon orqali CPU haroratini baribir ko'rsatadi.

**smartctl** — tekshirish:

```sh
smartctl --version | head -1
```

### 3.2. Token olish (panelda)

1. **Sozlamalar → Agentlar → Yangi agent qo'shish** ga kiring.
2. Izohga server nomini yozing (masalan, `srv-01`), Rejim: **Host (Proxmox)**, muddat 24 soat.
3. **Token yaratish** ni bosing va chiqqan buyruqni **Nusxa** tugmasi bilan oling.

Buyruq quyidagicha ko'rinadi:

```sh
curl -fsS --insecure --pinnedpubkey 'sha256//PIN' https://10.0.0.5:8443/install.sh | sh -s -- --token TOKEN
```

> Har bir token **bir marta** ishlatiladi. Har bir server uchun yangi token yarating.

### 3.3. O'rnatish (hostda, root)

Buyruqni Proxmox host konsolida (web UI → Shell yoki SSH) ishga tushiring. Oxirida shunday yozuv chiqishi kerak:

```
==> agent yuklanmoqda (amd64, host)
==> fayllar
==> hub'da ro'yxatdan o'tish
Ro'yxatdan o'tildi: agent ID 1 (srv-01)
==> systemd xizmati
Tayyor. Agent ishlayapti; panelda bir necha soniyada ko'rinadi.
```

Skript `ssacli`, `smartctl` yoki `ipmitool` topilmasa, "eslatma" chiqaradi. Bu xato emas: shunchaki tegishli bo'lim panelda bo'sh bo'ladi.

### 3.4. Tekshirish

```sh
systemctl status monitor-agent        # active (running)
journalctl -u monitor-agent -n 30     # "agent started", xatolar yo'q
monitor-agent collect | less          # agent ko'rayotgan hamma narsa (JSON, hub'ga yuborilmaydi)
```

Panelda quyidagilarni tekshiring:

- **Umumiy ko'rinish:** server kartochkasi paydo bo'lgan, CPU/RAM har soniyada yangilanyapti.
- **Server sahifasi:** grafiklar, VM/CT ro'yxati, "Disklar va RAID", "Harorat, fan, quvvat bloki".
- **Disklar:** disklar soni, bay raqamlari, RAID turi va a'zolari **haqiqiy holatga mos**. Buni ssacli chiqishi bilan solishtiring.
- **VM va CT:** ro'yxat Proxmox web UI dagi bilan bir xil.

Biror narsa noto'g'ri bo'lsa, 9-bo'limdagi ma'lumotlarni to'plang.

---

## 4. Guest agent: Docker bor VM/CT larga

Guest agent faqat **Docker ishlaydigan** Linux VM yoki Debian CT larga o'rnatiladi. U VM ichidagi resurslarni, eng ko'p resurs ishlatayotgan jarayonlarni va konteynerlarni ko'rsatadi. Windows VM larga o'rnatilmaydi.

### 4.1. Talablar (VM/CT ichida tekshiring)

```sh
systemctl --version | head -1          # systemd bor
curl --version | head -1               # curl bor (bo'lmasa: ichki ombordan apt install curl)
getent group docker                    # docker guruhi bor
curl -k -sS -o /dev/null -w '%{http_code}\n' https://10.0.0.5:8443/install.sh   # 200 chiqishi kerak (hub'ga yetib boriladi)
```

Oxirgi buyruq `200` bermasa, VLAN va firewall (OPNsense) qoidasini tekshiring (1.3).

### 4.2. Token va o'rnatish

1. Panelda **Sozlamalar → Agentlar** ga kiring. Izohga VM nomini yozing, Rejim: **Guest (VM/CT)**, so'ng **Token yaratish** ni bosing.
2. Chiqqan buyruqni (oxirida `--mode guest` bo'ladi) VM/CT ichida **root** sifatida ishga tushiring:

```sh
curl -fsS --insecure --pinnedpubkey 'sha256//PIN' https://10.0.0.5:8443/install.sh | sh -s -- --token TOKEN --mode guest
```

- Guest agent root'siz, `monitor-agent` foydalanuvchisi nomidan ishlaydi.
- Docker'ga `docker` guruhi orqali faqat o'qish uchun murojaat qiladi.

### 4.3. Tekshirish

```sh
systemctl status monitor-agent
journalctl -u monitor-agent -n 30
```

Panelda:

- **VM va CT** → shu VM sahifasi → "Ichki holat (guest agent)" bo'limida jarayonlar va konteynerlar ko'rinadi.
- **Docker** sahifasida konteynerlar ro'yxati ko'rinadi.
- Guest agent Proxmox'dagi VM'ga **MAC manzil** bo'yicha avtomatik bog'lanadi. Bog'lanmasa, u "VM va CT" sahifasi pastidagi "VM ga bog'lanmagan guest agentlar" bo'limida chiqadi. Bunda VM ichidagi `ip link` dagi MAC'ni Proxmox'dagi `netX` MAC bilan solishtiring.

Har bir Docker bor VM/CT uchun 4.2–4.3 ni takrorlang.

---

## 5. Bildirishnomalar (Mattermost)

Mattermost o'rnatilgandan keyin:

1. Mattermost'da: **Main Menu → Integrations → Incoming Webhooks → Add Incoming Webhook**, kanalni tanlang va manzilni nusxalang.
2. Panelda: **Sozlamalar → Bildirishnomalar** ga kiring va quyidagilarni bajaring:
   - "Bildirishnomalar yoqilgan" ni belgilang;
   - webhook manzilini kiriting;
   - Mattermost o'z sertifikatidan foydalansa, "TLS sertifikatini tekshirmaslik" ni belgilang;
   - **Sinov xabari** ni bosing → Mattermost kanalida xabar paydo bo'lishi kerak;
   - **Saqlash** ni bosing.
3. Mattermost hali yo'q bo'lsa, istalgan kompyuterda oddiy qabul qiluvchi bilan sinab ko'ring: `nc -l -p 9000` (manzil `http://IP:9000/`).

Alert qoidalari va chegaralari: **Alertlar → Qoidalar**. Standart qiymatlar TZ dagidek:

- CPU/RAM 90% dan yuqori, 5 daqiqa davomida;
- disk bandligi 85% (ogohlantirish) / 95% (xavfli);
- disk harorati 55 °C dan yuqori;
- qayta eslatish har 60 daqiqada.

**Kuzatiladigan VM lar:** **Sozlamalar → Kuzatiladigan VM lar** (yoki "VM va CT" ro'yxatidagi tugma). Standart holatda avtomatik ishga tushadigan (onboot) VM/CT lar kuzatiladi. Kuzatiladigan VM to'xtasa, xavfli alert keladi.

---

## 6. Yakuniy tekshiruv (TZ, 12-bo'lim)

| # | Tekshiruv | Qanday | ✓ |
|---|---|---|---|
| 1 | Barcha serverlar panelda, grafiklar jonli | Umumiy ko'rinish | ☐ |
| 2 | Disklar soni, nosozlari va RAID tarkibi haqiqiyga mos | Disklar ↔ `ssacli ctrl all show config` | ☐ |
| 3 | VM/CT ro'yxati va resurslari Proxmox'dagiga mos | VM va CT ↔ Proxmox UI | ☐ |
| 4 | Tanlangan VM larda konteynerlar va ularning CPU/RAM sarfi ko'rinadi | Docker | ☐ |
| 5 | Tarmoq chizmasida bridge, VLAN, VM to'g'ri bog'langan | Tarmoq chizmasi | ☐ |
| 6 | Agent o'chirilsa, 1 daqiqa ichida messenjerga xabar keladi | hostda `systemctl stop monitor-agent`, keyin `start` | ☐ |
| 7 | RAID/disk nosozligi alert beradi | real nosoz disk bo'lsa, panel/alert; simulyatsiya — 9-bo'lim | ☐ |
| 8 | Panelga faqat login bilan kirish mumkin, 2–3 admin ishlaydi | boshqa brauzerda kirib ko'ring | ☐ |
| 9 | Barcha matnlar o'zbekcha | ko'z bilan | ☐ |
| 10 | Internetsiz o'rnatildi, tashqi so'rov yo'q | brauzerda F12 → Network | ☐ |
| 11 | Host agent sarfi: CPU < 1%, RAM < 50 MB | 30 daqiqadan keyin `systemctl status monitor-agent` | ☐ |
| 12 | 1 haftalik tarix grafikda | 1 haftadan keyin "7 kun" oralig'i | ☐ |

---

## 7. Agentni yangilash va qaytarish

1. Yangi `monitor-agent` faylini **Sozlamalar → Agentlar → Agent versiyalari** orqali yuklang (versiya raqamini to'g'ri yozing).
2. **Avval bitta agentni** "Yangilash…" ro'yxatidan yangi versiyaga o'tkazing.
   - Agent faylni hub'dan oladi, SHA-256 va versiyasini tekshiradi, o'zini almashtiradi va qayta ishga tushadi (eski fayl `.prev` bo'lib qoladi).
3. Hammasi joyida bo'lsa, qolganlarini ham yangilang. Muammo bo'lsa, eski versiyani xuddi shu yo'l bilan tanlang.
4. "O'rnatish uchun tanlash" tugmasi yangi o'rnatishlarda qaysi versiya ishlatilishini belgilaydi.

**Hub'ni yangilash:**

```sh
tar xzf monitor-uz-NEW-linux-amd64.tar.gz && cd monitor-uz-NEW-linux-amd64
./install-hub.sh                         # --url shart emas, sozlamalar va ma'lumotlar saqlanadi
```

---

## 8. Muammolar va yechimlar

| Belgi | Sabab | Yechim |
|---|---|---|
| `curl: (90) SSL: public key does not match pinned public key` | buyruqdagi pin hub sertifikatiga mos emas | tokenni paneldan qayta yarating (buyruq to'g'ri pin bilan chiqadi); hub sertifikati almashtirilgan bo'lishi mumkin |
| `curl: (7) Failed to connect` / timeout | hub'ga tarmoq orqali yetib bo'lmayapti | hub ishlayaptimi (`systemctl status monitor-hub`), firewall/VLAN qoidasi (1.3), IP to'g'rimi |
| `XATO: ro'yxatdan o'tib bo'lmadi … install token is invalid, expired or already used` | token ishlatilgan yoki muddati o'tgan | yangi token yarating |
| Agent panelda "Aloqa yo'q" | agent to'xtagan yoki tarmoq uzilgan | hostda `systemctl status monitor-agent`, `journalctl -u monitor-agent -n 50` |
| Disklar bo'limi: "ssacli va smartctl topilmadi" | ssacli o'rnatilmagan | 3.1 |
| Disklar bo'limi: "ssacli: exit code …" | ssacli ishlamadi | hostda `ssacli ctrl all show config detail` ni qo'lda ishga tushiring, chiqishni yuboring (9) |
| Harorat bo'limi bo'sh yoki faqat hwmon | `/dev/ipmi0` yo'q | 3.1 (modprobe); bo'lmasa — kutilgan holat |
| Guest agent VM ga bog'lanmagan | MAC mos emas (bond, macvlan va hokazo) | VM ichida `ip link`, Proxmox'da VM → Hardware → Network; natijani yuboring |
| Guest: "Docker topilmadi yoki ulanib bo'lmadi" | docker guruhi o'rnatishdan keyin yaratilgan | o'rnatish buyrug'ini qayta bajaring (yangi token bilan) |
| 2FA kodi qabul qilinmaydi | telefon yoki hub vaqti noto'g'ri | ikkalasida vaqtni to'g'rilang; zarur bo'lsa: `sudo -u monitor-hub monitor-hub user reset-2fa LOGIN` |
| Parol esdan chiqdi | — | hub'da: `sudo -u monitor-hub monitor-hub user passwd LOGIN`, keyin `systemctl restart monitor-hub` |
| Brauzer har safar sertifikat haqida ogohlantiradi | self-signed sertifikat | `/var/lib/monitor-hub/hub.crt` ni admin kompyuterida ishonchli sertifikatlar ro'yxatiga qo'shing |

---

## 9. Muammo bo'lsa: menga yuboriladigan ma'lumotlar

Quyidagi buyruqlar faqat o'qiydi va tizimni o'zgartirmaydi. Hostda (root):

```sh
pveversion; uname -r
dmidecode -s system-product-name
lspci | grep -i -E 'raid|smart array|sas'
ssacli ctrl all show config detail > /tmp/ssacli.txt
smartctl -a -j -d cciss,0 /dev/sda > /tmp/smart0.json
ipmitool -c sdr list > /tmp/ipmi.txt
monitor-agent collect > /tmp/collect.json
journalctl -u monitor-agent -n 200 > /tmp/agent.log
```

Hub'da: `journalctl -u monitor-hub -n 200 > /tmp/hub.log`.

`/tmp/*.txt`, `*.json` va `*.log` fayllarini fleshkaga ko'chirib yuboring. Ularda parol yoki token yo'q.

**RAID alertini simulyatsiya qilish (ixtiyoriy, xavfsiz):** haqiqiy disk chiqarmasdan, alert zanjirini tekshirish uchun ssacli o'rniga vaqtinchalik "degraded" chiqaruvchi skript qo'yish mumkin. Bu usulni men bilan kelishib qilamiz.

---

## 10. O'chirish

```sh
# Host yoki VM/CT da:
systemctl disable --now monitor-agent
rm -rf /etc/systemd/system/monitor-agent.service /usr/local/lib/monitor-agent /usr/local/bin/monitor-agent /var/lib/monitor-agent
systemctl daemon-reload

# Hub'da:
systemctl disable --now monitor-hub
rm -rf /etc/systemd/system/monitor-hub.service /usr/local/bin/monitor-hub /etc/monitor-hub /var/lib/monitor-hub
userdel monitor-hub; systemctl daemon-reload
```

Agentni panelda ham o'chiring: **Sozlamalar → Agentlar → Bekor qilish → O'chirish**.

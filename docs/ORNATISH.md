# O'rnatish (internetsiz)

Paket: `monitor-uz-0.1.0-linux-amd64.tar.gz`. Uni USB orqali hub serveriga ko'chiring va oching:

```sh
tar xzf monitor-uz-0.1.0-linux-amd64.tar.gz
cd monitor-uz-0.1.0-linux-amd64
sha256sum bin/*            # ixtiyoriy tekshiruv
```

Paket ichida:

```
bin/monitor-hub  bin/monitor-agent   statik binary'lar (amd64)
dist/            hub hostlarga tarqatadigan agent fayllari + SHA256SUMS
systemd/         unit fayllar
install-hub.sh   hub o'rnatish skripti
docs/            hujjatlar
```

## 1. Hub

Talab: Debian 12 yoki 13 (VM yoki CT), systemd, hostlar unga 8443-port orqali yeta olishi kerak.

```sh
sudo ./install-hub.sh --url https://10.0.0.5:8443
```

`--url` — agentlar va brauzer hub'ga qaysi manzil orqali murojaat qilishi. Hub IP manzilini yozing. Skript quyidagilarni bajaradi:

- `monitor-hub` tizim foydalanuvchisini yaratadi;
- binary'larni `/usr/local/bin` ga o'rnatadi;
- ma'lumotlar katalogini yaratadi: `/var/lib/monitor-hub` (`hub.db`, `hub.crt`, `hub.key`, `dist/`);
- sozlamalarni `/etc/monitor-hub/hub.env` ga yozadi;
- `monitor-hub` xizmatini yoqadi va ishga tushiradi.

Birinchi ishga tushishda hub o'zining self-signed sertifikatini yaratadi (ECDSA P-256, 10 yil). Sertifikatga hostname, hub'ning barcha IP manzillari va `--url` dagi manzil kiritiladi. Kerak bo'lsa, qo'shimcha nomlarni `--san 10.0.0.5,monitor.lan` orqali bering.

Agentlar sertifikatni CA orqali emas, **pin** (ochiq kalitning SHA-256 xeshi) orqali tekshiradi. Shuning uchun `hub.crt`/`hub.key` ni o'chirsangiz yoki almashtirsangiz, barcha agentlarni qayta o'rnatishga to'g'ri keladi. Ularni zaxira nusxaga qo'shing.

Tekshirish:

```sh
systemctl status monitor-hub
journalctl -u monitor-hub -f
```

Brauzerda `https://10.0.0.5:8443` ni oching. Sertifikat self-signed bo'lgani uchun brauzer bir marta ogohlantiradi. Uni istisno sifatida qabul qiling yoki `hub.crt` ni brauzer/OS ishonchli ro'yxatiga qo'shing.

> **Diqqat (1-bosqich):** panelda hali login yo'q, u 6-bosqichda qo'shiladi. Hozircha hub'ning 8443-portini faqat adminlar tarmog'idan ochiq qoldiring. Agent API (ingest/enroll) token bilan himoyalangan.

## 2. Agentni Proxmox hostga o'rnatish

Hub'da bir martalik token oling (24 soat amal qiladi):

```sh
sudo -u monitor-hub monitor-hub token --note srv-01
```

Buyruqni `root` sifatida emas, `sudo -u monitor-hub` orqali ishga tushiring: aks holda baza fayllari root egaligiga o'tib qolishi mumkin. Dastur buni o'zi tekshiradi va root bo'lsa, ishlashdan bosh tortadi.

Chiqqan buyruqni Proxmox hostda `root` sifatida ishga tushiring:

```sh
curl -fsS --insecure --pinnedpubkey 'sha256//PIN' https://10.0.0.5:8443/install.sh | sh -s -- --token TOKEN
```

- `--insecure` + `--pinnedpubkey`: curl sertifikatni CA bo'yicha tekshirmaydi, lekin uning ochiq kaliti pin bilan mos kelmasa, ulanishni uzadi. Sinovda tekshirildi: noto'g'ri pin bilan xato 90 qaytadi.
- Skript agentni faqat hub'dan yuklaydi va `SHA256SUMS` bo'yicha tekshiradi.
- `monitor-agent` tizim foydalanuvchisini, `/var/lib/monitor-agent/state.json` faylini (doimiy token, 0600) va `monitor-agent.service` ni yaratadi.
- Bir hostga qayta o'rnatish mumkin (yangi token bilan): agent ID va tarix saqlanadi.

Bir necha soniyadan keyin server panelda paydo bo'ladi.

Agentni tekshirish:

```sh
systemctl status monitor-agent
journalctl -u monitor-agent -f
monitor-agent collect      # bitta namunani JSON ko'rinishida chiqaradi (hub'ga yubormaydi)
```

### Agent huquqlari

Agent `monitor-agent` foydalanuvchisi nomidan, root'siz ishlaydi. 1-bosqichda u faqat `/proc` va `/sys` ni o'qiydi. Unitdagi cheklovlar: `NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome=read-only`, `PrivateDevices`, bo'sh `CapabilityBoundingSet`, `MemoryMax=100M`, faqat `AF_INET/AF_INET6/AF_UNIX`. Tinglaydigan port yo'q.

Root talab qiladigan joylar keyingi bosqichlarda paydo bo'ladi (ssacli, smartctl, `/etc/pve` o'qish). Ular alohida hujjatlashtiriladi.

## 3. Boshqarish

```sh
sudo -u monitor-hub monitor-hub agents          # ro'yxat
sudo -u monitor-hub monitor-hub revoke 3        # agent tokenini bekor qilish
monitor-hub pin --data /var/lib/monitor-hub     # pin
```

Hub sozlamalari `/etc/monitor-hub/hub.env` da turadi. O'zgartirgandan keyin `systemctl restart monitor-hub` qiling.

| O'zgaruvchi | Ma'nosi | Standart |
|---|---|---|
| `MONITOR_HUB_URL` | agentlar uchun manzil | — |
| `MONITOR_HUB_LISTEN` | tinglash manzili | `:8443` |
| `MONITOR_HUB_SAN` | sertifikatga qo'shimcha IP/nomlar | — |
| `MONITOR_HUB_HISTORY` | tarix saqlash muddati | `168h` (1 hafta) |
| `MONITOR_HUB_TLS_CERT` / `_KEY` | tashqi sertifikat (ixtiyoriy) | — |

## 4. Yangilash

Yangi paketni ochib, `sudo ./install-hub.sh` ni qayta ishga tushiring. Ma'lumotlar va `hub.env` saqlanadi. Hostlardagi agentni yangilash uchun hostda yangi token bilan o'rnatish buyrug'ini qayta bajaring. Paneldan boshqariladigan yangilash 6-bosqichda qilinadi.

## 5. O'chirish

```sh
# host:
systemctl disable --now monitor-agent && rm -rf /etc/systemd/system/monitor-agent.service /usr/local/bin/monitor-agent /var/lib/monitor-agent && userdel monitor-agent
# hub:
systemctl disable --now monitor-hub && rm -rf /etc/systemd/system/monitor-hub.service /usr/local/bin/monitor-hub /etc/monitor-hub /var/lib/monitor-hub && userdel monitor-hub
```

## Ma'lumot saqlash

- `raw` jadvali: har bir qiymat asl chastotada (1 s). 1 soatdan ko'proq (75 daqiqa) saqlanadi, shunda kechikib kelgan buferlangan ma'lumot ham 10 s o'rtachaga qo'shiladi.
- `rollup` jadvali: 10 s o'rtachalar, 1 hafta saqlanadi. Har yadro bo'yicha CPU faqat xom holatda (1 soat) saqlanadi, bu bazani ixcham tutadi.
- Panel 1 soatgacha bo'lgan oraliqlarni xom ma'lumotdan, uzunroqlarini 10 s o'rtachalardan chizadi.

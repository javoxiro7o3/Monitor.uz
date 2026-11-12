# O'rnatish (internetsiz)

Paket: `monitor-uz-0.9.0-linux-amd64.tar.gz`. Uni USB orqali hub serveriga ko'chiring va oching:

```sh
tar xzf monitor-uz-0.9.0-linux-amd64.tar.gz && cd monitor-uz-0.9.0-linux-amd64
```

Paket ichida:

- `bin/` — `monitor-hub` va `monitor-agent` (statik, amd64);
- `dist/` — hub hostlarga tarqatadigan agent fayli, `SHA256SUMS`, `VERSION`;
- `systemd/` — unit fayllar;
- `install-hub.sh` — hub o'rnatish skripti;
- `docs/` — hujjatlar.

## 1. Hub

Talab: Debian 12 yoki 13 (VM yoki CT), systemd. Hostlar va VM lar hub'ga 8443-port orqali yeta olishi kerak.

```sh
sudo ./install-hub.sh --url https://10.0.0.5:8443
```

Skript quyidagilarni bajaradi:

- `monitor-hub` tizim foydalanuvchisini yaratadi;
- binary'ni o'rnatadi;
- `/var/lib/monitor-hub` (baza, sertifikat, shifrlash kaliti, agent fayllari) va `/etc/monitor-hub/hub.env` ni yaratadi;
- xizmatni ishga tushiradi.

Oxirida **o'rnatish kodi** chiqadi: brauzerda `https://10.0.0.5:8443` ni oching va shu kod bilan birinchi adminni yarating. Kodni keyin ham ko'rish mumkin: `sudo cat /var/lib/monitor-hub/setup-code`. Admin yaratilgach, kod o'chiriladi.

Hub o'zining self-signed sertifikatini yaratadi. Brauzer bir marta ogohlantiradi: istisno qilib qo'shing yoki `/var/lib/monitor-hub/hub.crt` ni ishonchli ro'yxatga kiriting. Agentlar sertifikatni CA orqali emas, ochiq kalit xeshi (**pin**) bo'yicha tekshiradi.

**Zaxira nusxaga albatta oling:** `hub.crt`, `hub.key`, `secret.key`, `hub.db`.

- `hub.key` yo'qolsa, barcha agentlarni qayta o'rnatish kerak bo'ladi.
- `secret.key` yo'qolsa, webhook manzili va 2FA kalitlari o'qilmaydi.

## 2. Agent: Proxmox host

Panel → **Sozlamalar → Agentlar** → rejim "Host" → **Token yaratish**. Chiqqan buyruqni hostda `root` sifatida ishga tushiring:

```sh
curl -fsS --insecure --pinnedpubkey 'sha256//PIN' https://10.0.0.5:8443/install.sh | sh -s -- --token TOKEN
```

- `--insecure` + `--pinnedpubkey`: curl sertifikatni CA orqali tekshirmaydi, lekin ochiq kalit pin'ga mos kelmasa, ulanishni uzadi.
- Agent `/usr/local/lib/monitor-agent/` ga o'rnatiladi. `/usr/local/bin/monitor-agent` unga ishora qiladi.
- Ma'lumotlar `/var/lib/monitor-agent/` da saqlanadi: `state.json` (doimiy token, 0600) va bufer.

**Huquqlar.** Host agent `root` sifatida ishlaydi, lekin cheklangan capability to'plami bilan: `CAP_SYS_RAWIO`, `CAP_SYS_ADMIN`, `CAP_DAC_READ_SEARCH`, `CAP_DAC_OVERRIDE`. Sababi:

- `ssacli` va `smartctl` disk kontrolleriga, `ipmitool` esa `/dev/ipmi0` ga murojaat qiladi;
- `/etc/pve` faqat root/www-data uchun o'qiladi.

Unitda qo'shimcha cheklovlar ham bor: `NoNewPrivileges`, `ProtectSystem=full`, `ProtectKernel*`, `MemoryMax=150M`. Agent tizimni o'zgartirmaydi: `qm` va `pct` buyruqlarini ham chaqirmaydi, VM/CT ma'lumotini konfig fayllar va cgroup'dan o'qiydi. Yagona yozadigan joyi — o'z katalogi (yangilanish uchun).

**Ixtiyoriy vositalar** (bo'lmasa, tegishli bo'lim "ma'lumot yo'q" deb ko'rsatiladi):

- **ssacli** — HPE saytidan boshqa kompyuterda yuklab, `.deb` ni hostga ko'chiring: `dpkg -i ssacli-*.deb`. Versiyasini yozib qo'ying.
- **smartctl** — `smartmontools` paketi. Proxmox'da odatda o'rnatilgan.
- **ipmitool** — `apt install ipmitool` (Proxmox ichki omboridan). Hostda `/dev/ipmi0` paydo bo'lishi uchun kernel modullari kerak: `modprobe ipmi_si ipmi_devintf`.

## 3. Agent: Docker bor VM/CT (guest)

Token yaratishda rejimni **"Guest"** qilib tanlang va buyruqni VM/CT ichida `root` sifatida bajaring (buyruq oxirida `--mode guest` bo'ladi).

- Guest agent root'siz, `monitor-agent` foydalanuvchisi nomidan ishlaydi. Docker socket'ga `docker` guruhi orqali kiradi va faqat GET so'rovlari yuboradi.
- Guest agent Proxmox'dagi VM/CT ga **MAC manzil** orqali avtomatik bog'lanadi.
- Talab: `curl` va systemd. Minimal Debian CT da curl bo'lmasa: `apt install curl` (ichki ombordan).

## 4. Alertlar va Mattermost

1. Mattermost'da: **Integrations → Incoming Webhooks → Add** va manzilni nusxalang.
2. Panelda: **Sozlamalar → Bildirishnomalar** → manzilni kiriting → **Sinov xabari** → **Saqlash**.

Manzil bazada shifrlangan holda saqlanadi, loglarga yozilmaydi. Mattermost hali yo'q bo'lsa, sinov uchun istalgan HTTP qabul qiluvchi (masalan, `nc -l 9000`) yetadi.

Qoidalar va chegaralar: **Alertlar → Qoidalar**. "VM/CT to'xtadi" faqat kuzatiladigan VM/CT lar uchun ishlaydi. Standart holatda avtomatik ishga tushadigan (`onboot: 1`) VM/CT lar kuzatiladi; buni VM ro'yxatidagi "Kuzatiladi" tugmasi bilan o'zgartirish mumkin.

## 5. Agentni yangilash va qaytarish

1. Yangi `monitor-agent` faylini **Sozlamalar → Agentlar → Agent versiyalari** orqali yuklang.
2. Avval bitta agentni **Yangilash…** ro'yxatidan yangi versiyaga o'tkazing.
3. Agent keyingi ulanishida faylni oladi, SHA-256 va versiyasini tekshiradi, o'zini almashtiradi va qayta ishga tushadi. Eski fayl `monitor-agent.prev` sifatida saqlanadi.
4. Muammo bo'lsa, eski versiyani xuddi shu yo'l bilan tanlang.

**O'rnatish uchun tanlash** tugmasi yangi o'rnatishlarda (`install.sh`) qaysi versiya ishlatilishini belgilaydi.

Hub'ni yangilash: yangi paketda `sudo ./install-hub.sh` ni qayta ishga tushiring. Ma'lumotlar saqlanadi.

## 6. Admin buyruqlari (hub serverida)

```sh
sudo -u monitor-hub monitor-hub user list
sudo -u monitor-hub monitor-hub user add ali           # parolni stdin'dan so'raydi
sudo -u monitor-hub monitor-hub user passwd ali
sudo -u monitor-hub monitor-hub user reset-2fa ali     # telefon yo'qolsa
sudo -u monitor-hub monitor-hub agents
sudo -u monitor-hub monitor-hub token --note srv-01    # paneldan tashqari token
monitor-hub pin --data /var/lib/monitor-hub
```

`sudo -u monitor-hub` bilan ishga tushiring, root bilan emas: aks holda baza fayllari root egaligiga o'tib qolishi mumkin. Dastur buni o'zi tekshiradi.

`/etc/monitor-hub/hub.env` sozlamalari:

- `MONITOR_HUB_URL` — agentlar ulanadigan manzil;
- `MONITOR_HUB_LISTEN` — tinglash manzili (standart `:8443`);
- `MONITOR_HUB_SAN` — sertifikatga qo'shimcha nomlar;
- `MONITOR_HUB_HISTORY` — tarix saqlash muddati (standart `168h`);
- `MONITOR_HUB_TLS_CERT`, `MONITOR_HUB_TLS_KEY` — ichki CA sertifikatidan foydalanish uchun.

O'zgartirgandan keyin: `systemctl restart monitor-hub`.

## 7. Xavfsizlik qisqacha

- **Agent → hub:** HTTPS, pin bo'yicha tekshiruv, har agentga alohida token (bazada xesh holida). Bekor qilingan token bilan ma'lumot qabul qilinmaydi.
- **Panelga kirish:**
  - Argon2id bilan xeshlangan parollar, kamida 10 belgi;
  - ixtiyoriy TOTP 2FA;
  - seans 12 soat amal qiladi, 2 soat harakatsizlikdan keyin yopiladi;
  - 5 ta noto'g'ri urinishdan keyin hisob 15 daqiqaga bloklanadi, bir IP uchun chegara 20 ta;
  - cookie `HttpOnly`, `Secure`, `SameSite=Strict`, CSRF uchun alohida sarlavha.
- **Panel tashqi manzilga murojaat qilmaydi:** CSP `default-src 'self'`.
- **Panel orqali boshqaruv yo'q:** start/stop ham, terminal ham yo'q.

## 8. O'chirish

```sh
# host yoki VM/CT:
systemctl disable --now monitor-agent; rm -rf /etc/systemd/system/monitor-agent.service /usr/local/lib/monitor-agent /usr/local/bin/monitor-agent /var/lib/monitor-agent
# hub:
systemctl disable --now monitor-hub; rm -rf /etc/systemd/system/monitor-hub.service /usr/local/bin/monitor-hub /etc/monitor-hub /var/lib/monitor-hub; userdel monitor-hub
```

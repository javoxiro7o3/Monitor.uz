# Hisobot: Monitor.uz 0.9.0

Sana: 2026-11-12. TZ dagi 6 bosqichning hammasi bajarildi. Kod simulyatsiyada sinovdan o'tdi, lekin **real HPE/Proxmox serverda hali tekshirilmadi**. Real serverda nimani tekshirish kerakligi pastda yozilgan.

## Bosqichlar bo'yicha

| # | Bosqich | Holat |
|---|---|---|
| 1 | Poydevor: host agent, hub, SQLite, jonli grafiklar | tayyor, ishlab chiqish mashinasidagi real `/proc` da sinalgan |
| 2 | Proxmox qatlami: VM/CT, tarmoq tuzilishi, tarmoq chizmasi, ko'p serverli ko'rinish | tayyor, soxta `/etc/pve` va cgroup bilan sinalgan |
| 3 | Disklar va harorat: ssacli, smartctl (+ mdstat, ZFS), ipmitool / hwmon | tayyor, hujjatlardagi chiqish formatlari asosida yozilgan |
| 4 | Guest agent va Docker | tayyor, soxta Docker API bilan sinalgan |
| 5 | Alertlar: qoidalar, guruhlash, eslatish, "tiklandi", webhook, sukut | tayyor, webhook qabul qiluvchida sinalgan |
| 6 | Login, 2FA, foydalanuvchilar, agent yangilash/qaytarish, o'zbekcha matnlar, offline paket, hujjatlar | tayyor |

## Simulyatsiyada nima tekshirildi

Sinov ishlab chiqish mashinasida o'tkazildi (Linux 6.18, 4 vCPU). Unda Linux namespace'lar yordamida 3 ta "Proxmox host" va 1 ta guest yaratildi. Har hostda alohida `/etc/pve`, cgroup, `/sys/class/net` va soxta `ssacli`/`smartctl`/`ipmitool` skriptlari bor edi; Docker API ham soxta socket orqali berildi.

| Tekshiruv | Natija |
|---|---|
| Unit testlar | parserlar: `/proc`, ssacli, smartctl JSON, ipmitool CSV, mdstat, zpool, Proxmox konfig, cgroup, Docker; delta/wrap, saqlash/rollup, alert dvigateli; Argon2 va TOTP (RFC 6238 vektorlari); auth oqimi (setup kodi, CSRF, 2FA, bloklash) |
| VM CPU / I/O | cgroup hisoblagichlaridan berilgan yuklamaga mos qiymat chiqdi (masalan, 88% berilganda ~72–90%) |
| RAID | srv-03 da RAID 5 "degraded" va bay 5 "nosoz" aniqlandi; RAID a'zolari va zaxira disk to'g'ri; smartctl'dan ishlagan soatlar seriya raqami bo'yicha qo'shildi |
| Guest → VM | guest agent MAC manzil bo'yicha VM 200 ga avtomatik bog'landi |
| Docker | 6 ta konteyner, CPU/RAM cgroup'dan, restartlar soni, to'xtagani |
| Alertlar → webhook | 7 ta alert Slack formatidagi JSON bilan qabul qiluvchiga yetdi; URL siri loglarda ko'rinmaydi |
| Agent o'chirildi | "agent javob bermayapti" xabari **30 s** da keldi; agent qaytgach "TIKLANDI" xabari keldi |
| Agentni yangilash | 0.9.1 panel orqali yuklandi va bitta agentga tayinlandi. Agent faylni yuklab, SHA-256 ni tekshirdi, o'zini almashtirdi (`.prev` saqlandi) va 0.9.1 bo'lib qayta ishga tushdi |
| Hub/agent o'chib-yonishi | bufer diskka saqlanadi va tiklanadi, grafikda bo'shliq qolmaydi (1-bosqichda sinalgan) |
| **Agent resursi** | host agent (hamma kollektorlar yoqiq): CPU o'rtacha **0.24–0.27%**, RSS **~13 MB**; guest agent (Docker bilan): **0.11%**, **~13 MB**. Hub: ~28 MB |
| Panel | barcha 8 sahifa, login, qorong'u/yorug' mavzu, telefon ko'rinishi; tashqi so'rov 0 ta, JS xatolari yo'q |

## Ertaga real serverda tekshiring

Pastdagi buyruqlar faqat o'qiydi. Natijalarni menga yuborsangiz, parserlarni haqiqiy chiqishga moslayman.

### 1. Disklar va RAID (eng muhimi)

Parser HPE hujjatlaridagi namunalar asosida yozilgan, real chiqishga qarshi sinalmagan.

```sh
dmidecode -s system-product-name
lspci | grep -i -E 'raid|smart array|sas'
ssacli ctrl all show config detail > /tmp/ssacli.txt      # shu faylni yuboring
smartctl -a -j -d cciss,0 /dev/sda > /tmp/smart0.json      # Smart Array ortidagi 1-disk
smartctl --scan
```

Panelda **Disklar** sahifasini oching va quyidagilarni haqiqiy holat bilan solishtiring:

- disklar soni va bay raqamlari;
- RAID turi va a'zolari;
- harorat va ishlagan soat.

### 2. Harorat, fan va quvvat bloki

```sh
ls -l /dev/ipmi* ; lsmod | grep -E 'ipmi|hpilo'
ipmitool -c sdr list > /tmp/ipmi.txt                       # shu faylni yuboring
```

`/dev/ipmi0` bo'lmasa, panel hwmon (CPU harorati) bilan cheklanadi, bu kutilgan holat.

### 3. Proxmox qatlami

- VM/CT ro'yxati, holati va CPU/RAM qiymatlarini Proxmox web UI dagi qiymatlar bilan solishtiring. RAM'da farq bo'lishi mumkin: agent QEMU jarayonining butun xotirasini o'lchaydi.
- Tarmoq chizmasida bridge, VLAN va VM bog'lanishlari to'g'ri ekanini tekshiring.
- Muammo bo'lsa, `monitor-agent collect > /tmp/collect.json` natijasini yuboring.

### 4. Guest agent

- Docker bor VM'ga o'rnating.
- Panelda VM sahifasida "Ichki holat" va konteynerlar paydo bo'lishini tekshiring.
- VM ga bog'lanmasa (MAC mos kelmasa), **VM va CT** sahifasining pastida "bog'lanmagan" bo'limida ko'rinadi.

### 5. Alert sinovi

- Webhook'ka sinov xabarini yuboring.
- Bitta hostda `systemctl stop monitor-agent` qiling → 30–35 s ichida xabar kelishi, `start` dan keyin "TIKLANDI" kelishi kerak.

### 6. Resurs o'lchovi

Real serverda 30 daqiqa ishlagandan keyin:

```sh
systemctl status monitor-agent | grep -E 'Memory|CPU'
```

## Ma'lum cheklovlar va ochiq masalalar

- **Tekshirilmagan:**
  - ssacli matn formati (firmware/versiyaga qarab farq qilishi mumkin);
  - `ipmitool -c` formati HPE iLO 5 da;
  - Debian 12/13 dagi systemd unitlari (sinov muhitida systemd yo'q edi);
  - `install.sh` / `install-hub.sh` real tizimda.
- **Host agent root bilan ishlaydi.** U capability'lar bilan cheklangan, lekin baribir root. ssacli/smartctl/ipmitool shuni talab qiladi.
- **VM tarmog'i** host tomondagi `tap<ID>i*` / `veth<ID>i*` hisoblagichlaridan olinadi; bu Proxmox nomlash qoidasiga asoslangan.
- **cgroup v1 dagi eski Linux VM lar:** Docker konteynerlarining CPU/RAM qiymati Docker stats API'dan olinadi, tarmoq va I/O tezligi ko'rsatilmaydi.
- **Hub o'chiq paytda** VM/Docker hisobotlari buferlanmaydi, faqat host metrikalari (1 s namunalar) buferlanadi. VM tarixida shu oraliq bo'sh qoladi.
- **2FA kalitini qo'shish** qo'lda bo'ladi: kalitni authenticator ilovaga yozasiz. QR kod yo'q, chunki QR generatori uchun kutubxona qo'shmadim.
- **Alert matnlari** (webhook) Go kodida o'zbekcha yozilgan. Panel matnlari esa `web/i18n/uz.json` da.
- **Fayl tanlash maydoni** (`Choose File`) brauzerning o'z yozuvi, uni o'zbekchaga o'girib bo'lmaydi.
- **TZ va promptdagi farq:** TZ dagi "SMTP" Sozlamalarga qo'shilmadi, promptdagi qarorga ko'ra o'rniga webhook qilindi.
- **Hub yagona nuqta.** Bu ongli qabul qilingan xavf. `hub.db`, `hub.key` va `secret.key` uchun zaxira nusxa rejasini tuzing.

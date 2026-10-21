# DEPLOY — Menjalankan `tafw` di server UAT (tanpa sudo)

Panduan ini **sesuai access dan environment** saat ini:

- Server: **Ubuntu 24.04**, user **`svr-adins`**, **tanpa akses sudo**.
- Shell login: **`rbash`** (terbatas).
- Home & folder kerja: **`/home/svr-adins`**.
- Egress ke S3 (`s3.ap-southeast-3.amazonaws.com:443`) OK; internet umum diblokir.
- Binary **di-build di Linux lain** (lihat `README.md`), lalu di-`scp` ke server.
- Dijalankan sebagai **`systemd --user`** (karena `Linger=no`, service hidup selama sesi login; cukup untuk dev/UAT).

Untuk build binary, lihat **`BUILD.md`**.

---

## Ringkasan alur

```
INCOMING/<file>.txt|.TAFCM
   │  (tafw upload: rename ke .tafcm huruf kecil)
   ▼
s3://taf-batch/SANDBOX/<file>.tafcm       <-- PREFIX HURUF BESAR
   │  (Provenir proses)
   ▼
s3://taf-batch/SANDBOX/TAFCM_<datetime>/output/success=true/part-*.txt
   │  (tafw download: gabung; header part pertama DIPERTAHANKAN)
   ▼
/home/svr-adins/OUTCOMING/TAFCM_<datetime>.tafcm

   (bila Provenir hanya menghasilkan output/success=false/part-*)
   ▼
/home/svr-adins/ERROR/TAFCM_<datetime>.tafcm
```

> ⚠️ **Prefix wajib `SANDBOX/` (huruf besar).**
> Salah case (`sandbox/`) membuat file masuk ke folder yang tidak dipantau Provenir.

---

## 0. Struktur folder di server

```
/home/svr-adins/
├── INCOMING/      # taruh file *.txt / *.TAFCM di sini
├── INCOMING_SENT/ # file input yang sudah sukses upload (DIPINDAH, bukan dihapus)
├── OUTCOMING/     # hasil gabungan *.tafcm
├── ERROR/         # file gagal + alasan (*.reason) dan hasil success=false *.tafcm
├── bin/tafw       # binary hasil build (BUILD.md)
├── state/         # state idempotency + state.json
├── logs/          # tafw-YYYYMMDD.log (rolling harian, bila dipakai)
├── config.yaml
└── credentials    # chmod 600
```

Buat folder yang belum ada (semua argumen boleh mengandung `/` walau rbash):

```bash
mkdir -p /home/svr-adins/bin /home/svr-adins/state /home/svr-adins/logs
ls -ld /home/svr-adins/INCOMING /home/svr-adins/INCOMING_SENT /home/svr-adins/OUTCOMING /home/svr-adins/ERROR
```

---

## 1. Kirim binary ke server

Dari mesin build (Linux):

```bash
scp tafw svr-adins@uat1-ubuntu-staging:/home/svr-adins/bin/tafw
```

Di server:

```bash
ls -l /home/svr-adins/bin/tafw
```

---

## 2. Config (`/home/svr-adins/config.yaml`)

> Kredensial AWS **TIDAK** di sini. Isi di file `credentials` (langkah 3).

Edit dengan `nano` (rbash memblokir `>` dan heredoc):

```bash
nano /home/svr-adins/config.yaml
```

Isi:

```yaml
s3:
  bucket: taf-batch
  region: ap-southeast-3
  endpoint: ""
  input_prefix: "SANDBOX/"
  output_prefix: "SANDBOX/"
  sse: "AES256"

credentials:
  mode: file
  file: /home/svr-adins/credentials

folders:
  incoming: /home/svr-adins/INCOMING
  incoming_sent: /home/svr-adins/INCOMING_SENT
  outcoming: /home/svr-adins/OUTCOMING
  error: /home/svr-adins/ERROR
  state: /home/svr-adins/state
  logs: /home/svr-adins/logs

watcher:
  settle_seconds: 10
  poll_seconds: 30
  completion_method: status_json
  file_patterns:
    - "*.txt"
    - "*.TAFCM"
  success_folder: "success=true"
  failed_folder: "success=false"
  output_dir_name: "output"
  output_folder_prefix: "TAFCM_"
  stability_windows: 2
  output_suffix: ".tafcm"
  rename_input_lower: true
  verify_counts: false

logging:
  level: info
  file: ""
  daily_roll: false
```

---

## 3. Kredensial (`/home/svr-adins/credentials`)

Isi:

```ini
AWS_ACCESS_KEY_ID=AKIAxxxxxxxxxxxxxxxx
AWS_SECRET_ACCESS_KEY=xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
# AWS_SESSION_TOKEN=...   # opsional
```

Bila **tidak bisa scp**, buat dengan `printf | dd` (pipe diizinkan rbash):

```bash
printf 'AWS_ACCESS_KEY_ID=AKIAxxxxxxxxxxxxxxxx\nAWS_SECRET_ACCESS_KEY=xxxxxxxx\n' \
  | dd of=/home/svr-adins/credentials
chmod 600 /home/svr-adins/credentials
ls -l /home/svr-adins/credentials
```

> Jangan menaruh kredensial di `config.yaml`, source, atau repo Git.

---

## 4. Unit `systemd --user`

Buat file service (pakai `nano`, atau `scp`):

```bash
mkdir -p /home/svr-adins/.config/systemd/user
nano /home/svr-adins/.config/systemd/user/tafwatcher-upload.service
```

`tafwatcher-upload.service`:

```ini
[Unit]
Description=tafw uploader (INCOMING -> S3)
After=default.target

[Service]
Type=simple
WorkingDirectory=/home/svr-adins
ExecStart=/home/svr-adins/bin/tafw upload --config /home/svr-adins/config.yaml
Environment=TAFW_LOG_FILE=/home/svr-adins/logs
Environment=TAFW_LOG_DAILY_ROLL=true
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
```

`tafwatcher-download.service`:

```ini
[Unit]
Description=tafw downloader (S3 output -> OUTCOMING)
After=default.target

[Service]
Type=simple
WorkingDirectory=/home/svr-adins
ExecStart=/home/svr-adins/bin/tafw download --config /home/svr-adins/config.yaml
Environment=TAFW_LOG_FILE=/home/svr-adins/logs
Environment=TAFW_LOG_DAILY_ROLL=true
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
```

Aktifkan:

```bash
systemctl --user daemon-reload
systemctl --user enable --now tafwatcher-upload tafwatcher-download
systemctl --user status tafwatcher-upload tafwatcher-download --no-pager
```

Ekspektasi: kedua service `active (running)`.

> Karena `Linger=no`, service berhenti saat logout. Setiap login lagi, jalankan:
> `systemctl --user start tafwatcher-upload tafwatcher-download`

Lihat log:

```bash
journalctl --user -u tafwatcher-upload -f
journalctl --user -u tafwatcher-download -f
# atau:
tail -f /home/svr-adins/logs/tafw-$(date +%Y%m%d).log
```

Bila muncul `Failed to connect to bus`, prefix env-nya:

```bash
XDG_RUNTIME_DIR=/run/user/1005 systemctl --user status tafwatcher-upload --no-pager
```

---

## 5. Verifikasi end-to-end (UAT)

Jangan logout sampai selesai.

```bash
# 5a. Upload — taruh file TANPA spasi/tanda kurung
cp /path/ke/input_file_test.txt /home/svr-adins/INCOMING/input_file_test.txt
journalctl --user -u tafwatcher-upload -f
```

Ekspektasi:
- log `"upload sukses"` dengan `"key":"SANDBOX/input_file_test.tafcm"`;
- file input **DIPINDAH** dari `INCOMING` ke `INCOMING_SENT` (rename ke `.tafcm`).

```bash
ls -l /home/svr-adins/INCOMING/          # harus kosong
ls -l /home/svr-adins/INCOMING_SENT/     # berisi input_file_test.tafcm
```

```bash
# 5b. Download — tunggu Provenir menghasilkan folder TAFCM_<datetime>/
journalctl --user -u tafwatcher-download -f
ls -l /home/svr-adins/OUTCOMING/
head -n 2 /home/svr-adins/OUTCOMING/TAFCM_*.tafcm
```

Ekspektasi: satu file `TAFCM_<datetime>.tafcm`, delimiter koma, satu baris header
di paling atas.

---

## 6. Jalur gagal (ERROR)

File yang gagal upload dipindah ke `ERROR/` + `<nama>.reason`:

```bash
ls -l /home/svr-adins/ERROR/
cat /home/svr-adins/ERROR/*.reason
```

> `ERROR/` juga dapat berisi hasil gabungan `TAFCM_<datetime>.tafcm` bila Provenir
> hanya menghasilkan `output/success=false/part-*`.

Cek state idempotency:

```bash
cat /home/svr-adins/state/state.json
```

---

## 7. Update binary

```bash
systemctl --user stop tafwatcher-upload tafwatcher-download
# scp binary baru dari mesin build ke /home/svr-adins/bin/tafw
systemctl --user start tafwatcher-upload tafwatcher-download
```

---

## 8. Fallback: cron + flock (bila tidak mau pakai systemd --user)

```bash
systemctl --user disable --now tafwatcher-upload tafwatcher-download

printf '%s\n' \
'SHELL=/bin/sh' \
'PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin' \
'* * * * * flock -n /home/svr-adins/state/upload.lock /home/svr-adins/bin/tafw upload --config /home/svr-adins/config.yaml --once' \
'* * * * * flock -n /home/svr-adins/state/download.lock /home/svr-adins/bin/tafw download --config /home/svr-adins/config.yaml --once' \
| crontab -
crontab -l
```

> cron memakai `/bin/sh` (bukan rbash), jadi path absolut aman.

---

## 9. Catatan rbash

- **Diblokir**: `>` `>>`, heredoc, dan perintah yang **namanya** mengandung `/`
  (mis. `./tafw`), serta `cd`.
- **Diizinkan**: pipe `|`, argumen berisi `/`, `scp`/`sftp`, `chmod`, `mkdir`,
  `systemctl --user`, `crontab`, `nano`.
- Membuat file: pakai **`nano`**, **`scp`**, atau **`printf | dd`**.
- Menjalankan binary: lewat `systemctl --user` + `journalctl --user`, atau
  `env PATH=/home/svr-adins/bin:/usr/bin:/bin tafw version`.

---

## 10. Troubleshooting

| Gejala | Cek |
|---|---|
| Service `failed` saat start | `journalctl --user -u <svc> -n 100 --no-pager`; cek config/folder |
| `permission denied` folder | owner folder = `svr-adins`; lihat langkah 0 |
| `AccessDenied`/`InvalidAccessKeyId` | isi & `chmod 600 credentials` (langkah 3) |
| `lookup ... on 127.0.0.53: i/o timeout` | binary di-build **tanpa cgo**; rebuild di Linux (`BUILD.md`) |
| `dial tcp <ip>:443: i/o timeout` | IP S3 tertentu diblok firewall (intermiten); minta tim jaringan allowlist range S3 `ap-southeast-3`, sementara ulangi upload |
| File input masuk `ERROR/` | buka file `*.reason` untuk alasan |
| Output tak masuk `OUTCOMING` | pastikan `status.json` = `COMPLETED`; jika Provenir balas `success=false`, hasil gabungan masuk ke `ERROR/`; cek log downloader |
| Service mati setelah logout | `Linger=no`; pakai cron (langkah 8) atau minta `loginctl enable-linger` |

---


## 11. Uninstall

```bash
systemctl --user disable --now tafwatcher-upload tafwatcher-download
rm -f /home/svr-adins/.config/systemd/user/tafwatcher-{upload,download}.service
systemctl --user daemon-reload
# Opsional hapus data (hati-hati):
# rm -rf /home/svr-adins/{bin,state,logs} /home/svr-adins/config.yaml /home/svr-adins/credentials
```

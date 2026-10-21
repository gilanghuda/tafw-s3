# tafw - Watcher TAFCM <-> AWS S3

Aplikasi kecil untuk menjembatani server Linux (DMZ) dengan bucket S3 milik
Provenir. Menggantikan pola Windows Service pada stack SPV2; di sini dijalankan
sebagai **service Linux** (systemd `--user` atau cron).

Dua tugas:

1. **Upload** - setiap file `*.txt` atau `*.TAFCM` yang ditaruh di folder
   `INCOMING` otomatis diupload ke S3 (`taf-batch/SANDBOX/`), dengan
   nama di-rename menjadi `.tafcm` huruf kecil (isi tidak diubah).
2. **Download** - setelah Provenir selesai memproses, output di S3
   (`TAFCM_<datetime>/output/success=true/...`) diunduh, digabung menjadi **satu
   file** `.tafcm`, lalu ditaruh di folder `OUTCOMING` untuk dikonsumsi Confins.
   Bila Provenir menghasilkan `success=false`, hasilnya digabung ke folder
   `ERROR`.

---

## Dokumentasi

| Topik | File |
|---|---|
| Install & build binary (Linux, cgo) | [`BUILD.md`](BUILD.md) |
| Deploy di server tanpa sudo (svr-adins) | [`DEPLOY.md`](DEPLOY.md) |
| Runbook user-space (systemd --user / cron) | [`deploy/USERSPACE-DEPLOY.md`](deploy/USERSPACE-DEPLOY.md) |
| Runbook Ubuntu/Debian (tanpa sudo) | [`deploy/UBUNTU-DEPLOY.md`](deploy/UBUNTU-DEPLOY.md) |
| Contoh config | `deploy/config.yaml.example`, `deploy/config.userspace.yaml.example` |
| Template kredensial | `deploy/credentials.example` |
| Contoh IAM policy | `deploy/iam-policy.json` |

> **Build wajib di Linux dengan cgo aktif** (bukan cross-compile dari Windows),
> supaya resolver DNS memakai glibc. Detail: [`BUILD.md`](BUILD.md).

---

## Setup folder (WAJIB untuk UAT & Deploy)

Semua folder berada di bawah home user (tanpa sudo), yaitu `/home/svr-adins`:

```
/home/svr-adins/
├── INCOMING/       # taruh file *.txt / *.TAFCM di sini untuk diupload
├── INCOMING_SENT/  # file input yang sudah terkirim ke S3 (arsip)
├── OUTCOMING/      # hasil gabungan *.tafcm (success=true) untuk Confins
├── ERROR/          # hasil gabungan *.tafcm (success=false) + alasan (*.reason)
├── state/          # catatan idempotency (state.json)
├── logs/           # tafw-YYYYMMDD.log (rolling harian)
├── bin/tafw        # binary hasil build
├── config.yaml
└── credentials    # chmod 600
```

Pembuatan folder yang belum ada:

```bash
mkdir -p /home/svr-adins/bin /home/svr-adins/state /home/svr-adins/logs
ls -ld /home/svr-adins/INCOMING /home/svr-adins/OUTCOMING /home/svr-adins/ERROR /home/svr-adins/INCOMING_SENT
```

Langkah lengkap (kredensial, config, systemd --user, cron) ada di
[`DEPLOY.md`](DEPLOY.md).

---

## Alur data

```
INCOMING/<file>.txt  atau  INCOMING/<file>.TAFCM
   |  (tafw upload: rename jadi <file>.tafcm huruf kecil)
   v
s3://taf-batch/SANDBOX/<file>.tafcm
   |  (Provenir proses)
   v
s3://taf-batch/SANDBOX/TAFCM_<Y>_<M>_<D>_<H>h_<m>m_<s>s/
   |- status.json                       {"status":"COMPLETED", ...}
   |- raw_<file>.tafcm                  copy file input
   |- intermediate-status/partition-*.json
   |- overall-status/part-*.json + _SUCCESS
   |- output/
   |    |- success=true/part-00000-<uuid>.c000.txt   <- digabung ke OUTCOMING
   |    |- success=false/part-00000-<uuid>.c000.txt  <- digabung ke ERROR
   |  (tafw download: unduh part, concat, pertahankan header part pertama)
   v
OUTCOMING/TAFCM_<datetime>.tafcm   (bila ada success=true)
ERROR/TAFCM_<datetime>.tafcm       (bila hanya ada success=false)
```

Catatan format output:

- Part file: `part-00000-<uuid>.c000.txt`, urut **leksikografis** = urutan partisi.
- Delimiter **koma**, akhir baris **LF**, tanpa BOM.
- Header: **hanya header part pertama dipertahankan**, header part berikutnya
  dibuang (opsi A) - header muncul sekali di atas file gabungan.
- Folder dengan part `success=true` digabung ke `OUTCOMING/`; folder yang hanya
  punya `success=false` digabung ke `ERROR/`.
- Prefix S3 input & output **`SANDBOX/`** (huruf besar - S3 case-sensitive).

---

## Konfigurasi

| File | Isi | Catatan |
|---|---|---|
| `config.yaml` | bucket, region, prefix, folder, watcher | **tanpa** kredensial |
| `credentials` | `AWS_ACCESS_KEY_ID` + `AWS_SECRET_ACCESS_KEY` | template di `deploy/credentials.example` |

> **Access key & secret key ditulis di file `credentials`, BUKAN di `config.yaml`.**
> `config.yaml` hanya menunjuk lewat `credentials.file`.

Config dapat ditimpa environment variable, mis. `TAFW_S3_BUCKET`,
`TAFW_INCOMING`, `TAFW_S3_SSE`, `TAFW_LOG_LEVEL`. Mode dry-run memakai flag
`--dry-run`.

---

## Menjalankan (manual, untuk uji)

```bash
# Uji config + format kredensial + akses folder (TANPA memanggil API S3)
tafw upload   --config /home/svr-adins/config.yaml --dry-run --once
tafw download --config /home/svr-adins/config.yaml --dry-run --once

# Jalankan loop penuh (upload & download)
tafw upload   --config /home/svr-adins/config.yaml
tafw download --config /home/svr-adins/config.yaml
```

> `--dry-run` hanya memvalidasi config, format kredensial, dan folder; **bukan**
> uji konektivitas/izin S3. Konektivitas S3 baru teruji saat upload nyata.

---

## Keamanan

- **Kredensial**: file `/home/svr-adins/credentials`, permission `600`, milik
  user. **JANGAN** menaruh kredensial di `config.yaml`, source code, atau repo.
- **Least privilege**: IAM policy hanya memberi `s3:PutObject`/`s3:GetObject`/
  `s3:ListBucket` pada bucket `taf-batch`, prefix `SANDBOX/*`.
- **TLS verify ON** (default SDK); SSE `AES256` (default) atau `aws:kms`.
- **Egress** hanya ke `s3.ap-southeast-3.amazonaws.com:443` (firewall tim jaringan).
- **Rotasi kredensial** berkala; jangka panjang migrasi ke **IAM Roles Anywhere**.

> Access Key & Secret yang tertulis di dokumen PDF awal adalah kredensial hidup -
> minta **rotate/revoke** setelah environment stabil.

---

## Struktur repo

```
cmd/tafw/main.go            # subcommand: upload | download | version
internal/config/            # muat & validasi konfigurasi
internal/logging/           # logger terstruktur (slog) + rotasi sederhana
internal/s3client/          # bungkus AWS SDK v2 + reader kredensial
internal/watch/             # deteksi file stabil sebelum diproses
internal/pipeline/          # alur upload & download
internal/state/             # idempotency (tidak upload/proses dua kali)
deploy/                     # contoh config, IAM policy, runbook deploy
BUILD.md                    # cara build binary
DEPLOY.md                   # cara deploy di server (tanpa sudo)
testdata/                   # contoh file TAFCM
```

---

## Status implementasi

- **Fase 1 (selesai)**: scaffold, config, logging, state, contoh config.
- **Fase 2 (selesai)**: uploader - stability check, PutObject (multipart + SSE),
  rename ke `.tafcm` huruf kecil, pindah file terkirim ke `INCOMING_SENT/`,
  pindah ke `ERROR/` + alasan bila gagal, idempotency state.
- **Fase 3 (selesai)**: downloader - list output, deteksi selesai
  (`status.json`=`COMPLETED` / `_SUCCESS` / mode stability), gabung
  `output/success=true/part-*` ke `OUTCOMING/<folder>.tafcm` dan
  `output/success=false/part-*` ke `ERROR/<folder>.tafcm` (header part pertama
  dipertahankan), tulis atomic, `ERROR/<folder>.reason` bila gagal, idempotency state.
- **Fase 4 (sebagian)**: unit systemd tersedia di runbook deploy.
- **Fase 5 (sebagian)**: unit test inti (grouping, urutan part, signature) selesai.

---

## Checklist UAT

- [ ] Folder `INCOMING/OUTCOMING/ERROR/INCOMING_SENT` ada di `/home/svr-adins`.
- [ ] `config.yaml` menunjuk bucket UAT & prefix `SANDBOX/`.
- [ ] File `credentials` di `chmod 600` (bukan di repo).
- [ ] `tafw upload --dry-run --once` validasi config + format kredensial + folder.
- [ ] Taruh file `.txt`/`.TAFCM` (tanpa spasi) di `INCOMING`, upload berjalan.
- [ ] File muncul di `s3://taf-batch/SANDBOX/` sebagai `.tafcm`, file
      input pindah ke `INCOMING_SENT`.
- [ ] Tunggu output `TAFCM_<datetime>/` muncul & `status.json` = `COMPLETED`.
- [ ] `tafw download` menghasilkan `TAFCM_<datetime>.tafcm` di `OUTCOMING`
      (atau di `ERROR` bila Provenir `success=false`).
- [ ] Uji jalur gagal: file + alasan muncul di `ERROR/`.

## Checklist Deploy (Prod)

- [ ] Semua poin UAT terpenuhi dengan bucket/prefix PROD.
- [ ] Service terpasang & aktif (`Restart=always`).
- [ ] `Linger=yes` untuk hidup saat logout, ATAU cron+flock aktif.
- [ ] Egress firewall hanya ke endpoint S3 (tanpa proxy).
- [ ] Kredensial di-rotate & jadwal rotasi dicatat.
- [ ] Backup/retensi folder `OUTCOMING` & `ERROR` disepakati.
- [ ] Monitoring alert untuk service down & file masuk `ERROR`.

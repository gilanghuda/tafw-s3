# BUILD — Cara Install & Build `tafw` (linux)

Panduan ini **hanya** sampai menghasilkan satu file binary `tafw`.
Untuk konfigurasi, kredensial, dan deploy ke server, lihat `deploy/`.

---

## ⚠️ WAJIB build di Linux (tidak boleh cross-compile dari Windows)

Program ini menggunakan **cgo** (default aktif saat build di Linux), supaya
resolver DNS memakai library sistem (`glibc`/`getaddrinfo`). Ini penting karena
di lingkungan DMZ, resolver pure-Go milik Go (yang dipakai bila build dengan
`CGO_ENABLED=0`) **gagal** query stub `127.0.0.53` milik `systemd-resolved`,
sehingga koneksi ke S3 timeout.

Konsekuensinya:

- ✅ Build **di Linux** (Ubuntu/Debian) dengan `gcc` terpasang.
- ❌ **Jangan** cross-compile dari Windows / pakai `CGO_ENABLED=0`.

Binary yang dihasilkan **dinamis** (ter-link ke `libc`), bukan statis.

---

## Prasyarat

- OS: **Linux** (Ubuntu/Debian) — disarankan versi yang se-kelas dengan server tujuan.
- Arsitektur: `x86_64` (amd64) atau `aarch64` (arm64).
- **Go 1.24+**.
- **gcc** (untuk cgo).
- Koneksi **internet** (untuk mengunduh dependency Go).

Cek arsitektur:

```bash
uname -m      # x86_64 atau aarch64
```

---

## 1. Install Go

### Opsi A — lewat apt (paling cepat)

```bash
sudo apt update
sudo apt install -y golang-go
go version
```

Pastikan hasilnya **go1.24 atau lebih baru**. Bila apt menyediakan versi lama,
pakai Opsi B.

### Opsi B — tarball resmi (dapat versi terbaru)

```bash
# Ganti versi/arsitektur bila perlu (amd64 / arm64)
curl -LO https://go.dev/dl/go1.24.5.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.24.5.linux-amd64.tar.gz
export PATH=$PATH:/usr/local/go/bin
go version
```

Agar permanen, tambahkan ke `~/.profile`:

```bash
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.profile
```

---

## 2. Install gcc (wajib untuk cgo)

```bash
sudo apt update
sudo apt install -y build-essential
gcc --version
```

---

## 3. Clone repository

```bash
git clone https://github.com/gilanghuda/tafw-s3.git
cd tafw-s3
```

---

## 4. Unduh dependency

```bash
go mod download
```

---

## 5. Build binary

```bash
go build -o tafw ./cmd/tafw
```

Perintah ini menghasilkan file **`tafw`** di folder saat ini.

---

## 6. Verifikasi binary

```bash
ls -l tafw
file tafw
```

Harapan pada `file tafw`:

```
ELF 64-bit LSB executable, ..., dynamically linked, ...
```

Kata **`dynamically linked`** menandakan cgo aktif (inilah yang diinginkan).

Cek program berjalan:

```bash
./tafw version
./tafw --help
```

---

## 7. Selesai

Binary `./tafw` siap dipakai/dipindahkan ke server target.

```bash
# contoh mengirim ke server
scp tafw <user>@<server>:/path/ke/folder/bin/tafw
```

Langkah selanjutnya (config, kredensial, service) ada di:

- `deploy/USERSPACE-DEPLOY.md` — deploy **tanpa sudo** (systemd `--user` / cron)
- `deploy/UBUNTU-DEPLOY.md` — deploy **dengan sudo** (system-wide)
- `deploy/config.yaml.example` & `deploy/config.userspace.yaml.example` — contoh config
- `deploy/credentials.example` — template kredensial AWS

---

## Catatan arsitektur (build untuk arm64)

Karena cgo **tidak bisa** cross-compile dengan mudah, build arm64 harus dilakukan
**di mesin ber-arsitektur arm64**. Di mesin tersebut cukup:

```bash
go build -o tafw ./cmd/tafw
```

---

## Troubleshooting

| Gejala | Solusi |
|---|---|
| `go: command not found` | `export PATH=$PATH:/usr/local/go/bin` lalu buka ulang shell |
| `go.mod requires go >= 1.24` | Naikkan versi Go (Opsi B) |
| `cgo: C compiler "gcc" not found` | Install gcc: `sudo apt install -y build-essential` |
| `file tafw` menampilkan `statically linked` | cgo tidak aktif; pastikan build **di Linux** dan **jangan** set `CGO_ENABLED=0` |

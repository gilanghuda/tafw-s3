// Package pipeline berisi alur kerja upload (INCOMING -> S3) dan download
// (output S3 -> gabungan -> OUTCOMING).
//
// FASE 2: implementasi uploader (stability check, PutObject, ERROR, state).
// FASE 3: implementasi downloader.
package pipeline

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"tafw/internal/config"
	"tafw/internal/s3client"
	"tafw/internal/state"
	"tafw/internal/watch"
)

// Options mengatur perilaku eksekusi pipeline.
type Options struct {
	// DryRun: tidak mengubah S3/filesystem, hanya mencatat aksi.
	DryRun bool
	// Once: jalankan satu siklus lalu keluar; false = loop sampai sinyal stop.
	Once bool
}

// EnsureFolders membuat folder kerja yang wajib ada (INCOMING, OUTCOMING,
// ERROR, INCOMING_SENT, state, logs). Folder ini WAJIB ada sebelum UAT/Deploy.
func EnsureFolders(cfg *config.Config) error {
	dirs := []string{
		cfg.Folders.Incoming,
		cfg.Folders.Outcoming,
		cfg.Folders.Error,
		cfg.Folders.IncomingSent,
		cfg.Folders.State,
		cfg.Folders.Logs,
	}
	for _, d := range dirs {
		if d == "" {
			continue
		}
		if err := os.MkdirAll(d, 0o750); err != nil {
			return fmt.Errorf("buat folder %s: %w", d, err)
		}
	}
	return nil
}

// Upload menjalankan watcher folder INCOMING -> S3.
func Upload(ctx context.Context, cfg *config.Config, log *slog.Logger, store *state.Store, opts Options) error {
	if err := EnsureFolders(cfg); err != nil {
		return err
	}
	log.Info("upload: folder siap",
		"incoming", cfg.Folders.Incoming,
		"outcoming", cfg.Folders.Outcoming,
		"error", cfg.Folders.Error,
	)

	client, err := s3client.New(ctx, cfg)
	if err != nil {
		return fmt.Errorf("inisialisasi klien S3: %w", err)
	}

	interval := time.Duration(cfg.Watcher.PollSeconds) * time.Second
	return loop(ctx, opts, log, "upload", interval, func(ctx context.Context) error {
		return uploadOnce(ctx, cfg, log, store, client, opts)
	})
}

// uploadOnce menjalankan satu siklus pemindaian INCOMING dan upload file yang
// cocok pola serta sudah stabil.
func uploadOnce(ctx context.Context, cfg *config.Config, log *slog.Logger, store *state.Store, client *s3client.Client, opts Options) error {
	entries, err := os.ReadDir(cfg.Folders.Incoming)
	if err != nil {
		return fmt.Errorf("baca folder incoming: %w", err)
	}

	var processed int
	for _, e := range entries {
		if ctx.Err() != nil {
			return nil
		}
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !watch.MatchAny(name, cfg.Watcher.FilePatterns) {
			continue
		}

		path := filepath.Join(cfg.Folders.Incoming, name)
		stamp, err := watch.Stamp(path)
		if err != nil {
			// File mungkin sudah hilang/dipindah proses lain; lewati.
			log.Debug("lewati file (tidak bisa dibaca)", "file", name, "err", err)
			continue
		}

		// Idempotency: sudah pernah diupload dan tidak berubah.
		if rec, ok := store.Uploaded(name); ok &&
			rec.Size == stamp.Size && rec.ModTime.Equal(stamp.ModTime) {
			log.Debug("lewati file (sudah diupload)", "file", name)
			continue
		}

		// Tunggu file selesai ditulis.
		if !watch.WaitStable(ctx, path, time.Duration(cfg.Watcher.SettleSeconds)*time.Second, log) {
			if ctx.Err() != nil {
				return nil
			}
			log.Debug("file belum stabil, dicoba siklus berikutnya", "file", name)
			continue
		}
		// Ambil stamp terbaru setelah stabil.
		if s, err := watch.Stamp(path); err == nil {
			stamp = s
		}

		key := cfg.S3.InputPrefix + watch.ObjectName(name, cfg.Watcher.RenameInputLower)

		if opts.DryRun {
			log.Info("dry-run: akan upload", "file", name, "key", key, "size", stamp.Size)
			processed++
			continue
		}

		if err := client.PutFile(ctx, path, key); err != nil {
			log.Error("upload gagal", "file", name, "key", key, "err", err)
			if err := moveToError(cfg.Folders.Error, path, key, err); err != nil {
				log.Error("gagal memindahkan file ke ERROR", "file", name, "err", err)
			}
			continue
		}

		if err := store.MarkUploaded(name, state.UploadRecord{
			Size:    stamp.Size,
			ModTime: stamp.ModTime,
			S3Key:   key,
		}); err != nil {
			// Upload sukses; kegagalan mencatat state tidak fatal (akan diupload
			// ulang dan S3 PutObject bersifat idempotent untuk key sama).
			log.Warn("gagal mencatat state upload", "file", name, "err", err)
		}

		// Pindahkan file input yang sudah terkirim ke INCOMING_SENT.
		if err := moveToDir(cfg.Folders.IncomingSent, path); err != nil {
			log.Warn("gagal memindahkan file ke INCOMING_SENT", "file", name, "err", err)
		}
		log.Info("upload sukses", "file", name, "key", key, "size", stamp.Size)
		processed++
	}

	if processed == 0 {
		log.Debug("siklus upload selesai: tidak ada file baru")
	} else {
		log.Info("siklus upload selesai", "jumlah", processed)
	}
	return nil
}

// moveToDir memindahkan file src ke folder dst (membuat dst bila perlu).
// Bila nama tujuan sudah ada, ditambahkan suffix waktu. Fallback copy+remove
// dipakai bila rename lintas device gagal.
func moveToDir(dir, src string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	base := filepath.Base(src)
	dst := filepath.Join(dir, base)
	if _, err := os.Stat(dst); err == nil {
		dst = filepath.Join(dir, fmt.Sprintf("%s.%d", base, time.Now().Unix()))
	}
	if err := os.Rename(src, dst); err != nil {
		if err2 := copyThenRemove(src, dst); err2 != nil {
			return fmt.Errorf("rename gagal (%v) dan copy gagal: %w", err, err2)
		}
	}
	return nil
}

// moveToError memindahkan file ke folder ERROR dan menulis file alasan
// <name>.reason. Bila nama tujuan sudah ada, ditambahkan suffix waktu.
// Fallback copy+remove dipakai bila rename lintas device gagal.
func moveToError(errorDir, src, key string, cause error) error {
	if err := os.MkdirAll(errorDir, 0o750); err != nil {
		return err
	}
	base := filepath.Base(src)
	dst := filepath.Join(errorDir, base)
	if _, err := os.Stat(dst); err == nil {
		dst = filepath.Join(errorDir, fmt.Sprintf("%s.%d", base, time.Now().Unix()))
	}

	if err := os.Rename(src, dst); err != nil {
		if err2 := copyThenRemove(src, dst); err2 != nil {
			return fmt.Errorf("rename gagal (%v) dan copy gagal: %w", err, err2)
		}
	}

	reason := fmt.Sprintf("waktu: %s\ns3_key: %s\nerror: %v\n",
		time.Now().Format(time.RFC3339), key, cause)
	if err := os.WriteFile(dst+".reason", []byte(reason), 0o640); err != nil {
		return fmt.Errorf("tulis alasan: %w", err)
	}
	return nil
}

// copyThenRemove menyalin src ke dst lalu menghapus src (untuk rename lintas device).
func copyThenRemove(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Remove(src)
}

// Download menjalankan poller output S3 -> gabungan -> OUTCOMING.
func Download(ctx context.Context, cfg *config.Config, log *slog.Logger, store *state.Store, opts Options) error {
	if err := EnsureFolders(cfg); err != nil {
		return err
	}
	log.Info("download: folder siap",
		"outcoming", cfg.Folders.Outcoming,
		"error", cfg.Folders.Error,
	)

	client, err := s3client.New(ctx, cfg)
	if err != nil {
		return fmt.Errorf("inisialisasi klien S3: %w", err)
	}
	d := &downloader{
		cfg:       cfg,
		log:       log,
		store:     store,
		client:    client,
		stability: map[string]stabilityState{},
	}

	interval := time.Duration(cfg.Watcher.PollSeconds) * time.Second
	return loop(ctx, opts, log, "download", interval, func(ctx context.Context) error {
		return d.runOnce(ctx, opts)
	})
}

// loop menjalankan fn secara berkala hingga ctx dibatalkan atau opts.Once.
func loop(ctx context.Context, opts Options, log *slog.Logger, name string, interval time.Duration, fn func(context.Context) error) error {
	for {
		if err := fn(ctx); err != nil {
			// FASE 3 akan memperkaya penanganan error per-file;
			// sementara cukup log agar loop tidak berhenti total.
			log.Error(name+": siklus gagal", "err", err)
		}
		if opts.Once {
			return nil
		}
		select {
		case <-ctx.Done():
			log.Info(name + ": berhenti")
			return nil
		case <-time.After(interval):
		}
	}
}

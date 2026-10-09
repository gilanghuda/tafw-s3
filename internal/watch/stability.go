// Package watch menyediakan helper untuk mendeteksi file yang sudah selesai
// ditulis (stabil) sebelum diproses lebih lanjut, serta pencocokan pola nama
// file yang tidak membedakan huruf besar/kecil.
package watch

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FileStamp adalah potret ukuran + waktu modifikasi sebuah file.
type FileStamp struct {
	Size    int64
	ModTime time.Time
}

// Stamp mengambil FileStamp dari path.
func Stamp(path string) (FileStamp, error) {
	info, err := os.Stat(path)
	if err != nil {
		return FileStamp{}, err
	}
	return FileStamp{Size: info.Size(), ModTime: info.ModTime()}, nil
}

// Stable memeriksa apakah file belum berubah dibanding stamp sebelumnya.
func (s FileStamp) Stable(prev FileStamp) bool {
	return s.Size == prev.Size && s.ModTime.Equal(prev.ModTime)
}

// WaitStable menunggu sampai file berhenti berubah (ukuran + mtime konstan)
// selama minimal settle. Mengembalikan true bila stabil, false bila file
// menghilang atau ctx dibatalkan sebelum stabil.
func WaitStable(ctx context.Context, path string, settle time.Duration, log *slog.Logger) bool {
	prev, err := Stamp(path)
	if err != nil {
		return false
	}

	// Bila settle <= 0, anggap langsung stabil.
	if settle <= 0 {
		return true
	}

	stableSince := time.Time{}
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			cur, err := Stamp(path)
			if err != nil {
				return false // file hilang / tak bisa dibaca
			}
			if !cur.Stable(prev) {
				prev = cur
				stableSince = time.Time{}
				continue
			}
			if stableSince.IsZero() {
				stableSince = time.Now()
			}
			if time.Since(stableSince) >= settle {
				if log != nil {
					log.Debug("file stabil", "file", path, "size", cur.Size)
				}
				return true
			}
		}
	}
}

// MatchAny mencocokkan nama file terhadap daftar pola secara case-insensitive.
// Pola memakai sintaks filepath.Match (mis. "*.TAFCM").
func MatchAny(name string, patterns []string) bool {
	for _, p := range patterns {
		ok, err := filepath.Match(strings.ToLower(p), strings.ToLower(name))
		if err == nil && ok {
			return true
		}
	}
	return false
}

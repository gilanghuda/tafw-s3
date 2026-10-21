// Package logging menyediakan logger terstruktur (log/slog) yang menulis ke
// stdout dan, bila dikonfigurasi, ke file dengan penanganan rotasi sederhana.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// maxLogBytes adalah ambang rotasi sederhana: bila file log melebihi ukuran ini
// saat dibuka, file lama di-rename menjadi <file>.1 dan file baru dibuat.
const maxLogBytes = 50 * 1024 * 1024 // 50 MiB

// New membuat logger berdasarkan level dan path file opsional.
// Bila file kosong, log hanya ditulis ke stdout.
func New(level, file string) (*slog.Logger, io.Closer, error) {
	return NewWithRoll(level, file, false)
}

// NewWithRoll seperti New, namun bila dailyRoll true, log ditulis ke file
// harian <dir>/tafw-YYYYMMDD.log; dalam mode ini File diperlakukan sebagai
// direktori tujuan.
func NewWithRoll(level, file string, dailyRoll bool) (*slog.Logger, io.Closer, error) {
	lvl, err := parseLevel(level)
	if err != nil {
		return nil, nil, err
	}

	var writers []io.Writer
	writers = append(writers, os.Stdout)

	var closer io.Closer
	if file != "" {
		if dailyRoll {
			dir := file
			if err := os.MkdirAll(dir, 0o750); err != nil {
				return nil, nil, fmt.Errorf("buat direktori log: %w", err)
			}
			f, c, err := newDailyWriter(dir, time.Now())
			if err != nil {
				return nil, nil, err
			}
			writers = append(writers, f)
			closer = c
		} else {
			if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
				return nil, nil, fmt.Errorf("buat direktori log: %w", err)
			}
			f, err := openRotated(file)
			if err != nil {
				return nil, nil, err
			}
			writers = append(writers, f)
			closer = f
		}
	}

	out := io.MultiWriter(writers...)
	handler := slog.NewJSONHandler(out, &slog.HandlerOptions{Level: lvl})
	return slog.New(handler), closerOrNop(closer), nil
}

// openRotated membuka file log dengan rotasi sederhana sekali di awal.
func openRotated(path string) (*os.File, error) {
	if info, err := os.Stat(path); err == nil && info.Size() >= maxLogBytes {
		_ = os.Rename(path, path+".1")
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
}

// dailyWriter menulis ke file log harian; saat tanggal berganti, file baru
// dibuat dan file lama ditutup.
type dailyWriter struct {
	mu   sync.Mutex
	dir  string
	day  string
	file *os.File
}

func newDailyWriter(dir string, t time.Time) (*dailyWriter, io.Closer, error) {
	w := &dailyWriter{dir: dir}
	if err := w.rotate(t); err != nil {
		return nil, nil, err
	}
	return w, w, nil
}

func (w *dailyWriter) rotate(t time.Time) error {
	day := t.Format("20060102")
	if w.file != nil && day == w.day {
		return nil
	}
	if w.file != nil {
		_ = w.file.Close()
		w.file = nil
	}
	path := filepath.Join(w.dir, "tafw-"+day+".log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return fmt.Errorf("buka file log harian: %w", err)
	}
	w.file = f
	w.day = day
	return nil
}

func (w *dailyWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.rotate(time.Now()); err != nil {
		return 0, err
	}
	return w.file.Write(p)
}

func (w *dailyWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		return w.file.Close()
	}
	return nil
}

func parseLevel(level string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("level log tidak dikenal: %q", level)
	}
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

func closerOrNop(c io.Closer) io.Closer {
	if c == nil {
		return nopCloser{}
	}
	return c
}

// SyncWriter membungkus io.Writer dengan mutex agar aman dipakai bersamaan
// (mis. untuk menulis ringkasan manual bila diperlukan).
type SyncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

// NewSyncWriter membungkus w supaya aman dari penulisan konkuren.
func NewSyncWriter(w io.Writer) *SyncWriter { return &SyncWriter{w: w} }

// Write menulis p secara serialisasi.
func (s *SyncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

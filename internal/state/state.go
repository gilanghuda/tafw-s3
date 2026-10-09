// Package state menyimpan catatan idempotency supaya file tidak diupload dua
// kali dan folder output tidak diproses berulang.
//
// Implementasi saat ini berbasis file JSON tunggal dengan penulisan atomic.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Store adalah state idempotency yang aman dipakai bersamaan.
type Store struct {
	mu   sync.Mutex
	path string
	data data
}

// data adalah bentuk serialisasi state ke disk.
type data struct {
	// Uploaded memetakan key file lokal -> metadata upload.
	Uploaded map[string]UploadRecord `json:"uploaded"`
	// ProcessedOutputs memetakan nama folder output S3 -> metadata proses.
	ProcessedOutputs map[string]ProcessedRecord `json:"processed_outputs"`
}

// UploadRecord mencatat file yang sudah diupload agar tidak diulang.
type UploadRecord struct {
	Size       int64     `json:"size"`
	ModTime    time.Time `json:"mod_time"`
	SHA256     string    `json:"sha256,omitempty"`
	S3Key      string    `json:"s3_key"`
	UploadedAt time.Time `json:"uploaded_at"`
}

// ProcessedRecord mencatat folder output S3 yang sudah diproses.
type ProcessedRecord struct {
	OutFile     string    `json:"out_file"`
	Parts       int       `json:"parts"`
	RowsWritten int64     `json:"rows_written"`
	ProcessedAt time.Time `json:"processed_at"`
}

// Open memuat store dari path. Bila file belum ada, store kosong dibuat.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("buat direktori state: %w", err)
	}

	s := &Store{
		path: path,
		data: data{
			Uploaded:         map[string]UploadRecord{},
			ProcessedOutputs: map[string]ProcessedRecord{},
		},
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("baca state: %w", err)
	}
	if len(raw) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(raw, &s.data); err != nil {
		return nil, fmt.Errorf("parse state: %w", err)
	}
	if s.data.Uploaded == nil {
		s.data.Uploaded = map[string]UploadRecord{}
	}
	if s.data.ProcessedOutputs == nil {
		s.data.ProcessedOutputs = map[string]ProcessedRecord{}
	}
	return s, nil
}

// Uploaded mengembalikan record upload untuk key, dan apakah ada.
func (s *Store) Uploaded(key string) (UploadRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.data.Uploaded[key]
	return r, ok
}

// MarkUploaded mencatat file lokal sebagai sudah diupload.
func (s *Store) MarkUploaded(key string, rec UploadRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec.UploadedAt.IsZero() {
		rec.UploadedAt = time.Now().UTC()
	}
	s.data.Uploaded[key] = rec
	return s.flushLocked()
}

// Processed mengembalikan record proses untuk folder output, dan apakah ada.
func (s *Store) Processed(folder string) (ProcessedRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.data.ProcessedOutputs[folder]
	return r, ok
}

// MarkProcessed mencatat folder output sebagai sudah diproses.
func (s *Store) MarkProcessed(folder string, rec ProcessedRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec.ProcessedAt.IsZero() {
		rec.ProcessedAt = time.Now().UTC()
	}
	s.data.ProcessedOutputs[folder] = rec
	return s.flushLocked()
}

// flushLocked menulis state ke disk secara atomic (tmp + rename).
// Pemanggil harus memegang s.mu.
func (s *Store) flushLocked() error {
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o640); err != nil {
		return fmt.Errorf("tulis state sementara: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("ganti state: %w", err)
	}
	return nil
}

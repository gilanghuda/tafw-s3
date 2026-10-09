// Package config memuat dan memvalidasi konfigurasi tafw dari file YAML
// dengan kemampuan override lewat environment variable.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultLocation adalah path konfigurasi default.
const DefaultLocation = "/etc/tafwatcher/config.yaml"

// Config adalah keseluruhan konfigurasi aplikasi.
type Config struct {
	S3          S3Config          `yaml:"s3"`
	Credentials CredentialsConfig `yaml:"credentials"`
	Folders     FoldersConfig     `yaml:"folders"`
	Watcher     WatcherConfig     `yaml:"watcher"`
	Logging     LoggingConfig     `yaml:"logging"`
}

// S3Config mengatur koneksi dan lokasi objek di S3.
type S3Config struct {
	Bucket       string `yaml:"bucket"`
	Region       string `yaml:"region"`
	Endpoint     string `yaml:"endpoint"`
	InputPrefix  string `yaml:"input_prefix"`
	OutputPrefix string `yaml:"output_prefix"`
	SSE          string `yaml:"sse"`
	KMSKeyID     string `yaml:"kms_key_id"`
}

// CredentialsConfig mengatur sumber kredensial AWS.
//
//	mode = "file"          -> baca access key/secret key dari Credentials.File (chmod 0600/0640)
//	mode = "default-chain" -> pakai mekanisme default AWS SDK (env, shared config, IAM Roles Anywhere, dsb.)
type CredentialsConfig struct {
	Mode string `yaml:"mode"`
	File string `yaml:"file"`
}

// FoldersConfig berisi path folder kerja. Folder INCOMING, OUTCOMING, dan ERROR
// WAJIB dibuat sebelum aplikasi dijalankan (lihat README).
type FoldersConfig struct {
	Incoming  string `yaml:"incoming"`
	Outcoming string `yaml:"outcoming"`
	Error     string `yaml:"error"`
	State     string `yaml:"state"`
	Logs      string `yaml:"logs"`
}

// WatcherConfig mengatur perilaku watcher upload & downloader.
type WatcherConfig struct {
	// SettleSeconds adalah lama menunggu ukuran file tidak berubah sebelum
	// file dianggap selesai ditulis dan aman untuk diupload.
	SettleSeconds int `yaml:"settle_seconds"`
	// PollSeconds adalah interval polling ke S3 untuk mencari output baru.
	PollSeconds int `yaml:"poll_seconds"`
	// CompletionMethod: "status_json" (baca status.json == COMPLETED) atau
	// "stability" (fallback, folder stabil selama ReliabilityWindows).
	CompletionMethod string `yaml:"completion_method"`
	// FilePatterns adalah pola nama file yang diproses uploader (case-insensitive).
	FilePatterns []string `yaml:"file_patterns"`
	// SuccessFolder / FailedFolder adalah nama subfolder output di dalam
	// folder TAFCM_<datetime>/output/. Default success=true / success=false.
	SuccessFolder string `yaml:"success_folder"`
	FailedFolder  string `yaml:"failed_folder"`
	// OutputDirName adalah nama folder root di bawah output_prefix S3.
	OutputDirName string `yaml:"output_dir_name"`
	// OutputFolderPrefix adalah awalan nama folder output (mis. "TAFCM_").
	OutputFolderPrefix string `yaml:"output_folder_prefix"`
	// StabilityWindows adalah jumlah siklus "tidak berubah" untuk
	// completion_method=stability sebelum folder dianggap selesai.
	StabilityWindows int `yaml:"stability_windows"`
	// OutputSuffix adalah akhiran nama file gabungan di OUTCOMING (mis. ".txt").
	OutputSuffix string `yaml:"output_suffix"`
	// VerifyCounts membandingkan jumlah baris hasil gabungan dengan total_success
	// dari overall-status (bila true). Default false (advisory).
	VerifyCounts bool `yaml:"verify_counts"`
}

// LoggingConfig mengatur level dan tujuan log.
type LoggingConfig struct {
	Level string `yaml:"level"` // debug | info | warn | error
	File  string `yaml:"file"`  // kosong = hanya ke stdout
}

// Load membaca konfigurasi dari path (bila kosong pakai DefaultLocation),
// menerapkan override dari environment variable, lalu memvalidasi hasilnya.
func Load(path string) (*Config, error) {
	if path == "" {
		path = DefaultLocation
	}

	cfg := Default()

	if data, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("baca config %s: %w", path, err)
	} else if path != DefaultLocation {
		// Path eksplisit diberikan tapi file tidak ada: ini error.
		return nil, fmt.Errorf("config tidak ditemukan: %s", path)
	}

	applyEnv(cfg)

	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Default mengembalikan konfigurasi default yang aman.
func Default() *Config {
	return &Config{
		S3: S3Config{
			Bucket:       "taf-batch",
			Region:       "ap-southeast-3",
			InputPrefix:  "sandbox/",
			OutputPrefix: "sandbox/",
			SSE:          "AES256",
		},
		Credentials: CredentialsConfig{
			Mode: "file",
			File: "/etc/tafwatcher/credentials",
		},
		Folders: FoldersConfig{
			Incoming:  "/opt/tafwatcher/INCOMING",
			Outcoming: "/opt/tafwatcher/OUTCOMING",
			Error:     "/opt/tafwatcher/ERROR",
			State:     "/opt/tafwatcher/state",
			Logs:      "/opt/tafwatcher/logs",
		},
		Watcher: WatcherConfig{
			SettleSeconds:      10,
			PollSeconds:        30,
			CompletionMethod:   "status_json",
			FilePatterns:       []string{"*.TAFCM"},
			SuccessFolder:      "success=true",
			FailedFolder:       "success=false",
			OutputDirName:      "output",
			OutputFolderPrefix: "TAFCM_",
			StabilityWindows:   2,
			OutputSuffix:       ".txt",
			VerifyCounts:       false,
		},
		Logging: LoggingConfig{
			Level: "info",
		},
	}
}

// applyEnv menimpa sebagian nilai dari environment variable agar memudahkan
// konfigurasi di systemd (Environment=).
func applyEnv(c *Config) {
	setStr(&c.S3.Bucket, "TAFW_S3_BUCKET")
	setStr(&c.S3.Region, "TAFW_S3_REGION")
	setStr(&c.S3.Endpoint, "TAFW_S3_ENDPOINT")
	setStr(&c.S3.InputPrefix, "TAFW_INPUT_PREFIX")
	setStr(&c.S3.OutputPrefix, "TAFW_OUTPUT_PREFIX")
	setStr(&c.S3.SSE, "TAFW_S3_SSE")
	setStr(&c.S3.KMSKeyID, "TAFW_S3_KMS_KEY_ID")
	setStr(&c.Credentials.Mode, "TAFW_CREDENTIALS_MODE")
	setStr(&c.Credentials.File, "TAFW_CREDENTIALS_FILE")
	setStr(&c.Folders.Incoming, "TAFW_INCOMING")
	setStr(&c.Folders.Outcoming, "TAFW_OUTCOMING")
	setStr(&c.Folders.Error, "TAFW_ERROR")
	setStr(&c.Folders.State, "TAFW_STATE")
	setStr(&c.Folders.Logs, "TAFW_LOGS")
	setStr(&c.Watcher.CompletionMethod, "TAFW_COMPLETION_METHOD")
	setStr(&c.Watcher.OutputFolderPrefix, "TAFW_OUTPUT_FOLDER_PREFIX")
	setStr(&c.Watcher.OutputSuffix, "TAFW_OUTPUT_SUFFIX")
	setStr(&c.Logging.Level, "TAFW_LOG_LEVEL")
	setStr(&c.Logging.File, "TAFW_LOG_FILE")
	setInt(&c.Watcher.SettleSeconds, "TAFW_SETTLE_SECONDS")
	setInt(&c.Watcher.PollSeconds, "TAFW_POLL_SECONDS")
	setInt(&c.Watcher.StabilityWindows, "TAFW_STABILITY_WINDOWS")
	setBool(&c.Watcher.VerifyCounts, "TAFW_VERIFY_COUNTS")
}

// normalize membersihkan input (trim spasi, samakan separator prefix).
func (c *Config) normalize() error {
	c.S3.Bucket = strings.TrimSpace(c.S3.Bucket)
	c.S3.Region = strings.TrimSpace(c.S3.Region)
	c.S3.Endpoint = strings.TrimSpace(c.S3.Endpoint)
	c.S3.SSE = strings.ToUpper(strings.TrimSpace(c.S3.SSE))
	c.S3.KMSKeyID = strings.TrimSpace(c.S3.KMSKeyID)
	c.S3.InputPrefix = normalizePrefix(c.S3.InputPrefix)
	c.S3.OutputPrefix = normalizePrefix(c.S3.OutputPrefix)

	c.Credentials.Mode = strings.ToLower(strings.TrimSpace(c.Credentials.Mode))
	c.Credentials.File = strings.TrimSpace(c.Credentials.File)

	c.Watcher.CompletionMethod = strings.ToLower(strings.TrimSpace(c.Watcher.CompletionMethod))
	c.Watcher.SuccessFolder = strings.Trim(strings.TrimSpace(c.Watcher.SuccessFolder), "/")
	c.Watcher.FailedFolder = strings.Trim(strings.TrimSpace(c.Watcher.FailedFolder), "/")
	c.Watcher.OutputDirName = strings.Trim(strings.TrimSpace(c.Watcher.OutputDirName), "/")
	c.Watcher.OutputFolderPrefix = strings.TrimSpace(c.Watcher.OutputFolderPrefix)
	c.Watcher.OutputSuffix = strings.TrimSpace(c.Watcher.OutputSuffix)

	c.Folders.Incoming = cleanPath(c.Folders.Incoming)
	c.Folders.Outcoming = cleanPath(c.Folders.Outcoming)
	c.Folders.Error = cleanPath(c.Folders.Error)
	c.Folders.State = cleanPath(c.Folders.State)
	c.Folders.Logs = cleanPath(c.Folders.Logs)

	c.Logging.Level = strings.ToLower(strings.TrimSpace(c.Logging.Level))

	if len(c.Watcher.FilePatterns) == 0 {
		c.Watcher.FilePatterns = []string{"*.TAFCM"}
	}
	return nil
}

// Validate memastikan nilai konfigurasi konsisten sebelum dipakai.
func (c *Config) Validate() error {
	var errs []string

	if c.S3.Bucket == "" {
		errs = append(errs, "s3.bucket wajib diisi")
	}
	if c.S3.Region == "" {
		errs = append(errs, "s3.region wajib diisi")
	}
	if c.S3.InputPrefix == "" {
		errs = append(errs, "s3.input_prefix wajib diisi")
	}
	if c.S3.OutputPrefix == "" {
		errs = append(errs, "s3.output_prefix wajib diisi")
	}
	switch c.S3.SSE {
	case "", "AES256", "aws:kms":
	default:
		errs = append(errs, "s3.sse harus AES256 atau aws:kms")
	}
	if c.S3.SSE == "aws:kms" && c.S3.KMSKeyID == "" {
		errs = append(errs, "s3.kms_key_id wajib diisi bila sse=aws:kms")
	}

	switch c.Credentials.Mode {
	case "file":
		if c.Credentials.File == "" {
			errs = append(errs, "credentials.file wajib diisi bila mode=file")
		}
	case "default-chain":
	default:
		errs = append(errs, "credentials.mode harus file atau default-chain")
	}

	for name, p := range map[string]string{
		"folders.incoming":  c.Folders.Incoming,
		"folders.outcoming": c.Folders.Outcoming,
		"folders.error":     c.Folders.Error,
		"folders.state":     c.Folders.State,
	} {
		if p == "" {
			errs = append(errs, name+" wajib diisi")
		}
	}

	switch c.Watcher.CompletionMethod {
	case "status_json", "stability":
	default:
		errs = append(errs, "watcher.completion_method harus status_json atau stability")
	}
	if c.Watcher.SettleSeconds < 0 {
		errs = append(errs, "watcher.settle_seconds tidak boleh negatif")
	}
	if c.Watcher.PollSeconds <= 0 {
		errs = append(errs, "watcher.poll_seconds harus > 0")
	}
	if c.Watcher.SuccessFolder == "" {
		errs = append(errs, "watcher.success_folder wajib diisi")
	}
	if c.Watcher.OutputDirName == "" {
		errs = append(errs, "watcher.output_dir_name wajib diisi")
	}
	if c.Watcher.OutputFolderPrefix == "" {
		errs = append(errs, "watcher.output_folder_prefix wajib diisi")
	}
	if c.Watcher.StabilityWindows < 1 {
		errs = append(errs, "watcher.stability_windows harus >= 1")
	}

	switch c.Logging.Level {
	case "", "debug", "info", "warn", "error":
	default:
		errs = append(errs, "logging.level harus debug, info, warn, atau error")
	}

	if len(errs) > 0 {
		return fmt.Errorf("konfigurasi tidak valid:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

// normalizePrefix memastikan prefix diakhiri "/" dan tidak diawali "/".
func normalizePrefix(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "/")
	if p != "" && !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}

func cleanPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return p
	}
	return filepath.Clean(p)
}

func setStr(dst *string, key string) {
	if v, ok := os.LookupEnv(key); ok {
		*dst = v
	}
}

func setInt(dst *int, key string) {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			*dst = n
		}
	}
}

func setBool(dst *bool, key string) {
	if v, ok := os.LookupEnv(key); ok {
		if b, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
			*dst = b
		}
	}
}

// Command tafw adalah watcher TAFCM <-> S3.
//
// Dua mode utama:
//
//	tafw upload    watch folder INCOMING, upload file *.txt/*.TAFCM ke S3
//	               (SANDBOX/, nama di-rename jadi .tafcm huruf kecil)
//	tafw download  poll S3 (SANDBOX/): success=true -> OUTCOMING,
//	               success=false -> ERROR (hasil gabungan <folder>.tafcm)
//
// Lihat README.md untuk kebutuhan folder INCOMING/OUTCOMING/ERROR/INCOMING_SENT
// dan langkah UAT/Deploy.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"tafw/internal/config"
	"tafw/internal/logging"
	"tafw/internal/pipeline"
	"tafw/internal/state"
)

// version diisi saat build: -ldflags "-X main.version=1.0.0".
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return errors.New("subcommand wajib: upload | download | version")
	}

	switch args[0] {
	case "version", "-v", "--version":
		fmt.Printf("tafw %s\n", version)
		return nil
	case "upload":
		return runMode(args[1:], "upload")
	case "download":
		return runMode(args[1:], "download")
	case "-h", "--help", "help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("subcommand tidak dikenal: %q", args[0])
	}
}

func runMode(args []string, mode string) error {
	fs := flag.NewFlagSet(mode, flag.ContinueOnError)
	cfgPath := fs.String("config", "", "path konfigurasi (default "+config.DefaultLocation+")")
	dryRun := fs.Bool("dry-run", false, "jalankan tanpa mengubah S3/filesystem (uji konektivitas & permission)")
	once := fs.Bool("once", false, "jalankan satu siklus lalu keluar (tanpa loop)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}

	log, closer, err := logging.NewWithRoll(cfg.Logging.Level, cfg.Logging.File, cfg.Logging.DailyRoll)
	if err != nil {
		return err
	}
	defer closer.Close()

	log.Info("tafw start",
		"version", version,
		"mode", mode,
		"dry_run", *dryRun,
		"once", *once,
		"bucket", cfg.S3.Bucket,
		"region", cfg.S3.Region,
	)

	store, err := state.Open(cfg.Folders.State + "/state.json")
	if err != nil {
		return err
	}

	// Tangani sinyal terminasi agar systemd stop bersih.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	opts := pipeline.Options{DryRun: *dryRun, Once: *once}

	switch mode {
	case "upload":
		return pipeline.Upload(ctx, cfg, log, store, opts)
	case "download":
		return pipeline.Download(ctx, cfg, log, store, opts)
	default:
		return fmt.Errorf("mode tidak dikenal: %q", mode)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `tafw - watcher TAFCM <-> S3

Penggunaan:
  tafw upload    [--config PATH] [--dry-run] [--once]
  tafw download  [--config PATH] [--dry-run] [--once]
  tafw version

Folder INCOMING, OUTCOMING, ERROR, dan INCOMING_SENT wajib dibuat lebih dulu.
Lihat README.md bagian "Setup Folder (WAJIB untuk UAT & Deploy)".
`)
}

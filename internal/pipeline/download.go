package pipeline

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"tafw/internal/config"
	"tafw/internal/s3client"
	"tafw/internal/state"
)

// downloader menjalankan alur output S3 -> gabungan -> OUTCOMING.
type downloader struct {
	cfg       *config.Config
	log       *slog.Logger
	store     *state.Store
	client    *s3client.Client
	stability map[string]stabilityState
}

// stabilityState melacak kestabilan sebuah folder output antar siklus.
type stabilityState struct {
	sig     string
	windows int
}

// outputGroup adalah kumpulan objek S3 yang berada dalam satu folder output.
type outputGroup struct {
	folder  string // contoh: "TAFCM_2026_4_22_12h_54m_54s"
	objects []s3client.ObjectInfo
}

// runDownloadOnce dipertahankan tidak ada; Download di pipeline.go membuat
// client sekali lalu memanggil d.runOnce tiap siklus.

func (d *downloader) runOnce(ctx context.Context, opts Options) error {
	objs, err := d.client.List(ctx, d.cfg.S3.OutputPrefix)
	if err != nil {
		return err
	}

	groups := groupOutputs(objs, d.cfg.S3.OutputPrefix, d.cfg.Watcher.OutputFolderPrefix)
	// Urut nama folder (deterministik) untuk catch-up semua yang belum diproses.
	sort.Slice(groups, func(i, j int) bool { return groups[i].folder < groups[j].folder })

	var processed int
	for _, g := range groups {
		if ctx.Err() != nil {
			return nil
		}
		if _, ok := d.store.Processed(g.folder); ok {
			continue
		}

		done, err := d.isComplete(ctx, g)
		if err != nil {
			d.log.Warn("gagal mendeteksi status output", "folder", g.folder, "err", err)
			continue
		}
		if !done {
			d.log.Debug("output belum selesai", "folder", g.folder, "method", d.cfg.Watcher.CompletionMethod)
			continue
		}

		parts := d.collectParts(g)
		if len(parts) == 0 {
			// Hanya ada success=false (atau tidak ada part sukses): lewati tanpa file.
			d.log.Warn("tidak ada part success=true, dilewati", "folder", g.folder)
			if !opts.DryRun {
				if err := d.store.MarkProcessed(g.folder, state.ProcessedRecord{Parts: 0}); err != nil {
					d.log.Warn("gagal mencatat state", "folder", g.folder, "err", err)
				}
			}
			continue
		}

		outName := g.folder + d.cfg.Watcher.OutputSuffix
		if opts.DryRun {
			d.log.Info("dry-run: akan gabung output",
				"folder", g.folder, "parts", len(parts), "out", outName)
			processed++
			continue
		}

		rows, err := d.concatAndWrite(ctx, g, parts, outName)
		if err != nil {
			d.log.Error("gagal menggabungkan output", "folder", g.folder, "err", err)
			_ = d.writeError(g.folder, err)
			continue
		}

		if d.cfg.Watcher.VerifyCounts {
			want, ok, verr := d.expectedSuccess(ctx, g)
			if verr != nil {
				d.log.Warn("verifikasi jumlah baris dilewati", "folder", g.folder, "err", verr)
			} else if ok && want != rows {
				err := fmt.Errorf("jumlah baris %d != total_success %d", rows, want)
				d.log.Error("verifikasi gagal", "folder", g.folder, "err", err)
				_ = os.Remove(filepath.Join(d.cfg.Folders.Outcoming, outName))
				_ = d.writeError(g.folder, err)
				continue
			}
		}

		if err := d.store.MarkProcessed(g.folder, state.ProcessedRecord{
			OutFile:     outName,
			Parts:       len(parts),
			RowsWritten: rows,
		}); err != nil {
			d.log.Warn("gagal mencatat state", "folder", g.folder, "err", err)
		}
		d.log.Info("output digabung",
			"folder", g.folder, "parts", len(parts), "rows", rows, "out", outName)
		processed++
	}

	if processed == 0 {
		d.log.Debug("siklus download selesai: tidak ada output baru")
	} else {
		d.log.Info("siklus download selesai", "jumlah", processed)
	}
	return nil
}

// groupOutputs mengelompokkan objek berdasarkan folder tingkat-1 di bawah
// outputPrefix yang diawali folderPrefix.
func groupOutputs(objs []s3client.ObjectInfo, outputPrefix, folderPrefix string) []outputGroup {
	byFolder := map[string][]s3client.ObjectInfo{}
	for _, o := range objs {
		if !strings.HasPrefix(o.Key, outputPrefix) {
			continue
		}
		rest := o.Key[len(outputPrefix):]
		// Buang key tepat di prefix (tanpa isi).
		seg, _, ok := strings.Cut(rest, "/")
		if !ok || seg == "" {
			continue
		}
		if !strings.HasPrefix(seg, folderPrefix) {
			continue
		}
		byFolder[seg] = append(byFolder[seg], o)
	}

	groups := make([]outputGroup, 0, len(byFolder))
	for folder, list := range byFolder {
		groups = append(groups, outputGroup{folder: folder, objects: list})
	}
	return groups
}

// isComplete menentukan apakah folder output sudah selesai diproses Provenir.
func (d *downloader) isComplete(ctx context.Context, g outputGroup) (bool, error) {
	switch d.cfg.Watcher.CompletionMethod {
	case "stability":
		return d.isStable(g), nil
	default: // status_json
		return d.statusComplete(ctx, g)
	}
}

// statusComplete membaca status.json dan mengecek "status" == COMPLETED.
// Defensif: bila status.json tak ada namun overall-status/_SUCCESS ada,
// folder dianggap selesai.
func (d *downloader) statusComplete(ctx context.Context, g outputGroup) (bool, error) {
	statusKey := g.folder + "/status.json"
	if _, ok := findObject(g.objects, statusKey); ok {
		raw, err := d.client.GetBytes(ctx, d.cfg.S3.OutputPrefix+statusKey)
		if err != nil {
			return false, err
		}
		var s struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			return false, fmt.Errorf("parse %s: %w", statusKey, err)
		}
		if strings.EqualFold(s.Status, "COMPLETED") {
			return true, nil
		}
		return false, nil
	}

	// Fallback Spark commit marker.
	successKey := g.folder + "/overall-status/_SUCCESS"
	if _, ok := findObject(g.objects, successKey); ok {
		return true, nil
	}
	return false, nil
}

// isStable menganggap selesai bila signature folder tidak berubah selama
// StabilityWindows siklus. (Berguna untuk mode loop yang berjalan terus.)
func (d *downloader) isStable(g outputGroup) bool {
	sig := folderSignature(g.objects)
	st := d.stability[g.folder]
	if st.sig == sig {
		st.windows++
	} else {
		st.sig = sig
		st.windows = 1
	}
	d.stability[g.folder] = st
	return st.windows >= d.cfg.Watcher.StabilityWindows
}

// folderSignature merangkum jumlah objek, total ukuran, dan mtime terbaru.
func folderSignature(objs []s3client.ObjectInfo) string {
	var total int64
	var maxMod int64
	for _, o := range objs {
		total += o.Size
		if m := o.LastModified.UnixNano(); m > maxMod {
			maxMod = m
		}
	}
	return fmt.Sprintf("%d:%d:%d", len(objs), total, maxMod)
}

// collectParts mengumpulkan dan mengurutkan key part di success=true.
func (d *downloader) collectParts(g outputGroup) []string {
	prefix := d.cfg.S3.OutputPrefix + g.folder + "/" + d.cfg.Watcher.OutputDirName + "/" + d.cfg.Watcher.SuccessFolder + "/"
	var parts []string
	for _, o := range g.objects {
		if strings.HasPrefix(o.Key, prefix) {
			base := o.Key[len(prefix):]
			if base == "" || strings.Contains(base, "/") {
				continue // hanya file langsung di dalam folder success
			}
			if !strings.HasPrefix(base, "part-") {
				continue
			}
			parts = append(parts, o.Key)
		}
	}
	sort.Strings(parts)
	return parts
}

// concatAndWrite mengunduh semua part, membuang header part pertama, menulis
// hasil gabungan secara atomic ke OUTCOMING/<outName>. Mengembalikan jumlah
// baris data yang ditulis.
func (d *downloader) concatAndWrite(ctx context.Context, g outputGroup, parts []string, outName string) (int64, error) {
	finalPath := filepath.Join(d.cfg.Folders.Outcoming, outName)
	tmpPath := finalPath + ".tmp"

	out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return 0, fmt.Errorf("buat file sementara %s: %w", tmpPath, err)
	}

	counter := &lineCounter{w: out}
	cleanup := func() {
		out.Close()
		_ = os.Remove(tmpPath)
	}

	for i, key := range parts {
		if err := d.copyPart(ctx, key, i == 0, counter); err != nil {
			cleanup()
			return 0, err
		}
	}

	if err := out.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return 0, fmt.Errorf("tutup file sementara: %w", err)
	}

	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return 0, fmt.Errorf("rename %s -> %s: %w", tmpPath, finalPath, err)
	}
	return counter.lines, nil
}

// copyPart menyalin satu part ke writer. Bila dropHeader true, baris pertama
// (header) dibuang.
func (d *downloader) copyPart(ctx context.Context, key string, dropHeader bool, w io.Writer) error {
	rc, err := d.client.GetStream(ctx, key)
	if err != nil {
		return err
	}
	defer rc.Close()

	if !dropHeader {
		if _, err := io.Copy(w, rc); err != nil {
			return fmt.Errorf("copy %s: %w", key, err)
		}
		return nil
	}

	br := bufio.NewReader(rc)
	if _, err := br.ReadString('\n'); err != nil && err != io.EOF {
		return fmt.Errorf("baca header %s: %w", key, err)
	}
	if _, err := io.Copy(w, br); err != nil {
		return fmt.Errorf("copy %s: %w", key, err)
	}
	return nil
}

// writeError menulis file alasan di folder ERROR.
func (d *downloader) writeError(folder string, cause error) error {
	if err := os.MkdirAll(d.cfg.Folders.Error, 0o750); err != nil {
		return err
	}
	dst := filepath.Join(d.cfg.Folders.Error, folder+".reason")
	msg := fmt.Sprintf("waktu: %s\nfolder: %s\nerror: %v\n",
		time.Now().Format(time.RFC3339), folder, cause)
	return os.WriteFile(dst, []byte(msg), 0o640)
}

// expectedSuccess menjumlahkan total_success dari overall-status/part-*.json.
// Mengembalikan (nilai, tersedia, error).
func (d *downloader) expectedSuccess(ctx context.Context, g outputGroup) (int64, bool, error) {
	prefix := d.cfg.S3.OutputPrefix + g.folder + "/overall-status/"
	var total int64
	var found bool
	for _, o := range g.objects {
		if !strings.HasPrefix(o.Key, prefix) {
			continue
		}
		base := o.Key[len(prefix):]
		if !strings.HasPrefix(base, "part-") || !strings.HasSuffix(base, ".json") {
			continue
		}
		raw, err := d.client.GetBytes(ctx, o.Key)
		if err != nil {
			return 0, false, err
		}
		var rec struct {
			TotalSuccess *int64 `json:"total_success"`
		}
		if err := json.Unmarshal(raw, &rec); err != nil {
			return 0, false, fmt.Errorf("parse %s: %w", o.Key, err)
		}
		if rec.TotalSuccess != nil {
			total += *rec.TotalSuccess
			found = true
		}
	}
	return total, found, nil
}

// findObject mencari objek berdasarkan key relatif terhadap output_prefix.
func findObject(objs []s3client.ObjectInfo, relKey string) (s3client.ObjectInfo, bool) {
	for _, o := range objs {
		if o.Key == relKey || strings.HasSuffix(o.Key, "/"+relKey) {
			return o, true
		}
	}
	return s3client.ObjectInfo{}, false
}

// lineCounter menghitung jumlah byte newline yang ditulis.
type lineCounter struct {
	w     io.Writer
	lines int64
}

func (l *lineCounter) Write(p []byte) (int, error) {
	l.lines += int64(countByte(p, '\n'))
	return l.w.Write(p)
}

func countByte(p []byte, b byte) int {
	n := 0
	for _, c := range p {
		if c == b {
			n++
		}
	}
	return n
}

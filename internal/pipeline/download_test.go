package pipeline

import (
	"testing"

	"tafw/internal/config"
	"tafw/internal/s3client"
)

func testConfig() *config.Config {
	return config.Default()
}

func TestGroupOutputs(t *testing.T) {
	const prefix = "SANDBOX/"
	objs := []s3client.ObjectInfo{
		{Key: prefix + "input_file_test.TAFCM"},                   // file input, bukan folder
		{Key: prefix + "TAFCM_2026_4_22_12h_54m_54s/status.json"}, // folder output
		{Key: prefix + "TAFCM_2026_4_22_12h_54m_54s/output/success=true/part-00000-x.c000.txt"},
		{Key: prefix + "TAFCM_2026_4_23_10h_00m_00s/status.json"}, // folder output lain
		{Key: prefix + "OTHER_FOLDER/status.json"},                // bukan prefix TAFCM_
	}

	groups := groupOutputs(objs, prefix, "TAFCM_")
	if len(groups) != 2 {
		t.Fatalf("harap 2 grup, dapat %d", len(groups))
	}
	seen := map[string]bool{}
	for _, g := range groups {
		seen[g.folder] = true
	}
	if !seen["TAFCM_2026_4_22_12h_54m_54s"] || !seen["TAFCM_2026_4_23_10h_00m_00s"] {
		t.Fatalf("grup tidak sesuai: %+v", seen)
	}
}

func TestCollectPartsOrderAndFilter(t *testing.T) {
	cfg := testConfig()
	prefix := cfg.S3.OutputPrefix // ikuti default (SANDBOX/)
	folder := "TAFCM_2026_4_22_12h_54m_54s"
	g := outputGroup{
		folder: folder,
		objects: []s3client.ObjectInfo{
			{Key: prefix + folder + "/output/success=true/part-00001-b.c000.txt"},
			{Key: prefix + folder + "/output/success=true/part-00000-a.c000.txt"},
			{Key: prefix + folder + "/output/success=false/part-00000-a.c000.txt"},
			{Key: prefix + folder + "/output/success=true/_SUCCESS"},
			{Key: prefix + folder + "/status.json"},
		},
	}
	d := &downloader{cfg: cfg}
	parts := d.collectParts(g, cfg.Watcher.SuccessFolder)
	if len(parts) != 2 {
		t.Fatalf("harap 2 part, dapat %d: %v", len(parts), parts)
	}
	want := prefix + folder + "/output/success=true/part-00000-a.c000.txt"
	if parts[0] != want {
		t.Fatalf("urutan part salah: %v (harap %s)", parts, want)
	}

	failed := d.collectParts(g, cfg.Watcher.FailedFolder)
	if len(failed) != 1 {
		t.Fatalf("harap 1 part gagal, dapat %d: %v", len(failed), failed)
	}
}

func TestFolderSignature(t *testing.T) {
	sigA := folderSignature([]s3client.ObjectInfo{{Size: 10}, {Size: 20}})
	sigB := folderSignature([]s3client.ObjectInfo{{Size: 10}, {Size: 20}})
	sigC := folderSignature([]s3client.ObjectInfo{{Size: 10}, {Size: 21}})
	if sigA != sigB {
		t.Fatalf("signature seharusnya sama: %q vs %q", sigA, sigB)
	}
	if sigA == sigC {
		t.Fatalf("signature seharusnya beda: %q vs %q", sigA, sigC)
	}
}

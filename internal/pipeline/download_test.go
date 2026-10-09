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
	objs := []s3client.ObjectInfo{
		{Key: "sandbox/input_file_test.TAFCM"},                   // file input, bukan folder
		{Key: "sandbox/TAFCM_2026_4_22_12h_54m_54s/status.json"}, // folder output
		{Key: "sandbox/TAFCM_2026_4_22_12h_54m_54s/output/success=true/part-00000-x.c000.txt"},
		{Key: "sandbox/TAFCM_2026_4_23_10h_00m_00s/status.json"}, // folder output lain
		{Key: "sandbox/OTHER_FOLDER/status.json"},                // bukan prefix TAFCM_
	}

	groups := groupOutputs(objs, "sandbox/", "TAFCM_")
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
	g := outputGroup{
		folder: "TAFCM_2026_4_22_12h_54m_54s",
		objects: []s3client.ObjectInfo{
			{Key: "sandbox/TAFCM_2026_4_22_12h_54m_54s/output/success=true/part-00001-b.c000.txt"},
			{Key: "sandbox/TAFCM_2026_4_22_12h_54m_54s/output/success=true/part-00000-a.c000.txt"},
			{Key: "sandbox/TAFCM_2026_4_22_12h_54m_54s/output/success=false/part-00000-a.c000.txt"},
			{Key: "sandbox/TAFCM_2026_4_22_12h_54m_54s/output/success=true/_SUCCESS"},
			{Key: "sandbox/TAFCM_2026_4_22_12h_54m_54s/status.json"},
		},
	}
	d := &downloader{cfg: cfg}
	parts := d.collectParts(g)
	if len(parts) != 2 {
		t.Fatalf("harap 2 part, dapat %d: %v", len(parts), parts)
	}
	if parts[0] != "sandbox/TAFCM_2026_4_22_12h_54m_54s/output/success=true/part-00000-a.c000.txt" {
		t.Fatalf("urutan part salah: %v", parts)
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

package core

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/evan-buss/openbooks/util"
)

// TestDownloadExtractDCCStringRejectsTraversal proves the DCC download path
// (core/file.go) does not let an IRC/DCC-controlled filename write outside
// baseDir. The DCC filename is untrusted remote data; before the fix a
// crafted "DCC SEND ../outside/escape.txt ..." reached os.Create unguarded
// (the extraction guard only covers entries inside an archive). The guard now
// rejects the name at path-construction time, so this test needs no network
// and is deterministic.
func TestDownloadExtractDCCStringRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	baseDir := filepath.Join(root, "base")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}

	escapeTarget := filepath.Join(outside, "escape.txt.temp")

	// 16777343 = 127.0.0.1. Filename climbs one level out of baseDir.
	dccStr := "DCC SEND ../outside/escape.txt 16777343 12345 5"

	_, err := DownloadExtractDCCString(baseDir, dccStr, nil)
	if err == nil {
		t.Fatal("traversal DCC filename was accepted (no error); a file may have been written outside baseDir")
	}

	// Nothing must have been created at the escape target.
	if _, statErr := os.Stat(escapeTarget); statErr == nil {
		t.Fatalf("file escaped baseDir: %q exists", escapeTarget)
	}
}

// TestSafeJoinNormalName checks a benign filename still resolves inside
// baseDir (the guard is not over-rejecting).
func TestSafeJoinNormalName(t *testing.T) {
	base := t.TempDir()
	got, err := util.SafeJoin(base, "a normal name.txt.zip.temp")
	if err != nil {
		t.Fatalf("benign filename rejected: %v", err)
	}
	if filepath.Base(got) != "a normal name.txt.zip.temp" {
		t.Fatalf("unexpected target %q", got)
	}

	if _, err := util.SafeJoin(base, "../outside/escape.txt"); err == nil {
		t.Fatal("traversal filename accepted")
	}
}

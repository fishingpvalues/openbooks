package util

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExtractArchiveReturnsExtractedFile is a regression test for a
// shadowing bug: the extraction walk assigned the target into a NEW local
// variable instead of the outer one, so ExtractArchive returned the
// archive itself instead of the extracted entry. The caller then parsed
// the binary archive and reported "no results" for a valid search.
func TestExtractArchiveReturnsExtractedFile(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "results.txt.zip.temp")

	const innerName = "SearchBot results.txt"
	const innerContent = "!server1 Author One - Some Title.epub ::INFO:: 10.00KB\n"
	if err := writeTestZip(t, archivePath, innerName, innerContent); err != nil {
		t.Fatalf("writeTestZip: %v", err)
	}

	extracted, err := ExtractArchive(archivePath)
	if err != nil {
		t.Fatalf("ExtractArchive: %v", err)
	}

	// The returned path must be the extracted entry, not the archive.
	base := filepath.Base(extracted)
	if strings.HasSuffix(base, ".temp") && !strings.HasSuffix(base, innerName+".temp") {
		t.Fatalf("ExtractArchive returned unexpected path %q", extracted)
	}
	if filepath.Clean(extracted) == filepath.Clean(archivePath) {
		t.Fatalf("ExtractArchive returned the archive itself: %q", extracted)
	}

	data, err := os.ReadFile(extracted)
	if err != nil {
		t.Fatalf("read extracted file %q: %v", extracted, err)
	}
	if !strings.Contains(string(data), "!server1") {
		t.Fatalf("extracted file does not contain the inner entry content, got %.40q", data)
	}
	// Archive must be removed after extraction (single-entry case).
	if _, err := os.Stat(archivePath); !os.IsNotExist(err) {
		t.Fatalf("archive %q still exists after extraction", archivePath)
	}
}

func writeTestZip(t *testing.T, path, name, content string) error {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := zip.NewWriter(f)
	h, err := w.Create(name)
	if err != nil {
		f.Close()
		return err
	}
	if _, err := h.Write([]byte(content)); err != nil {
		f.Close()
		return err
	}
	if err := w.Close(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

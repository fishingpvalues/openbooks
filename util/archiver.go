package util

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/mholt/archiver/v3"
)

var (
	ErrNotFullyCopied  = errors.New("didn't copy entire file from the archive")
	ErrBadArchiveEntry = errors.New("archive entry escapes the download directory (path traversal)")
	ErrNameEscapesDir  = errors.New("untrusted file name escapes the target directory (path traversal)")
)

// archiveRoot is the directory a downloaded .temp file sits in. Every entry
// name the archive hands us is resolved against it and rejected if the
// resolved path leaves it.
func archiveRoot(archivePath string) string {
	return filepath.Dir(archivePath)
}

// SafeJoin joins base and name and returns the joined path, or an error if
// the result lands outside base. It is the guard for untrusted file names
// (DCC download filenames, archive entry names) that must not escape a
// target directory via ../ sequences. Exported because it is used outside
// this package: core/file.go applies it to the DCC filename before writing.
func SafeJoin(base, name string) (string, error) {
	if name == "" || name == "." || name == ".." ||
		strings.ContainsRune(name, 0) {
		return "", ErrNameEscapesDir
	}

	target := filepath.Join(base, name)

	rel, err := filepath.Rel(base, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ErrNameEscapesDir
	}

	return target, nil
}

// safeArchiveTarget resolves archivePath + entryName and refuses the result
// if it lands outside the archive's own directory.
// archiver/v3 is unmaintained and the path-traversal issues it was reported
// with (GO-2024-2698, GO-2025-3605) have no fixed release, so the guard lives
// here, in the only call site that extracts downloaded archives: a crafted
// archive can no longer write through ../ sequences into the book library or
// the container.
func safeArchiveTarget(archivePath, entryName string) (string, error) {
	if entryName == "" || entryName == "." || entryName == ".." ||
		strings.ContainsRune(entryName, 0) {
		return "", ErrBadArchiveEntry
	}

	base := archiveRoot(archivePath)
	target := filepath.Join(base, entryName)

	rel, err := filepath.Rel(base, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ErrBadArchiveEntry
	}

	return target, nil
}

func ExtractArchive(archivePath string) (string, error) {
	// Our path will have a .temp appended to it so we can't rely on the automatic file-extension based archive extractor selection.
	// This code was taken from the archiver.Walk(archive string, walkFn WalkFunc) error function.
	// We just remove .temp before trying to find a matching archive extractor.
	wIface, err := archiver.ByExtension(archivePath[:len(archivePath)-len(".temp")])
	if err != nil {
		return "", err
	}
	w, ok := wIface.(archiver.Walker)
	if !ok {
		return "", fmt.Errorf("format specified by archive filename is not a walker format: %s (%T)", archivePath, wIface)
	}

	var newPath string
	err = w.Walk(archivePath, func(f archiver.File) error {
		// Upstream fix #187 (post-v4.5.0, ad12382): extract only one file
		// per archive. A multi-entry archive is not a book - delivering a
		// stray first entry (or a half-merged pile of entries) is worse
		// than delivering the archive itself. So on the second entry:
		// remove the first entry's temp file, stop the walk without
		// error, and fall through to delivering the archive unopened.
		// ErrStopWalk is the archiver/v3 contract (honoured by zip, tar
		// and rar walkers, verified in v3.5.1).
		if newPath != "" {
			if err := os.Remove(newPath); err != nil {
				return err
			}
			newPath = ""
			return archiver.ErrStopWalk
		}

		// target (not newPath) on purpose: safeArchiveTarget returns an
		// error, so a plain `newPath, err :=` here would shadow the
		// outer newPath and ExtractArchive would silently return the
		// archive itself instead of the extracted file.
		target, err := safeArchiveTarget(archivePath, f.Name()+".temp")
		if err != nil {
			return err
		}
		newPath = target

		// RAR dictionaries can claim absurd sizes (GO-2025-4020, no
		// fixed release): cap entries at 5 GiB, far beyond any real
		// ebook. Rejecting rather than truncating keeps the library
		// free of half-written files.
		const maxEntrySize = int64(5) << 30
		if f.Size() > maxEntrySize {
			return fmt.Errorf("archive entry too large: %s (%d bytes)", f.Name(), f.Size())
		}

		out, err := os.OpenFile(newPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			return err
		}

		copied, err := io.Copy(out, f)
		if err != nil {
			return err
		}
		if copied != f.Size() {
			return ErrNotFullyCopied
		}

		err = out.Close()
		if err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		return "", err
	}

	// If we extracted exactly one file, send that file and remove the zip
	// file. Otherwise (empty archive, or a multi-entry archive per #187),
	// send the archive itself.
	if newPath != "" {
		err := os.Remove(archivePath)
		if err != nil {
			log.Println("remove error", err)
		}
		return newPath, nil
	} else {
		return archivePath, nil
	}
}

// IsArchive returns true if the file at the given path is an archive that can
// be extracted. Returns false otherwise.
func IsArchive(path string) bool {
	if filepath.Ext(path) == ".temp" {
		path = path[:len(path)-len(".temp")]
	}

	_, err := archiver.ByExtension(path)
	return err == nil
}

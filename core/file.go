package core

import (
	"io"
	"os"
	"path/filepath"

	"github.com/evan-buss/openbooks/dcc"
	"github.com/evan-buss/openbooks/util"
)

func DownloadExtractDCCString(baseDir, dccStr string, progress io.Writer) (string, error) {
	// Download the file and wait until it is completed
	download, err := dcc.ParseString(dccStr)
	if err != nil {
		return "", err
	}

	// The DCC filename comes from the IRC book bot / a DCC sender - untrusted
	// remote data. A crafted "DCC SEND ../../x" would write outside baseDir
	// (and the extraction guard only covers entries *inside* an archive, so a
	// plain traversal filename reaches os.Create unguarded). Reject it before
	// any write so the library dir is the only place a download can land.
	dccPath, err := util.SafeJoin(baseDir, download.Filename+".temp")
	if err != nil {
		return "", err
	}
	file, err := os.Create(dccPath)
	if err != nil {
		return "", err
	}

	writer := io.Writer(file)
	if progress != nil {
		writer = io.MultiWriter(file, progress)
	}

	// Download DCC data to the file
	err = download.Download(writer)
	if err != nil {
		return "", err
	}
	file.Close()
	if !util.IsArchive(dccPath) {
		return renameTempFile(dccPath), nil
	}

	extractedPath, err := util.ExtractArchive(dccPath)
	if err != nil {
		return "", err
	}

	return renameTempFile(extractedPath), nil
}

func renameTempFile(filePath string) string {
	if filepath.Ext(filePath) == ".temp" {
		newPath := filePath[:len(filePath)-len(".temp")]
		os.Rename(filePath, newPath)
		return newPath
	}

	return filePath
}

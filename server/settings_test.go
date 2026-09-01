package server

import (
	"os"
	"path"
	"testing"
)

func TestRejectInvalidDir(t *testing.T) {
	cases := []struct {
		dir  string
		bad  bool
		name string
	}{
		{"/abs/path", false, "absolute"},
		{"/", true, "root"},
		{"/a\x00b", true, "nul byte"},
		{"/x/y", false, "nested absolute"},
		{"/x/y/", false, "trailing slash still absolute"},
	}
	for _, c := range cases {
		if got := rejectInvalidDir(c.dir); got != c.bad {
			t.Errorf("%s: rejectInvalidDir(%q) = %v, want %v", c.name, c.dir, got, c.bad)
		}
	}
	// Relative paths are rejected too.
	if !rejectInvalidDir("relative/path") {
		t.Errorf("rejectInvalidDir(relative/path) = false, want true")
	}
	if !rejectInvalidDir(path.Join("..", "x")) {
		t.Errorf("rejectInvalidDir(../x) = false, want true")
	}
}

func TestSettingsValidation(t *testing.T) {
	tmp := t.TempDir()

	s := &Settings{}

	// Refuse to start from nothing.
	if err := s.SetDownloadDir(""); err == nil {
		t.Errorf("SetDownloadDir(\"\") = nil, want error")
	}
	if err := s.SetDownloadDir("relative"); err == nil {
		t.Errorf("SetDownloadDir(relative) = nil, want error")
	}
	if err := s.SetDownloadDir("/"); err == nil {
		t.Errorf("SetDownloadDir(/) = nil, want error")
	}
	if err := s.SetDownloadDir("/a\x00b"); err == nil {
		t.Errorf("SetDownloadDir(nul) = nil, want error")
	}

	// A real dir works and the books/ child is created.
	want := tmp + "/books-target"
	if err := s.SetDownloadDir(want); err != nil {
		t.Fatalf("SetDownloadDir(%q) = %v", want, err)
	}
	if got := s.GetDownloadDir(); got != want {
		t.Errorf("GetDownloadDir() = %q, want %q", got, want)
	}
	if _, err := os.Stat(want + "/books"); err != nil {
		t.Errorf("books/ child not created: %v", err)
	}

	// A regular file is not a valid target.
	f, err := os.CreateTemp(tmp, "file")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := s.SetDownloadDir(f.Name()); err == nil {
		t.Errorf("SetDownloadDir(regular file) = nil, want error")
	}

	// The persist flag round-trips.
	if s.GetPersist() {
		t.Errorf("GetPersist() = true initially, want false")
	}
	s.SetPersist(true)
	if !s.GetPersist() {
		t.Errorf("SetPersist(true) not applied")
	}
}

package chat

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeFilename(t *testing.T) {
	cases := map[string]string{
		"photo.jpg":                       "photo.jpg",
		"../../etc/passwd":                "passwd",
		`C:\Users\x\notes.txt`:            "notes.txt",
		".hidden":                         "hidden",
		"a\x00b\nc.txt":                   "abc.txt",
		"":                                "file",
		"..":                              "file",
		"/":                               "file",
		"voice note (1).m4a":              "voice note (1).m4a",
		strings.Repeat("x", 300) + ".pdf": strings.Repeat("x", maxFilenameBytes-4) + ".pdf",
	}
	for in, want := range cases {
		if got := SanitizeFilename(in); got != want {
			t.Errorf("SanitizeFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFilesSaveStoresUnderAgentAndID(t *testing.T) {
	root := t.TempDir()
	fs := Files{Root: root, MaxBytes: 1 << 10}
	path, n, err := fs.Save("assistant", "01ID", "../evil.txt", strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "assistant", "01ID", "evil.txt"); path != want || n != 5 {
		t.Fatalf("path %q n %d; want %q", path, n, want)
	}
	if b, _ := os.ReadFile(path); string(b) != "hello" {
		t.Fatalf("content %q", b)
	}
}

func TestFilesSaveRefusesOverCapAndLeavesNothing(t *testing.T) {
	root := t.TempDir()
	fs := Files{Root: root, MaxBytes: 4}
	_, _, err := fs.Save("assistant", "01ID", "big.bin", bytes.NewReader([]byte("12345")))
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if _, err := os.Stat(filepath.Join(root, "assistant", "01ID")); !os.IsNotExist(err) {
		t.Fatalf("partial upload left behind: %v", err)
	}
}

func TestFilesSaveRejectsBadAgentOrID(t *testing.T) {
	fs := Files{Root: t.TempDir(), MaxBytes: 10}
	if _, _, err := fs.Save("../x", "01ID", "a", strings.NewReader("a")); err == nil {
		t.Fatal("agent name with a path must be refused")
	}
	if _, _, err := fs.Save("assistant", "../01", "a", strings.NewReader("a")); err == nil {
		t.Fatal("id with a path must be refused")
	}
}

func TestFilesCopyResolvesSymlinksAndCaps(t *testing.T) {
	src := t.TempDir()
	real := filepath.Join(src, "report.md")
	if err := os.WriteFile(real, []byte("# hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(src, "latest")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	fs := Files{Root: t.TempDir(), MaxBytes: 10}
	path, name, n, err := fs.Copy("assistant", "01ID", link)
	if err != nil {
		t.Fatal(err)
	}
	if name != "report.md" || n != 4 || filepath.Base(path) != "report.md" {
		t.Fatalf("copy = %q %q %d", path, name, n)
	}
	if _, _, _, err := fs.Copy("assistant", "01IE", src); err == nil {
		t.Fatal("a directory must be refused")
	}
	if _, _, _, err := fs.Copy("assistant", "01IF", "relative.txt"); err == nil {
		t.Fatal("a relative path must be refused")
	}
	small := Files{Root: t.TempDir(), MaxBytes: 2}
	if _, _, _, err := small.Copy("assistant", "01IG", real); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("over-cap copy err = %v", err)
	}
}

func TestDetectMIME(t *testing.T) {
	if got := DetectMIME("v.m4a", "", nil); got != "audio/mp4" {
		t.Fatalf("m4a = %q", got)
	}
	if got := DetectMIME("x", "image/png", nil); got != "image/png" {
		t.Fatalf("declared = %q", got)
	}
	if got := DetectMIME("x", "application/octet-stream", []byte("%PDF-1.4")); got != "application/pdf" {
		t.Fatalf("sniffed = %q", got)
	}
	if !IsAudio("audio/mp4") || IsAudio("video/mp4") {
		t.Fatal("IsAudio")
	}
}

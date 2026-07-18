package prompt

import (
	"io/fs"
	"os"
	"testing"
	"time"
)

type fakeFileInfo struct {
	mode fs.FileMode
}

func (f fakeFileInfo) Name() string       { return "stdin" }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() any           { return nil }

func TestIsInteractive(t *testing.T) {
	tests := []struct {
		name string
		mode fs.FileMode
		err  error
		want bool
	}{
		{"terminal (char device)", fs.ModeCharDevice | fs.ModeDevice, nil, true},
		{"pipe", fs.ModeNamedPipe, nil, false},
		{"regular file redirect", 0, nil, false},
		{"stat error treated as non-interactive", 0, os.ErrInvalid, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			orig := stdinStat
			defer func() { stdinStat = orig }()
			stdinStat = func() (fs.FileInfo, error) {
				return fakeFileInfo{mode: tt.mode}, tt.err
			}

			// Act
			got := IsInteractive()

			// Assert
			if got != tt.want {
				t.Errorf("IsInteractive() with mode %v = %v, want %v", tt.mode, got, tt.want)
			}
		})
	}
}

func TestIsInteractiveRealPipe(t *testing.T) {
	// Arrange: a real pipe wired into the seam, as when stdin is piped.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating pipe: %v", err)
	}
	defer r.Close()
	defer w.Close()

	orig := stdinStat
	defer func() { stdinStat = orig }()
	stdinStat = r.Stat

	// Act + Assert
	if IsInteractive() {
		t.Error("IsInteractive() = true for a pipe, want false")
	}
}

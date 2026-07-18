package update

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShouldVerifyAppleSignature(t *testing.T) {
	tests := []struct {
		name    string
		goos    string
		version string
		want    bool
	}{
		{"darwin at first signed release", "darwin", "v0.10.1", true},
		{"darwin at last unsigned release", "darwin", "v0.10.0", false},
		{"darwin before first signed release", "darwin", "v0.9.5", false},
		{"darwin well after first signed release", "darwin", "v1.2.3", true},
		{"linux at first signed release", "linux", "v0.10.1", false},
		{"darwin dev build", "darwin", "dev", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldVerifyAppleSignature(tt.goos, tt.version)
			if got != tt.want {
				t.Errorf("shouldVerifyAppleSignature(%q, %q) = %v, want %v", tt.goos, tt.version, got, tt.want)
			}
		})
	}
}

func TestAppleSignatureExpected(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{"v0.10.1", true},
		{"v0.10.0", false},
		{"v0.11.0", true},
		{"v0.9.5", false},
		{"v1.0.0", true},
		{"0.10.1", true},
		{"dev", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			got := appleSignatureExpected(tt.version)
			if got != tt.want {
				t.Errorf("appleSignatureExpected(%q) = %v, want %v", tt.version, got, tt.want)
			}
		})
	}
}

func TestVerifyAppleSignature(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		origRun := runCodesign
		defer func() { runCodesign = origRun }()
		runCodesign = func(requirement, path string) ([]byte, error) {
			return []byte(""), nil
		}

		if err := verifyAppleSignature("/tmp/fake-leo-binary"); err != nil {
			t.Fatalf("verifyAppleSignature() error = %v, want nil", err)
		}
	})

	t.Run("failure wraps codesign stderr", func(t *testing.T) {
		origRun := runCodesign
		defer func() { runCodesign = origRun }()
		runCodesign = func(requirement, path string) ([]byte, error) {
			return []byte("code object is not signed at all"), fmt.Errorf("exit status 1")
		}

		err := verifyAppleSignature("/tmp/fake-leo-binary")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "codesign") {
			t.Errorf("error = %q, want mention of codesign", err.Error())
		}
		if !strings.Contains(err.Error(), "code object is not signed at all") {
			t.Errorf("error = %q, want codesign stderr output included", err.Error())
		}
	})
}

// TestDownloadAndReplaceWithOptions_AppleSignatureVerification exercises the
// Apple codesign check wired into the update flow: called on darwin for a
// signed-era version, skipped on linux and for pre-signing versions, and a
// failing verification aborts before the binary swap.
func TestDownloadAndReplaceWithOptions_AppleSignatureVerification(t *testing.T) {
	tests := []struct {
		name         string
		goos         string
		version      string
		codesignErr  error
		wantCodesign bool
		wantErr      bool
		wantReplaced bool
	}{
		{
			name:         "darwin signed-era version calls codesign",
			goos:         "darwin",
			version:      "v0.10.1",
			wantCodesign: true,
			wantReplaced: true,
		},
		{
			name:         "linux skips codesign entirely",
			goos:         "linux",
			version:      "v0.10.1",
			wantCodesign: false,
			wantReplaced: true,
		},
		{
			name:         "darwin pre-signing version skips codesign",
			goos:         "darwin",
			version:      "v0.9.0",
			wantCodesign: false,
			wantReplaced: true,
		},
		{
			name:         "darwin failing codesign aborts before replace",
			goos:         "darwin",
			version:      "v0.10.1",
			codesignErr:  fmt.Errorf("exit status 1"),
			wantCodesign: true,
			wantErr:      true,
			wantReplaced: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			binaryContent := []byte("real binary content")
			archive := buildTestArchive(t, binaryContent)
			archiveName := fmt.Sprintf("leo_%s_%s_%s.tar.gz", strings.TrimPrefix(tt.version, "v"), tt.goos, "amd64")
			checksums := fmt.Sprintf("%s  %s\n", sha256Hex(archive), archiveName)

			_, teardown := testServer(t, archiveName, archive, checksums, nil, nil)
			defer teardown()

			tmpDir := t.TempDir()
			fakeBinary := filepath.Join(tmpDir, "leo")
			os.WriteFile(fakeBinary, []byte("original"), 0750)

			origExec := osExecutable
			defer func() { osExecutable = origExec }()
			osExecutable = func() (string, error) { return fakeBinary, nil }

			origGOOS := updateGOOS
			defer func() { updateGOOS = origGOOS }()
			updateGOOS = tt.goos

			origArch := updateGOARCH
			defer func() { updateGOARCH = origArch }()
			updateGOARCH = "amd64"

			origRun := runCodesign
			defer func() { runCodesign = origRun }()
			called := false
			runCodesign = func(requirement, path string) ([]byte, error) {
				called = true
				if tt.codesignErr != nil {
					return []byte("codesign failed"), tt.codesignErr
				}
				return []byte(""), nil
			}

			_, err := DownloadAndReplaceWithOptions(tt.version, unsignedOpts())

			if called != tt.wantCodesign {
				t.Errorf("codesign called = %v, want %v", called, tt.wantCodesign)
			}
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			data, _ := os.ReadFile(fakeBinary)
			replaced := string(data) == string(binaryContent)
			if replaced != tt.wantReplaced {
				t.Errorf("binary replaced = %v, want %v (content = %q)", replaced, tt.wantReplaced, string(data))
			}

			matches, _ := filepath.Glob(filepath.Join(tmpDir, "leo-update-*"))
			if len(matches) != 0 {
				t.Errorf("leftover temp files: %v", matches)
			}
		})
	}
}

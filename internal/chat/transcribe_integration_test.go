package chat

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestTranscribeRealWhisper runs the real ffmpeg + whisper-cli on a short
// spoken clip. It needs both binaries on PATH, the model named by
// LEO_WHISPER_MODEL (see docs/configuration/chat.md for fetching
// ggml-large-v3-turbo.bin), and macOS `say` to synthesise the clip; it is
// skipped when any is absent.
func TestTranscribeRealWhisper(t *testing.T) {
	model := os.Getenv("LEO_WHISPER_MODEL")
	if model == "" {
		t.Skip("LEO_WHISPER_MODEL not set")
	}
	if _, err := os.Stat(model); err != nil {
		t.Skipf("whisper model: %v", err)
	}
	for _, bin := range []string{WhisperBinary, FFmpegBinary, "say"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not on PATH", bin)
		}
	}
	clip := filepath.Join(t.TempDir(), "clip.aiff")
	if out, err := exec.Command("say", "-o", clip, "Hello from Leo chat.").CombinedOutput(); err != nil {
		t.Skipf("say failed: %v: %s", err, out)
	}
	tr := Transcriber{Run: ExecRunner, LookPath: exec.LookPath, Model: func() string { return model }}
	got, err := tr.Transcribe(context.Background(), clip)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(got), "hello") {
		t.Fatalf("transcript %q does not contain the spoken words", got)
	}
}

package chat

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Transcription binaries, looked up on PATH.
const (
	WhisperBinary = "whisper-cli"
	FFmpegBinary  = "ffmpeg"
)

// transcribeTimeout bounds one voice note's conversion plus transcription.
const transcribeTimeout = 5 * time.Minute

// Runner runs a command and returns its standard output.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// ExecRunner runs commands with os/exec.
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- fixed binaries, argv only
	cmd.WaitDelay = 5 * time.Second
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("%s: %w: %s", filepath.Base(name), err, clipRunes(strings.TrimSpace(stderr.String()), 400))
	}
	return out, nil
}

// Transcriber turns a voice note into text with local whisper.cpp: ffmpeg
// converts it to 16 kHz mono WAV, whisper-cli transcribes that. Both
// binaries and the model are optional; any one missing fails the note.
type Transcriber struct {
	Run      Runner
	LookPath func(string) (string, error)
	// Model returns the ggml model path (chat.whisper_model), read per note
	// so a config edit applies without a restart.
	Model func() string
}

// Transcribe returns the transcript of the audio file at path.
func (t Transcriber) Transcribe(ctx context.Context, path string) (string, error) {
	model := ""
	if t.Model != nil {
		model = t.Model()
	}
	if model == "" {
		return "", errors.New("chat.whisper_model is not set")
	}
	if _, err := os.Stat(model); err != nil {
		return "", fmt.Errorf("whisper model: %w", err)
	}
	ffmpeg, err := t.LookPath(FFmpegBinary)
	if err != nil {
		return "", fmt.Errorf("%s not found: %w", FFmpegBinary, err)
	}
	whisper, err := t.LookPath(WhisperBinary)
	if err != nil {
		return "", fmt.Errorf("%s not found: %w", WhisperBinary, err)
	}
	ctx, cancel := context.WithTimeout(ctx, transcribeTimeout)
	defer cancel()
	dir, err := os.MkdirTemp("", "leo-voice-")
	if err != nil {
		return "", fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(dir)
	wav := filepath.Join(dir, "note.wav")
	if _, err := t.Run(ctx, ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-i", path, "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", wav); err != nil {
		return "", fmt.Errorf("converting audio: %w", err)
	}
	out, err := t.Run(ctx, whisper, "-m", model, "-f", wav, "-l", "auto", "-nt", "-np")
	if err != nil {
		return "", fmt.Errorf("transcribing: %w", err)
	}
	text := strings.Join(strings.Fields(string(out)), " ")
	if text == "" {
		return "", errors.New("whisper returned no speech")
	}
	return text, nil
}

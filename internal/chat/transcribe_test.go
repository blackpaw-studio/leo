package chat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeRunner records commands and answers from a script keyed by binary.
type fakeRunner struct {
	calls   [][]string
	outputs map[string]string
	errs    map[string]error
}

func (f *fakeRunner) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	base := filepath.Base(name)
	return []byte(f.outputs[base]), f.errs[base]
}

func fakeLookPath(missing ...string) func(string) (string, error) {
	return func(name string) (string, error) {
		for _, m := range missing {
			if m == name {
				return "", errors.New("not found")
			}
		}
		return "/usr/bin/" + name, nil
	}
}

func modelFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ggml-large-v3-turbo.bin")
	if err := os.WriteFile(p, []byte("model"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTranscribeConvertsThenRunsWhisper(t *testing.T) {
	// Every path has a space: argv must carry each one as a single element.
	tmp := filepath.Join(t.TempDir(), "tmp dir")
	if err := os.Mkdir(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", tmp)
	model := filepath.Join(t.TempDir(), "whisper models", "ggml large.bin")
	if err := os.MkdirAll(filepath.Dir(model), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(model, []byte("model"), 0o600); err != nil {
		t.Fatal(err)
	}
	const bin = "/opt/my tools/bin/"
	lookPath := func(name string) (string, error) { return bin + name, nil }
	fr := &fakeRunner{outputs: map[string]string{"whisper-cli": "\n Remind me to call Sam.\n\n"}}
	tr := Transcriber{Run: fr.run, LookPath: lookPath, Model: func() string { return model }}
	const note = "/att/my notes/voice memo.m4a"
	got, err := tr.Transcribe(context.Background(), note)
	if err != nil {
		t.Fatal(err)
	}
	if got != "Remind me to call Sam." {
		t.Fatalf("transcript %q", got)
	}
	if len(fr.calls) != 2 {
		t.Fatalf("calls = %q", fr.calls)
	}
	wav := fr.calls[0][len(fr.calls[0])-1]
	if filepath.Base(wav) != "note.wav" || !strings.HasPrefix(wav, tmp+string(filepath.Separator)) {
		t.Fatalf("wav %q is not a note.wav under %q", wav, tmp)
	}
	wantFFmpeg := []string{bin + "ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-i", note, "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", wav}
	if !slices.Equal(fr.calls[0], wantFFmpeg) {
		t.Errorf("ffmpeg argv\n got %q\nwant %q", fr.calls[0], wantFFmpeg)
	}
	wantWhisper := []string{bin + "whisper-cli", "-m", model, "-f", wav, "-l", "auto", "-nt", "-np"}
	if !slices.Equal(fr.calls[1], wantWhisper) {
		t.Errorf("whisper argv\n got %q\nwant %q", fr.calls[1], wantWhisper)
	}
	if _, err := os.Stat(filepath.Dir(wav)); !os.IsNotExist(err) {
		t.Errorf("temporary wav directory left behind: %v", err)
	}
}

func TestTranscribeFailures(t *testing.T) {
	model := modelFile(t)
	cases := map[string]Transcriber{
		"no model configured": {Run: (&fakeRunner{}).run, LookPath: fakeLookPath(), Model: func() string { return "" }},
		"model file missing":  {Run: (&fakeRunner{}).run, LookPath: fakeLookPath(), Model: func() string { return "/nope.bin" }},
		"whisper-cli missing": {Run: (&fakeRunner{}).run, LookPath: fakeLookPath("whisper-cli"), Model: func() string { return model }},
		"ffmpeg missing":      {Run: (&fakeRunner{}).run, LookPath: fakeLookPath("ffmpeg"), Model: func() string { return model }},
		"ffmpeg fails": {Run: (&fakeRunner{errs: map[string]error{"ffmpeg": errors.New("exit 1")}}).run,
			LookPath: fakeLookPath(), Model: func() string { return model }},
		"empty transcript": {Run: (&fakeRunner{outputs: map[string]string{"whisper-cli": "  \n"}}).run,
			LookPath: fakeLookPath(), Model: func() string { return model }},
	}
	for name, tr := range cases {
		if _, err := tr.Transcribe(context.Background(), "/att/v.m4a"); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

package chat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	model := modelFile(t)
	fr := &fakeRunner{outputs: map[string]string{"whisper-cli": "\n Remind me to call Sam.\n\n"}}
	tr := Transcriber{Run: fr.run, LookPath: fakeLookPath(), Model: func() string { return model }}
	got, err := tr.Transcribe(context.Background(), "/att/voice.m4a")
	if err != nil {
		t.Fatal(err)
	}
	if got != "Remind me to call Sam." {
		t.Fatalf("transcript %q", got)
	}
	if len(fr.calls) != 2 {
		t.Fatalf("calls = %v", fr.calls)
	}
	ffmpeg, whisper := strings.Join(fr.calls[0], " "), strings.Join(fr.calls[1], " ")
	for _, want := range []string{"/usr/bin/ffmpeg", "-i /att/voice.m4a", "-ar 16000", "-ac 1"} {
		if !strings.Contains(ffmpeg, want) {
			t.Errorf("ffmpeg argv %q lacks %q", ffmpeg, want)
		}
	}
	wav := fr.calls[0][len(fr.calls[0])-1]
	for _, want := range []string{"/usr/bin/whisper-cli", "-m " + model, "-f " + wav, "-nt", "-np"} {
		if !strings.Contains(whisper, want) {
			t.Errorf("whisper argv %q lacks %q", whisper, want)
		}
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

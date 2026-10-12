package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReportChatVoice(t *testing.T) {
	model := filepath.Join(t.TempDir(), "ggml-large-v3-turbo.bin")
	if err := os.WriteFile(model, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	found := func(name string) (string, error) { return "/opt/bin/" + name, nil }
	missing := func(name string) (string, error) {
		if name == "ffmpeg" {
			return "", errors.New("not in PATH")
		}
		return "/opt/bin/" + name, nil
	}
	cases := []struct {
		name, model string
		look        func(string) (string, error)
		want        []string
	}{
		{"all present", model, found, []string{"whisper-cli: /opt/bin/whisper-cli", "ffmpeg: /opt/bin/ffmpeg", "model: " + model, "voice notes: on"}},
		{"binary missing", model, missing, []string{"ffmpeg: not found", "voice notes: off"}},
		{"model unset", "", found, []string{"chat.whisper_model is not set", "huggingface.co/ggerganov/whisper.cpp", "voice notes: off"}},
		{"model missing", "/nope/model.bin", found, []string{"/nope/model.bin", "voice notes: off"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := captureColorOutput(t, func() { reportChatVoice(tc.model, tc.look) })
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("output lacks %q:\n%s", w, out)
				}
			}
		})
	}
}

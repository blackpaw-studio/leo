package cli

import (
	"os"
	"os/exec"

	"github.com/blackpaw-studio/leo/internal/chat"
)

// whisperModelHint says where to get the recommended voice model.
const whisperModelHint = "fetch one with: curl -L -o ~/.leo/models/ggml-large-v3-turbo.bin " +
	"https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-large-v3-turbo.bin, then set chat.whisper_model"

// reportChatVoice prints whether Leo Chat can transcribe voice notes:
// whisper-cli, ffmpeg and the chat.whisper_model file are all optional,
// and a voice note is delivered with a "transcription failed" note when
// any is missing.
func reportChatVoice(model string, lookPath func(string) (string, error)) {
	info.Println("Leo Chat voice (optional):")
	ok := true
	for _, bin := range []string{chat.WhisperBinary, chat.FFmpegBinary} {
		path, err := lookPath(bin)
		if err != nil {
			warn.Printf("  %s: not found\n", bin)
			ok = false
			continue
		}
		success.Printf("  %s: %s\n", bin, path)
	}
	switch st, err := os.Stat(model); {
	case model == "":
		warn.Println("  model: chat.whisper_model is not set")
		info.Printf("    %s\n", whisperModelHint)
		ok = false
	case err != nil || st.IsDir():
		warn.Printf("  model: %s is not a readable file\n", model)
		info.Printf("    %s\n", whisperModelHint)
		ok = false
	default:
		success.Printf("  model: %s\n", model)
	}
	if ok {
		success.Println("  voice notes: on")
		return
	}
	info.Println("  voice notes: off (they arrive as \"transcription failed\")")
}

// defaultChatLookPath is reportChatVoice's binary lookup.
var defaultChatLookPath = exec.LookPath

package tmux

import "testing"

func TestOpenCodeComposerClassifier(t *testing.T) {
	tests := []struct {
		name, capture string
		want          ComposerState
	}{
		{"empty", "┃\n┃  Ask anything... \"Fix a TODO in the codebase\"\n┃\n┃  Build · model", ComposerEmpty},
		{"draft", "┃\n┃  do not overwrite this\n┃\n┃  Build · model", ComposerDraft},
		{"multiline-draft-ending-in-placeholder", "┃ human draft\n┃ Ask anything...\n┃\n┃ Build · model", ComposerDraft},
		{"long-draft-ending-in-placeholder", "┃ human draft above window\n┃\n┃\n┃\n┃\n┃ Ask anything...\n┃\n┃ Build · model", ComposerDraft},
		{"busy", "Working · esc to interrupt\n┃\n┃  Ask anything...\n┃\n┃  Build · model", ComposerBusy},
		{"history-only", "┃ old output", ComposerUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := OpenCodeComposerClassifier(tt.capture); got != tt.want {
				t.Fatalf("got %s want %s", got, tt.want)
			}
		})
	}
}

func TestComposerPasteConfirmedRecognizesOpenCodePanel(t *testing.T) {
	if !composerPasteConfirmed("┃\n┃ first line\n┃\n┃ Build · model", "first line") {
		t.Fatal("OpenCode paste not confirmed")
	}
}

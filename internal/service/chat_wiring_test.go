package service

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenChatUsesTheStateDirAndDegradesWhenItCannot(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	store, dir := openChat(home)
	if store == nil {
		t.Fatal("chat store did not open")
	}
	_ = store.Close()
	if dir != filepath.Join(home, "state", "chat", "attachments") {
		t.Fatalf("attachments dir = %s", dir)
	}
	if _, err := os.Stat(filepath.Join(home, "state", "chat.db")); err != nil {
		t.Fatalf("chat.db: %v", err)
	}

	blocked := t.TempDir()
	if err := os.WriteFile(filepath.Join(blocked, "state"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if store, _ := openChat(blocked); store != nil {
		t.Fatal("an unopenable store must leave chat off, not fail the daemon")
	}
}

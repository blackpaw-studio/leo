package web

import (
	"testing"
	"time"
)

func TestSessionStore_CreateValidateDestroy(t *testing.T) {
	store := newSessionStore(time.Hour)
	id, err := store.create()
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(id) != 64 {
		t.Fatalf("session id len = %d, want 64 (hex of 32 bytes)", len(id))
	}
	if !store.validate(id) {
		t.Fatal("validate: expected true for fresh id")
	}
	store.destroy(id)
	if store.validate(id) {
		t.Fatal("validate: expected false after destroy")
	}
}

func TestSessionStore_Expiry(t *testing.T) {
	store := newSessionStore(10 * time.Millisecond)
	id, _ := store.create()
	if !store.validate(id) {
		t.Fatal("fresh id should validate")
	}
	time.Sleep(20 * time.Millisecond)
	if store.validate(id) {
		t.Fatal("expired id should not validate")
	}
}

func TestSessionStore_UnknownID(t *testing.T) {
	store := newSessionStore(time.Hour)
	if store.validate("deadbeef") {
		t.Fatal("unknown id should not validate")
	}
	if store.validate("") {
		t.Fatal("empty id should not validate")
	}
}

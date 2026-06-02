package service

import (
	"sync"
	"testing"
)

func TestProcIdentity_RenameRewritesNameArg(t *testing.T) {
	id := newProcIdentity("leo-old", []string{"--name", "leo-old", "--model", "opus"})

	if id.Name() != "leo-old" {
		t.Fatalf("Name = %q", id.Name())
	}
	if id.SessionName() != "leo-old" {
		t.Fatalf("SessionName = %q", id.SessionName())
	}

	id.rename("leo-new")
	if id.Name() != "leo-new" {
		t.Fatalf("after rename Name = %q", id.Name())
	}
	args := id.Args()
	if args[1] != "leo-new" {
		t.Fatalf("--name not rewritten: %v", args)
	}
	// Args returns a copy: mutating it must not affect the handle.
	args[1] = "tampered"
	if id.Args()[1] != "leo-new" {
		t.Fatal("Args did not return a copy")
	}
}

func TestProcIdentity_ConcurrentAccess(t *testing.T) {
	id := newProcIdentity("leo-a", []string{"--name", "leo-a"})
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); _ = id.Name(); _ = id.Args() }()
		go func() { defer wg.Done(); id.rename("leo-b") }()
	}
	wg.Wait()
}

func TestProcIdentity_RenameWithoutNameArg(t *testing.T) {
	id := newProcIdentity("leo-x", []string{"--model", "opus"})
	id.rename("leo-y")
	if id.Name() != "leo-y" {
		t.Fatalf("Name = %q", id.Name())
	}
	if got := id.Args(); got[0] != "--model" || got[1] != "opus" {
		t.Fatalf("args unexpectedly altered: %v", got)
	}
}

func TestProcIdentity_SetArgsCopies(t *testing.T) {
	id := newProcIdentity("leo-x", []string{"--name", "leo-x"})
	src := []string{"--name", "leo-x", "--resume", "abc"}
	id.setArgs(src)
	src[3] = "tampered"
	if id.Args()[3] != "abc" {
		t.Fatal("setArgs did not store a copy")
	}
}

package service

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/observe"
)

func neverFires(time.Duration) <-chan time.Time { return nil }

func TestShutdownHoldsForTheFinalFlush(t *testing.T) {
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		awaitBounded("flush", func() { close(entered); <-release }, time.Hour, neverFires, t.Logf)
	}()

	select {
	case <-entered:
	case <-done:
		t.Fatal("shutdown returned without waiting for the flush")
	}
	select {
	case <-done:
		t.Fatal("shutdown returned before the flush finished")
	default:
	}
	close(release)
	<-done
}

func TestShutdownGivesUpAfterTheBoundAndSaysSo(t *testing.T) {
	fire := make(chan time.Time)
	var gotBound time.Duration
	timer := func(d time.Duration) <-chan time.Time { gotBound = d; return fire }
	var logged []string
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	done := make(chan struct{})
	go func() {
		defer close(done)
		awaitBounded("flush", func() { <-block }, attentionFlushBound, timer, func(f string, a ...any) { logged = append(logged, f) })
	}()

	fire <- time.Time{}
	<-done

	if gotBound != attentionFlushBound || len(logged) != 1 || !strings.Contains(logged[0], "not finished") {
		t.Fatalf("bound = %s, logged = %q; want %s and one warning", gotBound, logged, attentionFlushBound)
	}
}

func TestAwaitAttentionFlushLeavesLastTransitionOnDisk(t *testing.T) {
	path := attentionFilePath(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	store := observe.NewAttentionStore(nil)
	store.EnablePersistence(ctx, path, t.Logf)
	store.RegisterToken("tok", "a")
	store.Set("a", observe.AttentionFinished)

	cancel()
	awaitAttentionFlush(store, neverFires)

	if data, _ := os.ReadFile(path); !strings.Contains(string(data), `"finished"`) {
		t.Fatalf("last transition not on disk after shutdown wait:\n%s", data)
	}
}

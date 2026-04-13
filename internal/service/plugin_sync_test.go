package service

import (
	"context"
	"testing"
	"time"
)

// TestRunPluginSyncLoop_ExitsOnContextCancel verifies the centralized plugin
// sync loop terminates promptly when its context is cancelled, so the
// supervisor does not leak a goroutine on shutdown.
func TestRunPluginSyncLoop_ExitsOnContextCancel(t *testing.T) {
	// Use a very short interval so the ticker fires at least once.
	prev := pluginSyncInterval
	pluginSyncInterval = 10 * time.Millisecond
	t.Cleanup(func() { pluginSyncInterval = prev })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runPluginSyncLoop(ctx)
		close(done)
	}()

	// Let the loop tick a few times, then cancel.
	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runPluginSyncLoop did not exit within 1s of context cancel")
	}
}

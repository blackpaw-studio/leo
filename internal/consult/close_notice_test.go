package consult

import (
	"strings"
	"testing"
	"time"
)

// requireHeaderOnlyCloseNotice asserts calls is one close notice that names
// the run's close and points at its full output, without the last turn's
// result the caller already has.
func requireHeaderOnlyCloseNotice(t *testing.T, calls []string, id, reason, resultText string) {
	t.Helper()
	if len(calls) != 1 {
		t.Fatalf("calls=%q, want exactly one close notice", calls)
	}
	notice := calls[0]
	for _, want := range []string{"closed: " + reason, id + "#1", closeNoticeTail, "full output: leo_dispatch_output " + id} {
		if !strings.Contains(notice, want) {
			t.Fatalf("notice %q lacks %q", notice, want)
		}
	}
	if strings.Contains(notice, "\n") || strings.Contains(notice, inlineBegin) || strings.Contains(notice, resultText) || strings.Contains(notice, "collect with leo_wait") {
		t.Fatalf("notice %q repeats a result the caller already has", notice)
	}
}

func TestIdleCloseNoticeOmitsDeliveredResult(t *testing.T) {
	now := time.Date(2026, time.October, 18, 1, 0, 0, 0, time.UTC)
	d, _, delivery, id := startNotifyingClaude(t, &now, "the delivered answer")

	advance(&now, idleCloseAfter)
	d.Sweep(time.Time{})

	requireHeaderOnlyCloseNotice(t, delivery.calls, id, "idle 1h", "the delivered answer")
}

func TestIdleCloseNoticeAfterTruncatedDeliveryIsHeaderOnly(t *testing.T) {
	now := time.Date(2026, time.October, 18, 1, 0, 0, 0, time.UTC)
	long := strings.Repeat("y", maxInlineResultBytes+1)
	d, _, delivery, id := startNotifyingClaude(t, &now, long)

	advance(&now, idleCloseAfter)
	d.Sweep(time.Time{})

	requireHeaderOnlyCloseNotice(t, delivery.calls, id, "idle 1h", strings.Repeat("y", 64))
}

func TestIdleCloseNoticeCarriesUndeliveredResult(t *testing.T) {
	now := time.Date(2026, time.October, 18, 1, 0, 0, 0, time.UTC)
	d, _, delivery, id := startNotifyingClaudeUndelivered(t, &now, "the unheard answer")
	delivery.ready = false // the turn's own notice never goes out, and expires

	advance(&now, idleCloseAfter)
	d.Sweep(time.Time{})
	delivery.ready = true
	d.Sweep(time.Time{})

	if len(delivery.calls) != 1 {
		t.Fatalf("calls=%q, want only the close notice", delivery.calls)
	}
	for _, want := range []string{"closed: idle 1h", id + "#1", inlineBegin, "the unheard answer"} {
		if !strings.Contains(delivery.calls[0], want) {
			t.Fatalf("notice %q lacks %q", delivery.calls[0], want)
		}
	}
}

package harness

import "testing"

func TestQuickExitActionZeroValueIsClearSession(t *testing.T) {
	var a QuickExitAction
	if a != QuickExitClearSession {
		t.Fatalf("zero value must be QuickExitClearSession, got %d", a)
	}
}

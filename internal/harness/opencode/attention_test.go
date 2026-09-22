package opencode

import (
	"reflect"
	"testing"

	"github.com/blackpaw-studio/leo/internal/harness"
)

func TestOpencodeHasNoAttentionHooks(t *testing.T) {
	hooker, ok := (Opencode{}).Driver().(harness.AttentionHooker)
	if !ok {
		return // not implementing the capability at all is also "no support"
	}
	args := []string{"--x"}
	got, supported, err := hooker.AttentionLaunch(harness.SessionHandle{}, args, []string{"/opt/leo"})
	if err != nil || supported || !reflect.DeepEqual(got, args) {
		t.Fatalf("got %#v supported=%v err=%v", got, supported, err)
	}
}

package consult

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeAdditionalMCPServers(t *testing.T) {
	names := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = "s" + strings.Repeat("x", i)
		}
		return out
	}
	cases := []struct {
		name    string
		in      []string
		want    []string
		wantErr string
	}{
		{name: "nil", in: nil, want: nil},
		{name: "empty", in: []string{}, want: nil},
		{name: "dups removed in first-seen order", in: []string{"b", "a", "b", "a", "c"}, want: []string{"b", "a", "c"}},
		{name: "exactly 16", in: names(16), want: names(16)},
		{name: "17", in: names(17), wantErr: "17"},
		{name: "16 after dedupe is fine", in: append(names(16), names(16)...), want: names(16)},
		{name: "slash", in: []string{"a/b"}, wantErr: `"a/b"`},
		{name: "empty string", in: []string{""}, wantErr: "invalid"},
		{name: "65 chars", in: []string{strings.Repeat("a", 65)}, wantErr: "invalid"},
		{name: "64 chars", in: []string{strings.Repeat("a", 64)}, want: []string{strings.Repeat("a", 64)}},
		{name: "dot", in: []string{"."}, want: []string{"."}},
		{name: "allowed punctuation", in: []string{"my_server-1.x"}, want: []string{"my_server-1.x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeAdditionalMCPServers(tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

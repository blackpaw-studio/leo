package observe

import "testing"

func TestParseShortstat(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    GitStats
		wantErr bool
	}{
		{"empty is a clean tree", "", GitStats{}, false},
		{"whitespace only", " \n", GitStats{}, false},
		{"both", " 3 files changed, 10 insertions(+), 2 deletions(-)\n", GitStats{Added: 10, Removed: 2}, false},
		{"insertions only", " 1 file changed, 4 insertions(+)\n", GitStats{Added: 4}, false},
		{"deletions only", " 2 files changed, 7 deletions(-)\n", GitStats{Removed: 7}, false},
		{"singular forms", " 1 file changed, 1 insertion(+), 1 deletion(-)\n", GitStats{Added: 1, Removed: 1}, false},
		{"binary only", " 1 file changed, 0 insertions(+), 0 deletions(-)\n", GitStats{}, false},
		{"no trailing newline", " 1 file changed, 5 insertions(+)", GitStats{Added: 5}, false},
		{"garbage", "fatal: not a git repository", GitStats{}, true},
		{"number overflow", " 1 file changed, 99999999999999999999 insertions(+)\n", GitStats{}, true},
		{"two summary lines", " 1 file changed, 1 insertion(+)\n 1 file changed, 1 insertion(+)\n", GitStats{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseShortstat(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v; wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("stats = %+v; want %+v", got, tc.want)
			}
		})
	}
}

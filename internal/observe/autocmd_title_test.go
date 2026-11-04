package observe

import "testing"

func TestAutoCmdTitle(t *testing.T) {
	cases := []struct{ name, description, command, want string }{
		{"description wins", "Build the Mac app", "xcodebuild -scheme X", "Build the Mac app"},
		{"program without a known verb", "", "FOO=x heavy xcodebuild -scheme Y", "Shell command"},
		{"flags and secrets are dropped", "", `curl -H "Authorization: Bearer abc" https://x`, "Shell command"},
		{"a flag glued to a secret is dropped", "", "mysql -pSECRET", "Shell command"},
		{"leading cd is skipped", "", "cd dir && make test", "make test"},
		{"an unsafe first argument is dropped", "", "psql postgres://u:p@h/db", "Shell command"},
		{"an overlong first argument is dropped", "", "tool abcdefghijklmnopqrstuvwxyz0123456789", "Shell command"},
		{"path programs keep their base name", "", "/usr/local/bin/make test", "make test"},
		{"first of a pipeline", "", "cat file | grep x", "Shell command"},
		{"empty", "", "   ", "Shell command"},
		{"unsafe program", "", `"$(secret)" x`, "Shell command"},
		{"a hex token is dropped", "", "deploy 3f9a0c1b2d4e5f60718293a4b5c6d7e8", "Shell command"},
		{"a base64-ish token is dropped", "", "login Zm9vYmFyOnBhc3N3b3Jk0123", "Shell command"},
		{"an arbitrary make target is dropped", "", "make build-and-deploy-all", "make"},
		{"a known verb is kept", "", "go test ./...", "go test"},
		{"an unknown verb is dropped", "", "go Sup3rS3cret", "go"},
		{"a verb behind a flag is dropped", "", "npm -g install", "npm"},
		{"an unknown wrapper flag gives up", "", "sudo -p Sup3rS3cret make", "Shell command"},
		{"a piped secret is never titled", "", "echo Sup3rS3cret | sudo -S xcodebuild", "Shell command"},
		{"a wrapper flag gives up", "", "sudo -S xcodebuild -scheme X", "Shell command"},
		{"env assignment prefix is skipped", "", "TOKEN=abc123 go test ./...", "go test"},
		{"a quoted separator does not split", "", "export TOKEN='prefix;sk_live_1234567890abcdef;suffix'", "Shell command"},
		{"a quote before the first separator gives up", "", `make "a;b" && echo`, "Shell command"},
		{"unbalanced quote", "", `echo "oops; make test`, "Shell command"},
		{"quote before the first separator", "", `"a;b" x`, "Shell command"},
		{"sudo alone keeps the wrapped program", "", "sudo xcodebuild -scheme X", "xcodebuild"},
		{"command substitution path", "", "$(/tmp/sk_live_abc123 arg)", "Shell command"},
		{"backtick path", "", "`/tmp/sk_live_abc123 arg`", "Shell command"},
		{"here-string path", "", "<<</tmp/sk_live_abc123 cat", "Shell command"},
		{"env -S secret", "", "env -S sk_live_abc123", "Shell command"},
		{"allowlisted program via path", "", "/usr/bin/make build", "make build"},
		{"non-allowlisted program", "", "cat file", "Shell command"},
		{"whitespace description falls back", "  ", "make all", "make all"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := autoCmdTitle(tc.description, tc.command); got != tc.want {
				t.Fatalf("autoCmdTitle(%q, %q) = %q, want %q", tc.description, tc.command, got, tc.want)
			}
		})
	}
}

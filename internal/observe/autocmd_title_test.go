package observe

import "testing"

func TestAutoCmdTitle(t *testing.T) {
	cases := []struct{ name, description, command, want string }{
		{"description wins", "Build the Mac app", "xcodebuild -scheme X", "Build the Mac app"},
		{"program and first argument", "", "FOO=x heavy xcodebuild -scheme Y", "heavy xcodebuild"},
		{"flags and secrets are dropped", "", `curl -H "Authorization: Bearer abc" https://x`, "curl"},
		{"a flag glued to a secret is dropped", "", "mysql -pSECRET", "mysql"},
		{"leading cd is skipped", "", "cd dir && make test", "make test"},
		{"an unsafe first argument is dropped", "", "psql postgres://u:p@h/db", "psql"},
		{"an overlong first argument is dropped", "", "tool abcdefghijklmnopqrstuvwxyz0123456789", "tool"},
		{"path programs keep their base name", "", "/usr/local/bin/foo build", "foo build"},
		{"first of a pipeline", "", "cat file | grep x", "cat file"},
		{"empty", "", "   ", "Shell command"},
		{"unsafe program", "", `"$(secret)" x`, "Shell command"},
		{"a hex token is dropped", "", "deploy 3f9a0c1b2d4e5f60718293a4b5c6d7e8", "deploy"},
		{"a base64-ish token is dropped", "", "login Zm9vYmFyOnBhc3N3b3Jk0123", "login"},
		{"a long plain word is kept", "", "make build-and-deploy-all", "make build-and-deploy-all"},
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

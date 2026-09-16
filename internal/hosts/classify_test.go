package hosts

import "testing"

func TestClassifySSHStderr(t *testing.T) {
	cases := map[string]string{"Permission denied": "ssh_auth_required", "Host key verification failed": "ssh_host_key_unknown", "Connection refused": "ssh_unreachable", "weird": "ssh_failed"}
	for in, want := range cases {
		if got := ClassifySSHStderr(in).Code; got != want {
			t.Errorf("%q: %s", in, got)
		}
	}
}

package agentmove

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/redact"
)

const moveSentinel = "S3CR3T-SENTINEL"

// A pre_send hook that prints the agent's secret and fails leaves no trace
// of the value in the job status a poller reads.
func TestJobStatusRedactsSecretFromHookFailure(t *testing.T) {
	f := newMoveFixture(t)
	hook := filepath.Join(t.TempDir(), "export")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho \"token $OP_TOKEN\"\nexit 1\n"), 0o755); err != nil { //nolint:gosec // test hook, needs +x
		t.Fatal(err)
	}
	tmpl := f.cfg.Templates["coder"]
	tmpl.Move.Hooks.PreSend = []string{hook}
	f.cfg.Templates["coder"] = tmpl
	f.mv.Hooks = ExecHooks{}
	f.src.envs.env = map[string]string{"CLAUDE_CONFIG_DIR": f.claude, "OP_TOKEN": moveSentinel}
	f.src.envs.secrets = map[string]string{"OP_TOKEN": moveSentinel}

	jobs := NewJobs(f.mv)
	st, err := jobs.Start(context.Background(), Request{Agent: "web", Mode: ModeMove, To: "laptop"})
	if err != nil {
		t.Fatal(err)
	}
	st = waitJob(t, jobs, st.ID)
	if st.State != JobFailed || st.Error == nil || !strings.Contains(st.Error.Message, "token <secret OP_TOKEN>") {
		t.Fatalf("job = %+v, want the hook failure with its output redacted", st)
	}
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), moveSentinel) {
		t.Fatalf("job status holds the secret value:\n%s", data)
	}
}

// A destination post_receive hook that prints the agent's secret and fails
// returns, and leaves in the dormant record's reason, only the redacted text.
func TestReceiverCommitRedactsSecretFromHookFailure(t *testing.T) {
	f := newReceiverFixture(t)
	f.agents.envs.secrets = map[string]string{"OP_TOKEN": moveSentinel}
	f.hooks.err = fmt.Errorf("post_receive hook /bin/import exited with status 1: token %s", moveSentinel)
	err := f.call(t, VerbCommit, f.manifest(), nil)
	if err == nil || strings.Contains(err.Error(), moveSentinel) || !strings.Contains(err.Error(), "token <secret OP_TOKEN>") {
		t.Fatalf("commit = %v, want the redacted hook failure", err)
	}
	if len(f.agents.shadowCauses) != 1 || strings.Contains(f.agents.shadowCauses[0].Error(), moveSentinel) {
		t.Fatalf("shadow causes = %v", f.agents.shadowCauses)
	}
}

// redactError changes only an error that holds a value, and the one it
// returns still reads as its original's code and verdict.
func TestRedactErrorKeepsCodeAndVerdict(t *testing.T) {
	s := redact.NewSecrets(map[string]string{"OP_TOKEN": moveSentinel})
	clean := problemf(CodeTemplateMissing, "no template")
	if got := redactError(s, clean); got != error(clean) {
		t.Errorf("an error without the value was replaced: %v", got)
	}
	for name, err := range map[string]error{
		"problem":      fmt.Errorf("pre_send: %w", problemf(CodeTemplateMissing, "saw %s", moveSentinel)),
		"verb":         &VerbError{Code: CodeDaemonUnreachable, Message: "saw " + moveSentinel},
		"problems":     Problems{{Code: CodeTemplateMissing, Message: "saw " + moveSentinel}, {Code: CodeAgentMoving, Message: "busy"}},
		"uncoded":      fmt.Errorf("hook said %s: %w", moveSentinel, context.DeadlineExceeded),
		"wrapped verb": fmt.Errorf("commit: %w", &VerbError{Code: CodeAgentMoving, Message: moveSentinel}),
	} {
		t.Run(name, func(t *testing.T) {
			got := redactError(s, err)
			if strings.Contains(got.Error(), moveSentinel) || got.Error() != s.String(err.Error()) {
				t.Errorf("Error() = %q", got)
			}
			if CodeOf(got) != CodeOf(err) || Definite(got) != Definite(err) || HasCode(got, CodeAgentMoving) != HasCode(err, CodeAgentMoving) {
				t.Errorf("code/verdict changed: %s/%v, want %s/%v", CodeOf(got), Definite(got), CodeOf(err), Definite(err))
			}
			if errors.Is(got, context.DeadlineExceeded) != errors.Is(err, context.DeadlineExceeded) {
				t.Error("errors.Is changed")
			}
			var p *Problem
			var ve *VerbError
			var ps Problems
			for _, held := range []string{fmt.Sprint(errors.As(got, &p) && p != nil && strings.Contains(p.Message, moveSentinel)),
				fmt.Sprint(errors.As(got, &ve) && ve != nil && strings.Contains(ve.Message, moveSentinel)),
				fmt.Sprint(errors.As(got, &ps) && strings.Contains(ps.Error(), moveSentinel))} {
				if held == "true" {
					t.Fatal("errors.As reaches the unredacted text")
				}
			}
		})
	}
}

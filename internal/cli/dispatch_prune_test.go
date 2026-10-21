package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/consult"
)

func stubPrune(t *testing.T, report consult.PruneReport, err error) *struct {
	stateDir string
	opts     consult.PruneOptions
	calls    int
} {
	t.Helper()
	got := &struct {
		stateDir string
		opts     consult.PruneOptions
		calls    int
	}{}
	old := pruneWorktreesFn
	pruneWorktreesFn = func(stateDir string, opts consult.PruneOptions) (consult.PruneReport, error) {
		got.stateDir, got.opts, got.calls = stateDir, opts, got.calls+1
		report.DryRun = opts.DryRun
		return report, err
	}
	t.Cleanup(func() { pruneWorktreesFn = old })
	return got
}

var sampleReport = consult.PruneReport{
	Items: []consult.PruneItem{
		{ID: "d-aaaa", Action: "remove", Bytes: 3 << 30, Branch: "leo/a", Reason: "clean at base commit"},
		{ID: "d-bbbb", Action: "keep:dirty", Bytes: 1 << 20, Branch: "leo/b", Reason: "uncommitted changes"},
		{ID: "d-cccc", Action: "orphan", Bytes: 512, Reason: "no dispatch record; left alone"},
	},
	Totals: map[string]consult.PruneTotal{
		"remove":     {Count: 1, Bytes: 3 << 30},
		"keep:dirty": {Count: 1, Bytes: 1 << 20},
		"orphan":     {Count: 1, Bytes: 512},
	},
	Reclaimable: consult.PruneTotal{Count: 1, Bytes: 3 << 30},
}

func TestDispatchPruneDryRunPrintsEveryWorktreeAndTheTotals(t *testing.T) {
	cfg := useTempConfig(t, nil)
	got := stubPrune(t, sampleReport, nil)

	stdout, err := runRoot(t, "dispatch", "prune", "--dry-run")

	if err != nil {
		t.Fatal(err)
	}
	if got.calls != 1 || !got.opts.DryRun || got.stateDir != cfg.StatePath() {
		t.Fatalf("prune call = %+v, want one dry run over %s", got, cfg.StatePath())
	}
	for _, want := range []string{"remove", "d-aaaa", "3.0 GB", "keep:dirty", "d-bbbb", "orphan", "d-cccc", "dry run", "1 worktree", "reclaimable"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
}

func TestDispatchPruneWithoutDryRunRemoves(t *testing.T) {
	useTempConfig(t, nil)
	report := sampleReport
	report.Reclaimed = consult.PruneTotal{Count: 1, Bytes: 3 << 30}
	got := stubPrune(t, report, nil)

	stdout, err := runRoot(t, "dispatch", "prune")

	if err != nil {
		t.Fatal(err)
	}
	if got.opts.DryRun {
		t.Fatal("DryRun was set without --dry-run")
	}
	if !strings.Contains(stdout, "reclaimed") || strings.Contains(stdout, "dry run") {
		t.Errorf("output = %s", stdout)
	}
}

func TestDispatchPruneJSONIsOneObject(t *testing.T) {
	useTempConfig(t, nil)
	stubPrune(t, sampleReport, nil)

	stdout, err := runRoot(t, "dispatch", "prune", "--dry-run", "--json")

	if err != nil {
		t.Fatal(err)
	}
	var got consult.PruneReport
	dec := json.NewDecoder(strings.NewReader(stdout))
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("stdout is not JSON: %q", stdout)
	}
	if dec.More() {
		t.Fatalf("stdout carries more than one JSON value: %q", stdout)
	}
	if !got.DryRun || got.Reclaimable.Count != 1 || len(got.Items) != 3 {
		t.Fatalf("report = %+v", got)
	}
}

func TestDispatchPruneJSONFailureIsOneErrorObjectAndNonZero(t *testing.T) {
	useTempConfig(t, nil)
	stubPrune(t, consult.PruneReport{}, errors.New("disk on fire"))

	stdout, err := runRoot(t, "dispatch", "prune", "--json")

	if ExitCode(err) != 1 {
		t.Fatalf("exit = %d, err = %v", ExitCode(err), err)
	}
	var got struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &got); jerr != nil {
		t.Fatalf("stdout is not one JSON object: %q", stdout)
	}
	if got.Error == "" || !strings.Contains(got.Message, "disk on fire") {
		t.Fatalf("failure = %+v", got)
	}
}

func TestDispatchPruneUnknownFlagWithJSONIsAnInvalidArgsObject(t *testing.T) {
	useTempConfig(t, nil)
	stubPrune(t, consult.PruneReport{}, nil)

	stdout, err := runRoot(t, "dispatch", "prune", "--json", "--bogus")

	if ExitCode(err) != 1 {
		t.Fatalf("exit = %d", ExitCode(err))
	}
	var got struct {
		Error string `json:"error"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &got); jerr != nil || got.Error != "invalid_args" {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestFormatBytes(t *testing.T) {
	for in, want := range map[int64]string{0: "0 B", 512: "512 B", 1536: "1.5 KB", 3 << 20: "3.0 MB", 393 << 30: "393.0 GB"} {
		if got := formatBytes(in); got != want {
			t.Errorf("formatBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

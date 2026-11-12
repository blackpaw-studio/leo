package observe

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestFeaturesIncludeAgentProgress(t *testing.T) {
	if !slices.Contains(Features(), FeatureAgentProgress) || FeatureAgentProgress != "agent_progress" {
		t.Fatalf("Features() = %v, want agent_progress", Features())
	}
	if EventAgentTaskChanged != "agent_task_changed" {
		t.Fatalf("event type = %q", EventAgentTaskChanged)
	}
}

func TestAgentTaskChangedWire(t *testing.T) {
	at := time.Date(2026, 11, 3, 19, 30, 0, 0, time.UTC)
	started := at.Add(-4 * time.Minute)
	eta := at.Add(10 * time.Minute)
	ended := at.Add(time.Minute)

	assertWire(t, "task-event-determinate.json", &AgentTaskChangedPayload{
		Meta: Meta{Seq: 812, At: at}, Agent: "alfred", ID: "build",
		Task: &AgentTask{
			ID: "build", Source: SourceAgent, Title: "Xcode build", Mode: TaskDeterminate, Percent: ptr(62.0),
			Status: "Compiling 412/980", EtaAt: &eta, State: TaskRunning,
			StartedAt: started, UpdatedAt: at, Revision: 7,
		},
	})
	assertWire(t, "task-event-indeterminate-stale.json", &AgentTaskChangedPayload{
		Meta: Meta{Seq: 813, At: at}, Agent: "alfred", ID: "deploy",
		Task: &AgentTask{
			ID: "deploy", Source: SourceAgent, Title: "Deploy", Mode: TaskIndeterminate, State: TaskRunning, Stale: true,
			StartedAt: started, UpdatedAt: started, Revision: 2,
		},
	})
	assertWire(t, "task-event-failed.json", &AgentTaskChangedPayload{
		Meta: Meta{Seq: 814, At: at}, Agent: "alfred", ID: "tests",
		Task: &AgentTask{
			ID: "tests", Source: SourceAgent, Title: "Test sweep", Mode: TaskDeterminate, Percent: ptr(80.0),
			Status: "3 failures", State: TaskFailed,
			StartedAt: started, UpdatedAt: ended, EndedAt: &ended, Revision: 9,
		},
	})
	assertWire(t, "task-event-auto.json", &AgentTaskChangedPayload{
		Meta: Meta{Seq: 816, At: at}, Agent: "alfred", ID: "auto.cmd.3f9a0c1b2d4e",
		Task: &AgentTask{
			ID: "auto.cmd.3f9a0c1b2d4e", Source: SourceAuto, Title: "Build the Mac app", Mode: TaskIndeterminate,
			State: TaskRunning, StartedAt: started, UpdatedAt: at, Revision: 1,
		},
	})
	assertWire(t, "task-event-null.json", &AgentTaskChangedPayload{
		Meta: Meta{Seq: 815, At: at}, Agent: "alfred", ID: "tests",
	})
}

func TestAgentTasksOmittedWhenEmpty(t *testing.T) {
	b, err := json.Marshal(Agent{Name: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "tasks") {
		t.Fatalf("empty tasks on the wire: %s", b)
	}
	b, err = json.Marshal(Agent{Name: "a", Tasks: []AgentTask{{ID: "t", Title: "T", Mode: TaskIndeterminate, State: TaskRunning}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"tasks":[{"id":"t"`) {
		t.Fatalf("tasks missing: %s", b)
	}
}

func TestFeaturesAdvertiseAgentShell(t *testing.T) {
	if !slices.Contains(Features(), FeatureAgentShell) || FeatureAgentShell != "agent_shell" {
		t.Fatalf("Features() = %v, want agent_shell", Features())
	}
}

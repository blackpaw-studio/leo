package service

import (
	"sort"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/observe"
)

// pendingRestores names the stored agents that report "restoring" until the
// daemon is ready: every record not marked Stopped (see
// agent.Manager.notLiveStatus). Capture it before RestoreAgents runs.
func pendingRestores(homePath string) []string {
	records, err := agentstore.Load(agentstore.FilePath(homePath))
	if err != nil {
		return nil
	}
	var names []string
	for name, rec := range records {
		if !rec.Stopped {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// publishRestoreOutcomes resolves every agent that was reported "restoring"
// for event followers. A restore that fails (markFailedRestore, a worktree
// spawn failure) or drops a record publishes nothing itself, so without this
// a client that saw "restoring" in the snapshot would never see it end.
// Call it after RestoreAgents and before the daemon is marked ready.
//
// An agent still in the store gets an agent_state_changed with its current
// status: the supervisor's when it came up (live), else stopped. One whose
// record is gone gets an agent_stopped.
func publishRestoreOutcomes(homePath string, pending []string, live map[string]daemon.ProcessStateInfo, pub observe.Publisher) {
	if len(pending) == 0 || pub == nil {
		return
	}
	records, err := agentstore.Load(agentstore.FilePath(homePath))
	if err != nil {
		records = nil
	}
	for _, name := range pending {
		rec, stored := records[name]
		st, isLive := live[name]
		switch {
		case !stored && !isLive:
			pub.Publish(observe.Event{
				Type:    observe.EventAgentStopped,
				Payload: &observe.AgentStoppedPayload{Agent: name},
			})
		default:
			raw := "stopped"
			restarts := 0
			if isLive {
				raw, restarts = st.Status, st.Restarts
			}
			status, wake := observe.AgentDormancy(raw, stored && rec.WakeOnMessage)
			pub.Publish(observe.Event{
				Type: observe.EventAgentStateChanged,
				Payload: &observe.AgentStateChangedPayload{
					Agent:         name,
					Status:        status,
					Restarts:      restarts,
					WakeOnMessage: wake,
				},
			})
		}
	}
}

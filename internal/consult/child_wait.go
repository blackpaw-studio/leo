package consult

// childDispatchTask is the PendingWork task kind that counts a dispatch's
// running child dispatches.
const childDispatchTask = "dispatch"

// WithChildWait returns records with each idle or settling dispatch that has
// live child dispatches shown as waiting on them, their count in its
// PendingWork. It is display-only: the dispatcher's own status stays idle,
// so leo_send_dispatch and notification collection are unaffected, and
// records is not modified. A child counts when it is not finished and not
// itself idle without live children of its own.
func WithChildWait(records []Record) []Record {
	children := make(map[string][]int, len(records))
	for i, rec := range records {
		if rec.Kind != "dispatch" {
			continue
		}
		if parent := parentDispatchID(rec); parent != "" {
			children[parent] = append(children[parent], i)
		}
	}
	if len(children) == 0 {
		return records
	}
	live := make(map[int]int, len(records))
	visiting := map[int]bool{}
	var liveChildren func(i int) int
	liveChildren = func(i int) int {
		if n, ok := live[i]; ok {
			return n
		}
		if visiting[i] {
			return 0
		}
		visiting[i] = true
		n := 0
		for _, c := range children[records[i].ID] {
			if isLiveChild(records[c], liveChildren(c)) {
				n++
			}
		}
		visiting[i] = false
		live[i] = n
		return n
	}
	out := make([]Record, len(records))
	copy(out, records)
	for i, rec := range records {
		if rec.Kind != "dispatch" || !isIdleStatus(rec.Status) {
			continue
		}
		if n := liveChildren(i); n > 0 {
			out[i] = waitingOnChildren(rec, n)
		}
	}
	return out
}

func isIdleStatus(s Status) bool { return s == StatusIdle || s == StatusSettling }

// isLiveChild reports whether a child dispatch still holds its parent's
// attention: unfinished, and either working or itself waiting on children.
func isLiveChild(rec Record, liveGrandchildren int) bool {
	if rec.Status.Terminal() || rec.Kind != "dispatch" {
		return false
	}
	return !isIdleStatus(rec.Status) || liveGrandchildren > 0
}

func waitingOnChildren(rec Record, n int) Record {
	pending := clonePendingWork(rec.PendingWork)
	if pending == nil {
		pending = &PendingWork{}
	}
	if pending.Tasks == nil {
		pending.Tasks = map[string]int{}
	}
	pending.Tasks[childDispatchTask] += n
	rec.PendingWork = pending
	rec.Status = StatusWaiting
	return rec
}

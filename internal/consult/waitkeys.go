package consult

import (
	"sort"
	"strings"
	"sync"
)

type serialLock struct {
	mu   sync.Mutex
	refs int
}

func transitionKey(runID string, mode Mode, turnID string) string {
	if turnID != "" {
		return turnID
	}
	return strings.SplitN(runID, "#", 2)[0]
}

func (d *Dispatcher) waitCovers(key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.waits[key] > 0
}

func (d *Dispatcher) registerWaitsLocked(keys []string) func() {
	for _, key := range keys {
		if key != "" {
			d.waits[key]++
			for _, state := range d.runs {
				n, ok := state.record.Notifications[key]
				if !ok || n.Disposition != NotificationPending {
					continue
				}
				n.Disposition, n.SuppressedAt = NotificationSuppressed, d.now()
				state.record.Notifications[key] = n
				_ = d.persistNotificationRecordLocked(state)
			}
		}
	}
	return func() {
		d.mu.Lock()
		for _, key := range keys {
			if d.waits[key] <= 1 {
				delete(d.waits, key)
			} else {
				d.waits[key]--
			}
		}
		d.mu.Unlock()
	}
}

func (d *Dispatcher) waitCount(key string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.waits[key]
}

func (d *Dispatcher) serialLocks(ids []string) func() {
	unique := map[string]bool{}
	for _, id := range ids {
		unique[strings.SplitN(id, "#", 2)[0]] = true
	}
	keys := make([]string, 0, len(unique))
	for id := range unique {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	d.mu.Lock()
	locks := make([]*serialLock, 0, len(keys))
	for _, id := range keys {
		lock := d.serial[id]
		if lock == nil {
			lock = &serialLock{}
			d.serial[id] = lock
		}
		lock.refs++
		locks = append(locks, lock)
	}
	d.mu.Unlock()
	for _, lock := range locks {
		lock.mu.Lock()
	}
	return func() {
		for i := len(locks) - 1; i >= 0; i-- {
			locks[i].mu.Unlock()
		}
		d.mu.Lock()
		for i, id := range keys {
			lock := locks[i]
			lock.refs--
			if lock.refs == 0 && d.serial[id] == lock {
				delete(d.serial, id)
			}
		}
		d.mu.Unlock()
	}
}

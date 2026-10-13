package chat

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const scheduleColumns = `id, agent, kind, task, run_id, text, fire_at, created_at`

// pendingWhere selects reminders that have neither fired nor been cancelled.
const pendingWhere = `fired_at IS NULL AND cancelled_at IS NULL`

// InsertSchedule stores a pending reminder.
func (s *Store) InsertSchedule(r Schedule) error {
	_, err := s.db.Exec(`INSERT INTO schedules (`+scheduleColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Agent, r.Kind, r.Task, r.RunID, r.Text, r.FireAt.UnixMilli(), r.Created.UnixMilli())
	if err != nil {
		return fmt.Errorf("inserting reminder: %w", err)
	}
	return nil
}

func scanSchedule(row interface{ Scan(...any) error }) (Schedule, error) {
	var r Schedule
	var fire, created int64
	if err := row.Scan(&r.ID, &r.Agent, &r.Kind, &r.Task, &r.RunID, &r.Text, &fire, &created); err != nil {
		return Schedule{}, err
	}
	r.FireAt, r.Created = time.UnixMilli(fire), time.UnixMilli(created)
	return r, nil
}

func (s *Store) querySchedules(query string, args ...any) ([]Schedule, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("reading reminders: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Schedule
	for rows.Next() {
		r, err := scanSchedule(rows)
		if err != nil {
			return nil, fmt.Errorf("reading reminders: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PendingSchedules lists agent's pending reminders, soonest first.
func (s *Store) PendingSchedules(agent string) ([]Schedule, error) {
	return s.querySchedules(`SELECT `+scheduleColumns+` FROM schedules WHERE agent = ? AND `+pendingWhere+` ORDER BY fire_at, id`, agent)
}

// DuePending lists every pending reminder due at or before now, soonest first.
func (s *Store) DuePending(now time.Time) ([]Schedule, error) {
	return s.querySchedules(`SELECT `+scheduleColumns+` FROM schedules WHERE `+pendingWhere+` AND fire_at <= ? ORDER BY fire_at, id`, now.UnixMilli())
}

// NextPending returns the earliest pending reminder, if any.
func (s *Store) NextPending() (Schedule, bool, error) {
	r, err := scanSchedule(s.db.QueryRow(`SELECT ` + scheduleColumns + ` FROM schedules WHERE ` + pendingWhere + ` ORDER BY fire_at, id LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return Schedule{}, false, nil
	}
	if err != nil {
		return Schedule{}, false, fmt.Errorf("reading next reminder: %w", err)
	}
	return r, true, nil
}

// CancelSchedule cancels agent's pending reminder id. ErrNotFound: no such
// reminder of agent's. ErrConflict: it has already fired or been cancelled.
func (s *Store) CancelSchedule(id, agent string, at time.Time) error {
	res, err := s.db.Exec(`UPDATE schedules SET cancelled_at = ? WHERE id = ? AND agent = ? AND `+pendingWhere, at.UnixMilli(), id, agent)
	if err != nil {
		return fmt.Errorf("cancelling reminder: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	var one int
	err = s.db.QueryRow(`SELECT 1 FROM schedules WHERE id = ? AND agent = ?`, id, agent).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("cancelling reminder: %w", err)
	}
	return ErrConflict
}

// FireSchedule marks reminder id fired and stores its message m in one
// transaction. It reports false, storing nothing, when the reminder is no
// longer pending (already fired, or cancelled).
func (s *Store) FireSchedule(id string, m Message, firedAt time.Time) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, fmt.Errorf("firing reminder: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.Exec(`UPDATE schedules SET fired_at = ? WHERE id = ? AND `+pendingWhere, firedAt.UnixMilli(), id)
	if err != nil {
		return false, fmt.Errorf("firing reminder: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	if err := insertMessageTx(tx, m, nil, nil); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("firing reminder: %w", err)
	}
	return true, nil
}

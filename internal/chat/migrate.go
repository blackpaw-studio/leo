package chat

import (
	"database/sql"
	"fmt"
)

// migration is one ordered, transactional step of the chat.db schema;
// version is the PRAGMA user_version the database holds once it has run.
type migration struct {
	version int
	sql     string
}

// migrations is the whole history of chat.db, applied in order to a
// database below the step's version. Steps are never edited once released:
// a schema change is a new step, and schemaVersion is the last one's.
//
// Step 1 is the v0.49.0 schema; its IF NOT EXISTS lets it adopt a database
// an earlier build created without a version.
var migrations = []migration{
	{version: 1, sql: `
CREATE TABLE IF NOT EXISTS messages (
	id         TEXT PRIMARY KEY,
	agent      TEXT NOT NULL,
	direction  TEXT NOT NULL,
	kind       TEXT NOT NULL,
	text       TEXT NOT NULL DEFAULT '',
	transcript TEXT NOT NULL DEFAULT '',
	reply_to   TEXT NOT NULL DEFAULT '',
	task       TEXT NOT NULL DEFAULT '',
	run_id     TEXT NOT NULL DEFAULT '',
	state      TEXT NOT NULL DEFAULT '',
	answer     TEXT NOT NULL DEFAULT '',
	options    TEXT NOT NULL DEFAULT '',
	meta       TEXT NOT NULL DEFAULT '',
	delivery_ref TEXT NOT NULL DEFAULT '',
	ts         INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS messages_agent_id ON messages(agent, id);
CREATE INDEX IF NOT EXISTS messages_agent_task_id ON messages(agent, task, id);
CREATE TABLE IF NOT EXISTS attachments (
	id         TEXT PRIMARY KEY,
	message_id TEXT NOT NULL DEFAULT '',
	agent      TEXT NOT NULL,
	filename   TEXT NOT NULL,
	mime       TEXT NOT NULL,
	bytes      INTEGER NOT NULL,
	path       TEXT NOT NULL DEFAULT '',
	created    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS attachments_message ON attachments(message_id);
CREATE TABLE IF NOT EXISTS reads (
	agent     TEXT PRIMARY KEY,
	last_read TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS captures (
	message_id   TEXT PRIMARY KEY,
	agent        TEXT NOT NULL,
	prompt       TEXT NOT NULL,
	delivered_at INTEGER NOT NULL
);`},
	// Typed ask inputs: the options column becomes an input spec (every
	// earlier ask or permission was a set of buttons), and the answer a
	// {values, text} object rather than the chosen value.
	{version: 2, sql: `
ALTER TABLE messages ADD COLUMN input TEXT NOT NULL DEFAULT '';
UPDATE messages SET input = json_object('type', 'buttons', 'options', json(options)) WHERE options != '';
UPDATE messages SET answer = json_object('values', json_array(answer), 'text', '') WHERE answer != '';
ALTER TABLE messages DROP COLUMN options;`},
	// Reminders (leo_chat_schedule): a row per scheduled post, pending until
	// it fires or is cancelled.
	{version: 3, sql: `
CREATE TABLE schedules (
	id           TEXT PRIMARY KEY,
	agent        TEXT NOT NULL,
	kind         TEXT NOT NULL,
	task         TEXT NOT NULL DEFAULT '',
	run_id       TEXT NOT NULL DEFAULT '',
	text         TEXT NOT NULL,
	fire_at      INTEGER NOT NULL,
	created_at   INTEGER NOT NULL,
	fired_at     INTEGER,
	cancelled_at INTEGER
);
CREATE INDEX schedules_pending ON schedules(fire_at) WHERE fired_at IS NULL AND cancelled_at IS NULL;`},
}

// schemaVersion is the user_version of a database with every migration
// applied.
var schemaVersion = migrations[len(migrations)-1].version

// migrate brings db up to the last of steps, each step in its own
// transaction together with its version stamp, so a failed step leaves the
// database at the previous version. A database newer than steps is refused
// rather than downgraded.
func migrate(db *sql.DB, steps []migration) error {
	var current int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&current); err != nil {
		return fmt.Errorf("reading schema version: %w", err)
	}
	if latest := steps[len(steps)-1].version; current > latest {
		return fmt.Errorf("chat database is schema version %d, newer than this leo supports (%d); upgrade leo", current, latest)
	}
	for _, step := range steps {
		if step.version <= current {
			continue
		}
		if err := applyMigration(db, step); err != nil {
			return fmt.Errorf("migrating chat database to version %d: %w", step.version, err)
		}
	}
	return nil
}

func applyMigration(db *sql.DB, step migration) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(step.sql); err != nil {
		return err
	}
	// user_version takes no bound parameters; the version is a constant.
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", step.version)); err != nil {
		return err
	}
	return tx.Commit()
}

package chat

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// v0490Schema is chat.db as v0.49.0 created it (user_version 1).
const v0490Schema = `
CREATE TABLE messages (
	id TEXT PRIMARY KEY, agent TEXT NOT NULL, direction TEXT NOT NULL, kind TEXT NOT NULL,
	text TEXT NOT NULL DEFAULT '', transcript TEXT NOT NULL DEFAULT '', reply_to TEXT NOT NULL DEFAULT '',
	task TEXT NOT NULL DEFAULT '', run_id TEXT NOT NULL DEFAULT '', state TEXT NOT NULL DEFAULT '',
	answer TEXT NOT NULL DEFAULT '', options TEXT NOT NULL DEFAULT '', meta TEXT NOT NULL DEFAULT '',
	delivery_ref TEXT NOT NULL DEFAULT '', ts INTEGER NOT NULL
);
CREATE INDEX messages_agent_id ON messages(agent, id);
CREATE INDEX messages_agent_task_id ON messages(agent, task, id);
CREATE TABLE attachments (id TEXT PRIMARY KEY, message_id TEXT NOT NULL DEFAULT '', agent TEXT NOT NULL, filename TEXT NOT NULL, mime TEXT NOT NULL, bytes INTEGER NOT NULL, path TEXT NOT NULL DEFAULT '', created INTEGER NOT NULL);
CREATE INDEX attachments_message ON attachments(message_id);
CREATE TABLE reads (agent TEXT PRIMARY KEY, last_read TEXT NOT NULL);
CREATE TABLE captures (message_id TEXT PRIMARY KEY, agent TEXT NOT NULL, prompt TEXT NOT NULL, delivered_at INTEGER NOT NULL);
PRAGMA user_version = 1;
INSERT INTO messages (id, agent, direction, kind, text, state, answer, options, ts) VALUES
 ('01A', 'assistant', 'out', 'ask', 'Ship?', 'answered', 'y', '[{"value":"y","label":"Yes"},{"value":"n","label":"No"}]', 1000),
 ('01B', 'assistant', 'out', 'ask', 'Open one', 'open', '', '[{"value":"x","label":"X"}]', 2000),
 ('01C', 'assistant', 'out', 'permission', 'Bash: ls', 'answered', 'terminal', '[{"value":"allow","label":"Allow"},{"value":"deny","label":"Deny"}]', 3000),
 ('01D', 'assistant', 'out', 'text', 'hello', '', '', '', 4000);
`

func TestOpenStoreUpgradesAV0490Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(v0490Schema); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()

	st, err := OpenStore(path)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	defer func() { _ = st.Close() }()

	a, err := st.Message("01A")
	if err != nil {
		t.Fatal(err)
	}
	if a.Input == nil || a.Input.Type != InputButtons || len(a.Input.Options) != 2 || a.Input.Options[0].Label != "Yes" {
		t.Fatalf("migrated ask input = %+v", a.Input)
	}
	if a.Answer == nil || len(a.Answer.Values) != 1 || a.Answer.Values[0] != "y" || a.Answer.Text != "" {
		t.Fatalf("migrated answer = %+v", a.Answer)
	}
	b, _ := st.Message("01B")
	// The v4 step expires an ask left open by a crash.
	if b.Answer != nil || b.State != StateExpired || b.Input == nil {
		t.Fatalf("open ask = %+v", b)
	}
	c, _ := st.Message("01C")
	if c.Answer == nil || len(c.Answer.Values) != 1 || c.Answer.Values[0] != AnswerTerminal || c.Input == nil {
		t.Fatalf("permission = %+v", c)
	}
	d, _ := st.Message("01D")
	if d.Input != nil || d.Answer != nil || d.Text != "hello" {
		t.Fatalf("plain message changed: %+v", d)
	}
	if v := userVersion(t, st); v != schemaVersion {
		t.Fatalf("user_version = %d, want %d", v, schemaVersion)
	}
	// Reopening is a no-op.
	_ = st.Close()
	st2, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	_ = st2.Close()
}

func TestOpenStoreFreshDatabaseIsCurrent(t *testing.T) {
	st := openTestStore(t)
	if v := userVersion(t, st); v != schemaVersion {
		t.Fatalf("user_version = %d, want %d", v, schemaVersion)
	}
}

func TestOpenStoreRefusesANewerDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat.db")
	raw, _ := sql.Open("sqlite", "file:"+path)
	if _, err := raw.Exec(`CREATE TABLE messages (id TEXT); PRAGMA user_version = 99;`); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()
	if _, err := OpenStore(path); err == nil {
		t.Fatal("opening a database from a newer leo must fail")
	}
}

func TestFailedMigrationStepRollsBack(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	steps := []migration{
		{version: 1, sql: `CREATE TABLE t (x INTEGER);`},
		{version: 2, sql: `INSERT INTO t VALUES (1); INSERT INTO nope VALUES (1);`},
	}
	if err := migrate(db, steps); err == nil {
		t.Fatal("want error")
	}
	var v, n int
	_ = db.QueryRow(`PRAGMA user_version`).Scan(&v)
	_ = db.QueryRow(`SELECT count(*) FROM t`).Scan(&n)
	if v != 1 || n != 0 {
		t.Fatalf("user_version = %d, rows = %d; want 1, 0", v, n)
	}
}

func userVersion(t *testing.T, st *Store) int {
	t.Helper()
	var v int
	if err := st.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestOpenStoreUpgradesAV2DatabaseAddsSchedules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	if err := migrate(raw, migrations[:2]); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()

	st, err := OpenStore(path)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	defer func() { _ = st.Close() }()
	if v := userVersion(t, st); v != schemaVersion {
		t.Fatalf("user_version = %d, want %d", v, schemaVersion)
	}
	at := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	if err := st.InsertSchedule(Schedule{ID: "S1", Agent: "assistant", Kind: KindText, Text: "hi", FireAt: at, Created: at}); err != nil {
		t.Fatalf("schedules table unusable: %v", err)
	}
}

func TestOpenStoreUpgradesAV3DatabaseAddsAskColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	if err := migrate(raw, migrations[:3]); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO messages (id, agent, direction, kind, text, state, ts) VALUES
		('01A', 'assistant', 'out', 'ask', 'Open one', 'open', 1000),
		('01B', 'assistant', 'out', 'ask', 'Done one', 'answered', 2000),
		('01C', 'assistant', 'out', 'permission', 'Bash: ls', 'open', 3000)`); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()

	st, err := OpenStore(path)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	defer func() { _ = st.Close() }()
	if v := userVersion(t, st); v != 4 {
		t.Fatalf("user_version = %d, want 4", v)
	}
	open, _ := st.Message("01A")
	if open.State != StateExpired || open.ClosedReason != ClosedTimeout {
		t.Fatalf("pre-v4 open ask = %s/%q, want expired/timeout", open.State, open.ClosedReason)
	}
	done, _ := st.Message("01B")
	if done.State != StateAnswered || done.ClosedReason != "" {
		t.Fatalf("answered ask changed: %s/%q", done.State, done.ClosedReason)
	}
	perm, _ := st.Message("01C")
	if perm.State != StateOpen {
		t.Fatalf("open permission prompt changed to %s", perm.State)
	}
}

package chat

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver; goreleaser builds without CGO
)

// Paging bounds for Messages.
const (
	DefaultPageLimit = 50
	MaxPageLimit     = 200
)

// AttachmentRetention is how long attachment files are kept; their rows
// outlive them with the path cleared.
const AttachmentRetention = 90 * 24 * time.Hour

// ErrNotFound reports an unknown message or attachment.
var ErrNotFound = errors.New("not found")

// schemaVersion is stored in PRAGMA user_version.
const schemaVersion = 1

const schema = `
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
	delivered_at INTEGER NOT NULL,
	tool_posted  INTEGER NOT NULL DEFAULT 0
);
`

// Store is the chat database. It is safe for concurrent use.
type Store struct {
	db *sql.DB
}

// OpenStore opens (creating if needed) the database at path, in WAL mode,
// with the file and its directory private to the owner.
func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("creating chat store directory: %w", err)
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening chat store: %w", err)
	}
	// One connection serializes writers, so SQLITE_BUSY never surfaces;
	// the chat load is a handful of rows per minute.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("creating chat schema: %w", err)
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("stamping chat schema: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("restricting chat store permissions: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// storedMeta is the meta column: a permission message's tool call.
type storedMeta struct {
	Tool    string          `json:"tool,omitempty"`
	Summary string          `json:"summary,omitempty"`
	Input   json.RawMessage `json:"input,omitempty"`
}

// InsertMessage stores m.
func (s *Store) InsertMessage(m Message) error {
	options, meta, err := encodeExtras(m)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO messages (id, agent, direction, kind, text, transcript, reply_to, task, run_id, state, answer, options, meta, ts)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.Agent, m.Direction, m.Kind, m.Text, m.Transcript, m.ReplyTo, m.Task, m.RunID, m.State, m.Answer, options, meta, m.TS.UnixMilli())
	if err != nil {
		return fmt.Errorf("inserting chat message: %w", err)
	}
	return nil
}

func encodeExtras(m Message) (string, string, error) {
	var options, meta string
	if len(m.Options) > 0 {
		b, err := json.Marshal(m.Options)
		if err != nil {
			return "", "", fmt.Errorf("encoding options: %w", err)
		}
		options = string(b)
	}
	if m.Permission != nil {
		b, err := json.Marshal(storedMeta{Tool: m.Permission.Tool, Summary: m.Permission.Summary, Input: m.Permission.input})
		if err != nil {
			return "", "", fmt.Errorf("encoding permission: %w", err)
		}
		meta = string(b)
	}
	return options, meta, nil
}

const messageColumns = `id, agent, direction, kind, text, transcript, reply_to, task, run_id, state, answer, options, meta, ts`

func scanMessage(row interface{ Scan(...any) error }) (Message, error) {
	var m Message
	var options, meta string
	var ts int64
	if err := row.Scan(&m.ID, &m.Agent, &m.Direction, &m.Kind, &m.Text, &m.Transcript, &m.ReplyTo, &m.Task, &m.RunID, &m.State, &m.Answer, &options, &meta, &ts); err != nil {
		return Message{}, err
	}
	m.TS = time.UnixMilli(ts).UTC()
	if options != "" {
		if err := json.Unmarshal([]byte(options), &m.Options); err != nil {
			return Message{}, fmt.Errorf("decoding options of %s: %w", m.ID, err)
		}
	}
	if meta != "" {
		var sm storedMeta
		if err := json.Unmarshal([]byte(meta), &sm); err != nil {
			return Message{}, fmt.Errorf("decoding meta of %s: %w", m.ID, err)
		}
		m.Permission = &Permission{Tool: sm.Tool, Summary: sm.Summary, input: sm.Input}
	}
	return m, nil
}

// Message returns one message with its attachments.
func (s *Store) Message(id string) (Message, error) {
	m, err := scanMessage(s.db.QueryRow(`SELECT `+messageColumns+` FROM messages WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	if err != nil {
		return Message{}, fmt.Errorf("reading chat message: %w", err)
	}
	withAtt, err := s.attachTo([]Message{m})
	if err != nil {
		return Message{}, err
	}
	return withAtt[0], nil
}

// Page selects a slice of a thread, newest first.
type Page struct {
	// Before is an exclusive upper bound id; empty starts at the newest.
	Before string
	Limit  int
	// Task keeps only messages of that task.
	Task string
}

func (p Page) limit() int {
	switch {
	case p.Limit <= 0:
		return DefaultPageLimit
	case p.Limit > MaxPageLimit:
		return MaxPageLimit
	}
	return p.Limit
}

// Messages returns a page of agent's thread, newest first.
func (s *Store) Messages(agent string, p Page) ([]Message, error) {
	query := `SELECT ` + messageColumns + ` FROM messages WHERE agent = ?`
	args := []any{agent}
	if p.Before != "" {
		query += ` AND id < ?`
		args = append(args, p.Before)
	}
	if p.Task != "" {
		query += ` AND task = ?`
		args = append(args, p.Task)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, p.limit())
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("reading chat messages: %w", err)
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading chat messages: %w", err)
	}
	return s.attachTo(out)
}

// attachTo fills each message's attachments.
func (s *Store) attachTo(ms []Message) ([]Message, error) {
	if len(ms) == 0 {
		return ms, nil
	}
	ids := make([]any, len(ms))
	index := make(map[string]int, len(ms))
	for i, m := range ms {
		ids[i] = m.ID
		index[m.ID] = i
	}
	rows, err := s.db.Query(`SELECT `+attachmentColumns+` FROM attachments WHERE message_id IN (?`+strings.Repeat(",?", len(ids)-1)+`) ORDER BY id`, ids...)
	if err != nil {
		return nil, fmt.Errorf("reading attachments: %w", err)
	}
	defer rows.Close()
	out := append([]Message(nil), ms...)
	for rows.Next() {
		a, err := scanAttachment(rows)
		if err != nil {
			return nil, err
		}
		i := index[a.MessageID]
		out[i].Attachments = append(out[i].Attachments, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading attachments: %w", err)
	}
	return out, nil
}

// Threads lists every agent with messages, most recent first.
func (s *Store) Threads() ([]Thread, error) {
	rows, err := s.db.Query(`SELECT ` + messageColumns + ` FROM messages m
		WHERE id = (SELECT MAX(id) FROM messages WHERE agent = m.agent)
		ORDER BY id DESC`)
	if err != nil {
		return nil, fmt.Errorf("reading chat threads: %w", err)
	}
	var last []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		last = append(last, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, fmt.Errorf("reading chat threads: %w", err)
	}
	last, err = s.attachTo(last)
	if err != nil {
		return nil, err
	}
	out := make([]Thread, 0, len(last))
	for _, m := range last {
		var unread int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE agent = ? AND direction = ?
			AND id > COALESCE((SELECT last_read FROM reads WHERE agent = ?), '')`, m.Agent, DirectionOut, m.Agent).Scan(&unread); err != nil {
			return nil, fmt.Errorf("counting unread: %w", err)
		}
		out = append(out, Thread{Agent: m.Agent, LastMessage: m, Unread: unread, Pinned: m.Agent == DefaultAgent})
	}
	return out, nil
}

// MarkRead marks agent's thread read up to and including id. The marker
// only moves forward.
func (s *Store) MarkRead(agent, id string) error {
	_, err := s.db.Exec(`INSERT INTO reads (agent, last_read) VALUES (?, ?)
		ON CONFLICT(agent) DO UPDATE SET last_read = excluded.last_read WHERE excluded.last_read > reads.last_read`, agent, id)
	if err != nil {
		return fmt.Errorf("marking chat read: %w", err)
	}
	return nil
}

// Transition moves message id from one of from to state, recording answer
// when non-empty. It reports false, without error, when the message was not
// in one of from — someone else settled it first.
func (s *Store) Transition(id string, from []string, state, answer string) (bool, error) {
	if len(from) == 0 {
		return false, errors.New("transition needs at least one from-state")
	}
	args := []any{state, answer, answer, id}
	for _, f := range from {
		args = append(args, f)
	}
	res, err := s.db.Exec(`UPDATE messages SET state = ?, answer = CASE WHEN ? = '' THEN answer ELSE ? END
		WHERE id = ? AND state IN (?`+strings.Repeat(",?", len(from)-1)+`)`, args...)
	if err != nil {
		return false, fmt.Errorf("updating chat message: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("updating chat message: %w", err)
	}
	return n == 1, nil
}

// SetTranscript records a voice message's transcript.
func (s *Store) SetTranscript(id, transcript string) error {
	if _, err := s.db.Exec(`UPDATE messages SET transcript = ? WHERE id = ?`, transcript, id); err != nil {
		return fmt.Errorf("storing transcript: %w", err)
	}
	return nil
}

// OpenMessages returns agent's messages of kind in state, oldest first.
func (s *Store) OpenMessages(agent, kind, state string) ([]Message, error) {
	rows, err := s.db.Query(`SELECT `+messageColumns+` FROM messages WHERE agent = ? AND kind = ? AND state = ? ORDER BY id`, agent, kind, state)
	if err != nil {
		return nil, fmt.Errorf("reading open messages: %w", err)
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ExpireOpen moves every open message to expired: their waiters died with
// the previous daemon. It returns the ids it moved.
func (s *Store) ExpireOpen() ([]string, error) {
	rows, err := s.db.Query(`SELECT id FROM messages WHERE state = ?`, StateOpen)
	if err != nil {
		return nil, fmt.Errorf("reading open messages: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(`UPDATE messages SET state = ? WHERE state = ?`, StateExpired, StateOpen); err != nil {
		return nil, fmt.Errorf("expiring open messages: %w", err)
	}
	return ids, nil
}

const attachmentColumns = `id, message_id, agent, filename, mime, bytes, path, created`

func scanAttachment(row interface{ Scan(...any) error }) (Attachment, error) {
	var a Attachment
	var created int64
	if err := row.Scan(&a.ID, &a.MessageID, &a.Agent, &a.Filename, &a.MIME, &a.Bytes, &a.Path, &created); err != nil {
		return Attachment{}, err
	}
	a.Created = time.UnixMilli(created).UTC()
	a.Available = a.Path != ""
	return a, nil
}

// InsertAttachment stores a, not yet linked to a message.
func (s *Store) InsertAttachment(a Attachment) error {
	_, err := s.db.Exec(`INSERT INTO attachments (`+attachmentColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.MessageID, a.Agent, a.Filename, a.MIME, a.Bytes, a.Path, a.Created.UnixMilli())
	if err != nil {
		return fmt.Errorf("inserting attachment: %w", err)
	}
	return nil
}

// Attachment returns one attachment.
func (s *Store) Attachment(id string) (Attachment, error) {
	a, err := scanAttachment(s.db.QueryRow(`SELECT `+attachmentColumns+` FROM attachments WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Attachment{}, ErrNotFound
	}
	if err != nil {
		return Attachment{}, fmt.Errorf("reading attachment: %w", err)
	}
	return a, nil
}

// LinkAttachments attaches uploads ids to message messageID. Each must be
// agent's own and not yet linked; otherwise nothing is linked.
func (s *Store) LinkAttachments(messageID, agent string, ids []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("linking attachments: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, id := range ids {
		res, err := tx.Exec(`UPDATE attachments SET message_id = ? WHERE id = ? AND agent = ? AND message_id = ''`, messageID, id, agent)
		if err != nil {
			return fmt.Errorf("linking attachment: %w", err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return fmt.Errorf("attachment %s is not an unused upload for %s", id, agent)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("linking attachments: %w", err)
	}
	return nil
}

// PruneAttachments clears the path of every attachment created before
// cutoff and returns the paths cleared, for the caller to delete.
func (s *Store) PruneAttachments(cutoff time.Time) ([]string, error) {
	rows, err := s.db.Query(`SELECT path FROM attachments WHERE path != '' AND created < ?`, cutoff.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("finding expired attachments: %w", err)
	}
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return nil, err
		}
		paths = append(paths, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(`UPDATE attachments SET path = '' WHERE path != '' AND created < ?`, cutoff.UnixMilli()); err != nil {
		return nil, fmt.Errorf("pruning attachments: %w", err)
	}
	return paths, nil
}

// AddCapture records a pending auto-capture.
func (s *Store) AddCapture(c Capture) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO captures (message_id, agent, prompt, delivered_at, tool_posted) VALUES (?, ?, ?, ?, ?)`,
		c.MessageID, c.Agent, c.Prompt, c.DeliveredAt.UnixMilli(), boolInt(c.ToolPosted))
	if err != nil {
		return fmt.Errorf("recording capture: %w", err)
	}
	return nil
}

// Captures returns agent's pending captures, oldest first.
func (s *Store) Captures(agent string) ([]Capture, error) {
	rows, err := s.db.Query(`SELECT message_id, agent, prompt, delivered_at, tool_posted FROM captures WHERE agent = ? ORDER BY message_id`, agent)
	if err != nil {
		return nil, fmt.Errorf("reading captures: %w", err)
	}
	defer rows.Close()
	var out []Capture
	for rows.Next() {
		var c Capture
		var at int64
		var posted int
		if err := rows.Scan(&c.MessageID, &c.Agent, &c.Prompt, &at, &posted); err != nil {
			return nil, err
		}
		c.DeliveredAt = time.UnixMilli(at).UTC()
		c.ToolPosted = posted != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

// MarkCapturesToolPosted flags every pending capture of agent: the agent
// posted through leo_chat_post since those deliveries.
func (s *Store) MarkCapturesToolPosted(agent string) error {
	if _, err := s.db.Exec(`UPDATE captures SET tool_posted = 1 WHERE agent = ?`, agent); err != nil {
		return fmt.Errorf("flagging captures: %w", err)
	}
	return nil
}

// RemoveCapture clears one pending capture.
func (s *Store) RemoveCapture(messageID string) error {
	if _, err := s.db.Exec(`DELETE FROM captures WHERE message_id = ?`, messageID); err != nil {
		return fmt.Errorf("clearing capture: %w", err)
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

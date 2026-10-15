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

// ErrUnusable reports an upload a message may not link: another agent's,
// or already sent.
var ErrUnusable = errors.New("unusable upload")

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
	if err := createPrivate(path); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening chat store: %w", err)
	}
	// One connection serializes writers, so SQLITE_BUSY never surfaces;
	// the chat load is a handful of rows per minute.
	db.SetMaxOpenConns(1)
	if err := migrate(db, migrations); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// createPrivate creates the database file at path with mode 0600 before
// SQLite opens it — SQLite gives the -wal and -shm files it creates the
// database file's mode — and restricts an existing database and its
// sidecars, which an earlier build may have left readable.
func createPrivate(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- the daemon's own state path
	if err != nil {
		return fmt.Errorf("creating chat store: %w", err)
	}
	_ = f.Close()
	// -wal and -shm are the files SQLite keeps beside a WAL-mode database.
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(p, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("restricting chat store permissions: %w", err)
		}
	}
	return nil
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
func (s *Store) InsertMessage(m Message) error { return s.InsertMessageWith(m, nil, nil) }

// InsertMessageWith stores m together with its new attachments add and the
// earlier uploads link (each agent's own or unassigned, and unused) in one
// transaction: on any failure nothing is stored.
func (s *Store) InsertMessageWith(m Message, add []Attachment, link []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("inserting chat message: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := insertMessageTx(tx, m, add, link); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("inserting chat message: %w", err)
	}
	return nil
}

// insertMessageTx writes m, its new attachments and its linked uploads in tx.
func insertMessageTx(tx *sql.Tx, m Message, add []Attachment, link []string) error {
	input, meta, answer, err := encodeExtras(m)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO messages (`+messageColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.Agent, m.Direction, m.Kind, m.Text, m.Transcript, m.ReplyTo, m.Task, m.RunID, m.State, answer, input, meta, m.DeliveryRef, m.TS.UnixMilli(),
		millisOrZero(m.Deadline), m.ClosedReason, m.Asker)
	if err != nil {
		return fmt.Errorf("inserting chat message: %w", err)
	}
	for _, a := range add {
		a.MessageID = m.ID
		if err := insertAttachment(tx, a); err != nil {
			return err
		}
	}
	return linkAttachments(tx, m.ID, m.Agent, link)
}

// encodeExtras renders a message's input, meta and answer columns.
func encodeExtras(m Message) (input, meta, answer string, err error) {
	if m.Input != nil {
		b, err := json.Marshal(m.Input)
		if err != nil {
			return "", "", "", fmt.Errorf("encoding input: %w", err)
		}
		input = string(b)
	}
	if answer, err = encodeAnswer(m.Answer); err != nil {
		return "", "", "", err
	}
	if m.Permission != nil {
		b, err := json.Marshal(storedMeta{Tool: m.Permission.Tool, Summary: m.Permission.Summary, Input: m.Permission.input})
		if err != nil {
			return "", "", "", fmt.Errorf("encoding permission: %w", err)
		}
		meta = string(b)
	}
	return input, meta, answer, nil
}

// encodeAnswer renders an answer for the answer column; "" for none (nil).
// An empty selection is an answer: it encodes as {"values":[],"text":""}.
func encodeAnswer(a *Answer) (string, error) {
	if a == nil {
		return "", nil
	}
	b, err := json.Marshal(a)
	if err != nil {
		return "", fmt.Errorf("encoding answer: %w", err)
	}
	return string(b), nil
}

const messageColumns = `id, agent, direction, kind, text, transcript, reply_to, task, run_id, state, answer, input, meta, delivery_ref, ts, deadline, closed_reason, asker`

// millisOrZero stores a zero time as 0 (no deadline) rather than the
// year-1 millisecond count.
func millisOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func scanMessage(row interface{ Scan(...any) error }) (Message, error) {
	var m Message
	var input, answer, meta string
	var ts, deadline int64
	if err := row.Scan(&m.ID, &m.Agent, &m.Direction, &m.Kind, &m.Text, &m.Transcript, &m.ReplyTo, &m.Task, &m.RunID, &m.State, &answer, &input, &meta, &m.DeliveryRef, &ts, &deadline, &m.ClosedReason, &m.Asker); err != nil {
		return Message{}, err
	}
	m.TS = time.UnixMilli(ts).UTC()
	if deadline != 0 {
		m.Deadline = time.UnixMilli(deadline).UTC()
	}
	if input != "" {
		m.Input = new(Input)
		if err := json.Unmarshal([]byte(input), m.Input); err != nil {
			return Message{}, fmt.Errorf("decoding input of %s: %w", m.ID, err)
		}
	}
	if answer != "" {
		m.Answer = new(Answer)
		if err := json.Unmarshal([]byte(answer), m.Answer); err != nil {
			return Message{}, fmt.Errorf("decoding answer of %s: %w", m.ID, err)
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
func (s *Store) Messages(agent string, p Page) ([]Message, bool, error) {
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
	// One row past the page says whether an older page exists.
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, p.limit()+1)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, false, fmt.Errorf("reading chat messages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Message{}
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, false, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("reading chat messages: %w", err)
	}
	hasMore := len(out) > p.limit()
	if hasMore {
		out = out[:p.limit()]
	}
	out, err = s.attachTo(out)
	return out, hasMore, err
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
	// #nosec G202 -- only constant columns and "?" placeholders are concatenated
	rows, err := s.db.Query(`SELECT `+attachmentColumns+` FROM attachments WHERE message_id IN (?`+strings.Repeat(",?", len(ids)-1)+`) ORDER BY id`, ids...)
	if err != nil {
		return nil, fmt.Errorf("reading attachments: %w", err)
	}
	defer func() { _ = rows.Close() }()
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
			_ = rows.Close()
			return nil, err
		}
		last = append(last, m)
	}
	err = rows.Err()
	_ = rows.Close()
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
// when it is not nil. It reports false, without error, when the message was not
// in one of from — someone else settled it first.
func (s *Store) Transition(id string, from []string, state string, answer *Answer) (bool, error) {
	if len(from) == 0 {
		return false, errors.New("transition needs at least one from-state")
	}
	encoded, err := encodeAnswer(answer)
	if err != nil {
		return false, err
	}
	args := []any{state, encoded, encoded, id}
	for _, f := range from {
		args = append(args, f)
	}
	// #nosec G202 -- only "?" placeholders are concatenated
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

// CloseAsk moves an open ask to expired with reason. It reports false,
// without error, when id is not an open ask — someone else settled it first.
func (s *Store) CloseAsk(id, reason string) (bool, error) {
	res, err := s.db.Exec(`UPDATE messages SET state = ?, closed_reason = ? WHERE id = ? AND kind = ? AND state = ?`,
		StateExpired, reason, id, KindAsk, StateOpen)
	if err != nil {
		return false, fmt.Errorf("closing chat ask: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("closing chat ask: %w", err)
	}
	return n == 1, nil
}

// CloseAsksOf moves every open ask posted by asker to expired with reason,
// returning their ids.
func (s *Store) CloseAsksOf(asker, reason string) ([]string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("closing chat asks: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.Query(`SELECT id FROM messages WHERE kind = ? AND state = ? AND asker = ? ORDER BY id`, KindAsk, StateOpen, asker)
	if err != nil {
		return nil, fmt.Errorf("closing chat asks: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("closing chat asks: %w", err)
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, fmt.Errorf("closing chat asks: %w", err)
	}
	if _, err := tx.Exec(`UPDATE messages SET state = ?, closed_reason = ? WHERE kind = ? AND state = ? AND asker = ?`,
		StateExpired, reason, KindAsk, StateOpen, asker); err != nil {
		return nil, fmt.Errorf("closing chat asks: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("closing chat asks: %w", err)
	}
	return ids, nil
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
	defer func() { _ = rows.Close() }()
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

// ExpireOpen moves every open message of kind to expired. It returns the ids
// it moved.
func (s *Store) ExpireOpen(kind string) ([]string, error) {
	moved, err := s.moveAll(kind, StateOpen, StateExpired)
	if err != nil {
		return nil, fmt.Errorf("expiring open messages: %w", err)
	}
	ids := make([]string, len(moved))
	for i, m := range moved {
		ids[i] = m.ID
	}
	return ids, nil
}

// SetDeliveryRef records the ref message id is being delivered under.
func (s *Store) SetDeliveryRef(id, ref string) error {
	if _, err := s.db.Exec(`UPDATE messages SET delivery_ref = ? WHERE id = ?`, ref, id); err != nil {
		return fmt.Errorf("recording delivery ref: %w", err)
	}
	return nil
}

// MessagesInState returns every message in state, oldest first.
func (s *Store) MessagesInState(state string) ([]Message, error) {
	rows, err := s.db.Query(`SELECT `+messageColumns+` FROM messages WHERE state = ? ORDER BY id`, state)
	if err != nil {
		return nil, fmt.Errorf("reading %s messages: %w", state, err)
	}
	defer func() { _ = rows.Close() }()
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

// moveAll moves every message of kind in state from to state to, returning
// their ids and agents.
func (s *Store) moveAll(kind, from, to string) ([]Message, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.Query(`SELECT id, agent FROM messages WHERE kind = ? AND state = ? ORDER BY id`, kind, from)
	if err != nil {
		return nil, err
	}
	var moved []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.Agent); err != nil {
			_ = rows.Close()
			return nil, err
		}
		moved = append(moved, m)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE messages SET state = ? WHERE kind = ? AND state = ?`, to, kind, from); err != nil {
		return nil, err
	}
	return moved, tx.Commit()
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
func (s *Store) InsertAttachment(a Attachment) error { return insertAttachment(s.db, a) }

func insertAttachment(db interface {
	Exec(string, ...any) (sql.Result, error)
}, a Attachment) error {
	_, err := db.Exec(`INSERT INTO attachments (`+attachmentColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
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

// linkAttachments attaches uploads ids to message messageID. Each must be
// agent's own or unassigned, and not yet linked.
func linkAttachments(tx *sql.Tx, messageID, agent string, ids []string) error {
	for _, id := range ids {
		// An upload made without an agent is claimed by the first message
		// that links it.
		res, err := tx.Exec(`UPDATE attachments SET message_id = ?, agent = ? WHERE id = ? AND (agent = ? OR agent = '') AND message_id = ''`, messageID, agent, id, agent)
		if err != nil {
			return fmt.Errorf("linking attachment: %w", err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return fmt.Errorf("%w: attachment %s is not an unused upload for %s", ErrUnusable, id, agent)
		}
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
			_ = rows.Close()
			return nil, err
		}
		paths = append(paths, p)
	}
	err = rows.Err()
	_ = rows.Close()
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
	_, err := s.db.Exec(`INSERT OR REPLACE INTO captures (message_id, agent, prompt, delivered_at) VALUES (?, ?, ?, ?)`,
		c.MessageID, c.Agent, c.Prompt, c.DeliveredAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("recording capture: %w", err)
	}
	return nil
}

// Captures returns agent's pending captures, oldest first.
func (s *Store) Captures(agent string) ([]Capture, error) {
	rows, err := s.db.Query(`SELECT message_id, agent, prompt, delivered_at FROM captures WHERE agent = ? ORDER BY message_id`, agent)
	if err != nil {
		return nil, fmt.Errorf("reading captures: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Capture
	for rows.Next() {
		var c Capture
		var at int64
		if err := rows.Scan(&c.MessageID, &c.Agent, &c.Prompt, &at); err != nil {
			return nil, err
		}
		c.DeliveredAt = time.UnixMilli(at).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// RemoveCapture clears one pending capture, reporting whether it was still
// there: of two concurrent Stop hooks, only the one that removes it posts.
func (s *Store) RemoveCapture(messageID string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM captures WHERE message_id = ?`, messageID)
	if err != nil {
		return false, fmt.Errorf("clearing capture: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("clearing capture: %w", err)
	}
	return n == 1, nil
}

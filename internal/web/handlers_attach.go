package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

const (
	// attachHeartbeat is how often the attach socket sends a JSON ping, well
	// inside Cloudflare's ~100 s idle timeout.
	attachHeartbeat = 30 * time.Second
	// attachWriteTimeout bounds each frame write; a client that cannot take
	// a frame in this long is dropped.
	attachWriteTimeout = 10 * time.Second
	// attachReadLimit caps an incoming (control) frame.
	attachReadLimit = 4 << 10
	attachBufSize   = 32 << 10

	defaultAttachCols uint16 = 80
	defaultAttachRows uint16 = 24
	maxAttachDim             = 1000
)

// attachProc is a running read-only tmux client: its terminal output, a
// resize, and a Close that kills it and releases the PTY.
type attachProc interface {
	Read(p []byte) (int, error)
	Resize(cols, rows uint16) error
	Close() error
}

// attachControl is a text frame on the attach socket, in either direction:
// {"type":"ping"|"pong"}, {"type":"resize","cols":N,"rows":M} from the
// client, {"type":"error","message":...} from the daemon.
type attachControl struct {
	Type    string `json:"type"`
	Cols    uint16 `json:"cols,omitempty"`
	Rows    uint16 `json:"rows,omitempty"`
	Message string `json:"message,omitempty"`
}

// attachEnd is why an attach socket is closing. now skips the close
// handshake, for a peer that is already gone.
type attachEnd struct {
	code   websocket.StatusCode
	reason string
	now    bool
}

// handleAPIAgentAttach streams an agent's tmux session to a read-only
// viewer over a WebSocket: binary frames carry terminal output, text frames
// carry JSON control. Input frames are refused — the client is
// `tmux attach -r`, and nothing the socket receives reaches its PTY.
// GET /api/v1/agents/{name}/attach[?cols=N&rows=M]
func (s *Server) handleAPIAgentAttach(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := s.processStates()[name]; !ok {
		writeJSON(w, http.StatusNotFound, apiResponse{Error: "unknown agent " + strconv.Quote(name)})
		return
	}
	cols, rows, err := attachSize(r.URL.Query())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	// The server's Read/WriteTimeout deadlines survive the hijack; a
	// long-lived socket must clear them (http.Hijacker leaves that to us).
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return // Accept has written the error response.
	}
	defer conn.CloseNow() //nolint:errcheck
	conn.SetReadLimit(attachReadLimit)

	proc, err := s.startAttach(agent.SessionName(name), cols, rows)
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "attach failed")
		return
	}
	defer proc.Close() //nolint:errcheck

	// The reader gets its own context, canceled only after the close
	// handshake: canceling a blocked conn.Read tears the connection down,
	// which would race the close frame and leave the client with a bare EOF.
	readCtx, cancelRead := context.WithCancel(context.Background())
	defer cancelRead()
	writeCtx, cancelWrite := context.WithCancel(context.Background())
	defer cancelWrite()
	end := make(chan attachEnd, 4)
	// A revoked device loses its view promptly (the lost-phone case).
	revoked, stopWatch := s.watchDeviceRevocation(r)
	defer stopWatch()
	go func() {
		select {
		case <-revoked:
			end <- attachEnd{code: websocket.StatusPolicyViolation, reason: "device revoked"}
		case <-writeCtx.Done():
		}
	}()
	go pumpAttachOutput(writeCtx, conn, proc, end)
	go readAttachControl(readCtx, conn, proc, end)
	ticks, stop := s.attachTicker(attachHeartbeat)
	defer stop()
	go sendAttachHeartbeats(writeCtx, conn, ticks, end)

	reason := <-end
	cancelWrite()
	if reason.now {
		return
	}
	_ = conn.Close(reason.code, reason.reason)
}

func pumpAttachOutput(ctx context.Context, conn *websocket.Conn, proc attachProc, end chan<- attachEnd) {
	buf := make([]byte, attachBufSize)
	for {
		n, err := proc.Read(buf)
		if n > 0 {
			if werr := writeAttachFrame(ctx, conn, websocket.MessageBinary, buf[:n]); werr != nil {
				end <- attachEnd{now: true}
				return
			}
		}
		if err != nil {
			// EOF (or EIO from a PTY whose client exited): the session
			// ended or the agent stopped.
			end <- attachEnd{code: websocket.StatusNormalClosure, reason: "session ended"}
			return
		}
	}
}

func readAttachControl(ctx context.Context, conn *websocket.Conn, proc attachProc, end chan<- attachEnd) {
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			end <- attachEnd{now: true}
			return
		}
		if typ == websocket.MessageBinary {
			end <- attachEnd{code: websocket.StatusPolicyViolation, reason: "input is not accepted"}
			return
		}
		var msg attachControl
		if err := json.Unmarshal(data, &msg); err != nil {
			_ = writeAttachControl(ctx, conn, attachControl{Type: "error", Message: "control frames must be JSON"})
			continue
		}
		switch msg.Type {
		case "ping":
			_ = writeAttachControl(ctx, conn, attachControl{Type: "pong"})
		case "pong":
		case "resize":
			if !validAttachDim(int(msg.Cols)) || !validAttachDim(int(msg.Rows)) {
				_ = writeAttachControl(ctx, conn, attachControl{Type: "error", Message: "resize needs cols and rows in 1-" + strconv.Itoa(maxAttachDim)})
				continue
			}
			_ = proc.Resize(msg.Cols, msg.Rows)
		default:
			_ = writeAttachControl(ctx, conn, attachControl{Type: "error", Message: "unknown control type " + strconv.Quote(msg.Type)})
		}
	}
}

func sendAttachHeartbeats(ctx context.Context, conn *websocket.Conn, ticks <-chan time.Time, end chan<- attachEnd) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			if err := writeAttachControl(ctx, conn, attachControl{Type: "ping"}); err != nil {
				end <- attachEnd{now: true}
				return
			}
		}
	}
}

func writeAttachControl(ctx context.Context, conn *websocket.Conn, msg attachControl) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return writeAttachFrame(ctx, conn, websocket.MessageText, b)
}

// writeAttachFrame writes one frame under attachWriteTimeout. Conn.Write is
// safe for concurrent use, so the three goroutines share conn.
func writeAttachFrame(ctx context.Context, conn *websocket.Conn, typ websocket.MessageType, b []byte) error {
	ctx, cancel := context.WithTimeout(ctx, attachWriteTimeout)
	defer cancel()
	return conn.Write(ctx, typ, b)
}

// attachSize reads the optional initial terminal size from ?cols=&rows=.
func attachSize(q url.Values) (cols, rows uint16, err error) {
	dim := func(key string, def uint16) (uint16, error) {
		raw := q.Get(key)
		if raw == "" {
			return def, nil
		}
		n, err := strconv.ParseUint(raw, 10, 16)
		if err != nil || n < 1 || n > maxAttachDim {
			return 0, errors.New(key + " must be an integer in 1-" + strconv.Itoa(maxAttachDim))
		}
		return uint16(n), nil
	}
	if cols, err = dim("cols", defaultAttachCols); err != nil {
		return 0, 0, err
	}
	rows, err = dim("rows", defaultAttachRows)
	return cols, rows, err
}

func validAttachDim(n int) bool { return n >= 1 && n <= maxAttachDim }

// realTicker is the production clock for the attach heartbeat and the
// revocation watcher.
func realTicker(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(d)
	return t.C, t.Stop
}

// attachCommand builds the read-only tmux client for session: an exact
// target, UTF-8 forced (-u; the launchd daemon has no LANG, and tmux
// sanitizes output for a client it thinks lacks UTF-8), and no inherited
// $TMUX, which would make tmux refuse to nest.
func attachCommand(tmuxPath, session string) *exec.Cmd {
	cmd := exec.Command(tmuxPath, tmux.Args("-u", "attach-session", "-r", "-t", tmux.Target(session))...)
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "TMUX=") || strings.HasPrefix(kv, "TMUX_PANE=") || strings.HasPrefix(kv, "TERM=")
	})
	env = append(env, "TERM=xterm-256color")
	cmd.Env = env
	return cmd
}

// ptyAttach is attachProc over a real PTY.
type ptyAttach struct {
	ptmx *os.File
	cmd  *exec.Cmd
	once sync.Once
}

// startTmuxAttach runs attachCommand in a new PTY of the given size.
func startTmuxAttach(session string, cols, rows uint16) (attachProc, error) {
	cmd := attachCommand(findTmuxPath(), session)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return nil, err
	}
	return &ptyAttach{ptmx: ptmx, cmd: cmd}, nil
}

func (p *ptyAttach) Read(b []byte) (int, error) { return p.ptmx.Read(b) }

func (p *ptyAttach) Resize(cols, rows uint16) error {
	return pty.Setsize(p.ptmx, &pty.Winsize{Cols: cols, Rows: rows})
}

// Close kills the tmux client, closes the PTY and reaps the process.
func (p *ptyAttach) Close() error {
	p.once.Do(func() {
		_ = p.cmd.Process.Kill()
		_ = p.ptmx.Close()
		_ = p.cmd.Wait()
	})
	return nil
}

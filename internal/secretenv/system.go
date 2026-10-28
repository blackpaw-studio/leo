package secretenv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"
)

// DefaultTimeout bounds one resolution, so a hung command or a keychain
// prompt fails the spawn instead of hanging it.
const DefaultTimeout = 10 * time.Second

// MaxValueSize caps a resolved value.
const MaxValueSize = 64 << 10

// maxStderrReason caps the stderr excerpt a keychain or secret-service
// failure carries.
const maxStderrReason = 200

// securityPath is macOS's keychain CLI. It is absolute because a launchd
// daemon's PATH is minimal and a PATH lookup could pick up another binary.
const securityPath = "/usr/bin/security"

// keychainNotFound is the exit status `security` uses for a missing item
// (errSecItemNotFound).
const keychainNotFound = 44

// Runner runs argv with no shell and empty stdin, and returns its output.
// exitCode is the process's exit status when it ran to completion (err nil,
// even for a non-zero status); err reports a process that could not start or
// was stopped by ctx.
type Runner func(ctx context.Context, argv []string) (stdout, stderr []byte, exitCode int, err error)

// ResolutionError reports a reference that could not be resolved. Reason
// never carries the value or a backend's stdout. Unavailable marks a backend
// that cannot run in this process at all (another platform's store, or
// systemd-creds outside its unit), as opposed to one that failed on this
// reference: the same config may resolve on another host.
type ResolutionError struct {
	Ref         Ref
	Reason      string
	Unavailable bool
}

func (e *ResolutionError) Error() string {
	return "secret " + e.Ref.String() + ": " + e.Reason
}

// System resolves references against this machine's secret stores. The zero
// value uses the real OS; tests inject every seam.
type System struct {
	GOOS      string                      // "" → runtime.GOOS
	Run       Runner                      // nil → exec with no shell
	Getuid    func() int                  // nil → os.Getuid
	LookupEnv func(string) (string, bool) // nil → os.LookupEnv
	Timeout   time.Duration               // 0 → DefaultTimeout
}

// Resolve returns the referenced value. Every failure is a *ResolutionError.
func (s System) Resolve(ref Ref) (string, error) {
	fail := func(format string, args ...any) (string, error) {
		return "", &ResolutionError{Ref: ref, Reason: fmt.Sprintf(format, args...)}
	}
	if err := ref.Validate(); err != nil {
		return fail("invalid reference: %v", err)
	}
	if reason := s.unavailable(ref); reason != "" {
		return "", &ResolutionError{Ref: ref, Reason: reason, Unavailable: true}
	}
	var (
		value  string
		reason string
	)
	switch ref.Backend {
	case Keychain:
		value, reason = s.keychain(ref)
	case SecretService:
		value, reason = s.secretService(ref)
	case SystemdCreds:
		value, reason = s.systemdCreds(ref)
	case File:
		value, reason = s.file(ref.Path)
	case Command:
		value, reason = s.command(ref)
	}
	if reason == "" {
		reason = checkValue(value)
	}
	if reason != "" {
		return fail("%s", reason)
	}
	return value, nil
}

// requiredGOOS gates the platform-specific backends. A mismatch is a
// resolution error, not a parse error, so one shared config serves both.
var requiredGOOS = map[Backend]string{
	Keychain:      "darwin",
	SecretService: "linux",
	SystemdCreds:  "linux",
}

// noCredentialsDirectory is why systemd-creds cannot run outside its unit.
const noCredentialsDirectory = "CREDENTIALS_DIRECTORY is not set (load the credential with LoadCredential= in the unit)"

// unavailable is why ref's backend cannot run in this process, or "".
func (s System) unavailable(ref Ref) string {
	if need := requiredGOOS[ref.Backend]; need != "" && s.goos() != need {
		return "not supported on " + s.goos()
	}
	if ref.Backend == SystemdCreds {
		if dir, ok := s.lookupEnv("CREDENTIALS_DIRECTORY"); !ok || dir == "" {
			return noCredentialsDirectory
		}
	}
	return ""
}

func (s System) keychain(ref Ref) (string, string) {
	argv := []string{securityPath, "find-generic-password", "-s", ref.Service}
	if ref.Account != "" {
		argv = append(argv, "-a", ref.Account)
	}
	argv = append(argv, "-w")
	out, code, reason := s.exec(argv)
	switch {
	case reason != "":
		return "", reason
	case code == keychainNotFound:
		return "", "item not found"
	case code != 0:
		return "", exitReason(code, out.stderr)
	}
	// `security -w` appends exactly one newline to the stored value.
	return strings.TrimSuffix(string(out.stdout), "\n"), ""
}

func (s System) secretService(ref Ref) (string, string) {
	argv := []string{"secret-tool", "lookup"}
	for _, k := range slices.Sorted(maps.Keys(ref.Attributes)) {
		argv = append(argv, k, ref.Attributes[k])
	}
	out, code, reason := s.exec(argv)
	switch {
	case reason != "":
		return "", reason
	case code != 0:
		return "", exitReason(code, out.stderr)
	}
	return string(out.stdout), ""
}

func (s System) command(ref Ref) (string, string) {
	out, code, reason := s.exec(ref.Argv)
	switch {
	case reason != "":
		return "", reason
	case code != 0:
		// The command's stderr may echo what it read; leave it out.
		return "", fmt.Sprintf("exited %d", code)
	}
	return trimNewlines(string(out.stdout)), ""
}

func (s System) systemdCreds(ref Ref) (string, string) {
	dir, _ := s.lookupEnv("CREDENTIALS_DIRECTORY") // set: checked by unavailable
	data, reason := readCapped(filepath.Join(dir, ref.Name), nil)
	if reason == "file not found" {
		reason = "credential not found"
	}
	return trimNewlines(string(data)), reason
}

func (s System) file(path string) (string, string) {
	if !filepath.IsAbs(path) {
		return "", "path must be absolute"
	}
	data, reason := readCapped(path, func(fi fs.FileInfo) string {
		if perm := fi.Mode().Perm(); perm&^0o600 != 0 {
			return fmt.Sprintf("mode %04o is too open (want 0600 or stricter)", perm)
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok || int(st.Uid) != s.getuid() {
			return "not owned by the current user"
		}
		return ""
	})
	return trimNewlines(string(data)), reason
}

// readCapped opens path without blocking on a FIFO, checks the opened file
// (not the path, so a swap after the check cannot slip through), and reads
// at most MaxValueSize+1 bytes.
func readCapped(path string, check func(fs.FileInfo) string) ([]byte, string) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, openReason(err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, openReason(err)
	}
	if !fi.Mode().IsRegular() {
		return nil, "not a regular file"
	}
	if check != nil {
		if reason := check(fi); reason != "" {
			return nil, reason
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxValueSize+1))
	if err != nil {
		return nil, openReason(err)
	}
	return data, ""
}

func openReason(err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "file not found"
	case errors.Is(err, fs.ErrPermission):
		return "permission denied"
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Op + ": " + pe.Err.Error()
	}
	return err.Error()
}

type output struct{ stdout, stderr []byte }

// exec runs argv under the timeout. A non-empty reason means it did not run
// to completion.
func (s System) exec(argv []string) (output, int, string) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout())
	defer cancel()
	stdout, stderr, code, err := s.run()(ctx, argv)
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return output{}, 0, "timed out after " + s.timeout().String()
	case err != nil:
		return output{}, 0, "could not run: " + err.Error()
	}
	return output{stdout: stdout, stderr: stderr}, code, ""
}

// exitReason is the exit status plus the first stderr line, capped.
func exitReason(code int, stderr []byte) string {
	line, _, _ := bytes.Cut(bytes.TrimLeft(stderr, "\r\n"), []byte("\n"))
	line = bytes.TrimRight(line, "\r")
	if len(line) > maxStderrReason {
		line = line[:maxStderrReason]
	}
	if len(line) == 0 {
		return fmt.Sprintf("exited %d", code)
	}
	return fmt.Sprintf("exited %d: %s", code, strings.ToValidUTF8(string(line), "?"))
}

func checkValue(v string) string {
	switch {
	case len(v) > MaxValueSize:
		return "value exceeds 64 KiB"
	case strings.IndexByte(v, 0) >= 0:
		return "value contains a NUL byte"
	}
	return ""
}

func trimNewlines(s string) string { return strings.TrimRight(s, "\r\n") }

func (s System) goos() string {
	if s.GOOS != "" {
		return s.GOOS
	}
	return runtime.GOOS
}

func (s System) run() Runner {
	if s.Run != nil {
		return s.Run
	}
	return execRunner
}

func (s System) getuid() int {
	if s.Getuid != nil {
		return s.Getuid()
	}
	return os.Getuid()
}

func (s System) lookupEnv(k string) (string, bool) {
	if s.LookupEnv != nil {
		return s.LookupEnv(k)
	}
	return os.LookupEnv(k)
}

func (s System) timeout() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	return DefaultTimeout
}

// execWaitDelay bounds how long Wait lingers on pipes a killed command's
// children still hold open.
const execWaitDelay = time.Second

// execRunner is the default Runner: no shell, stdin from the null device, the
// daemon's environment, and stdout capped just past MaxValueSize.
func execRunner(ctx context.Context, argv []string) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	stdout := &capped{limit: MaxValueSize + 1}
	stderr := &capped{limit: maxStderrReason * 4}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = execWaitDelay
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() >= 0 && ctx.Err() == nil {
		return stdout.buf.Bytes(), stderr.buf.Bytes(), exitErr.ExitCode(), nil
	}
	if err != nil {
		return nil, nil, -1, err
	}
	return stdout.buf.Bytes(), stderr.buf.Bytes(), 0, nil
}

// capped keeps the first limit bytes written and discards the rest, so the
// child never blocks on a full pipe.
type capped struct {
	buf   bytes.Buffer
	limit int
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.limit - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

// Describe is a resolver that does no I/O: it renders a reference as its
// descriptor, for dry runs and display surfaces.
type Describe struct{}

// Resolve returns "<secret DESCRIPTOR>".
func (Describe) Resolve(ref Ref) (string, error) {
	return "<secret " + ref.String() + ">", nil
}

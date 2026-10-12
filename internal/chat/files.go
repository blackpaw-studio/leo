package chat

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/blackpaw-studio/leo/internal/config"
)

// MaxFileBytes caps one attachment, uploaded or posted by an agent.
const MaxFileBytes = 50 << 20

// maxFilenameBytes caps a stored file name.
const maxFilenameBytes = 128

// ErrTooLarge reports an attachment over the cap.
var ErrTooLarge = errors.New("file exceeds the 50 MB attachment cap")

// SanitizeFilename reduces a client- or agent-supplied name to a safe base
// name: no directories, no control characters, no leading dots, at most
// maxFilenameBytes (the extension kept). Nothing usable left is "file".
func SanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, `\`, "/")
	name = name[strings.LastIndex(name, "/")+1:]
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return -1
		}
		return r
	}, name)
	name = strings.TrimLeft(strings.TrimSpace(name), ".")
	if name == "" {
		return "file"
	}
	if len(name) > maxFilenameBytes {
		ext := filepath.Ext(name)
		if len(ext) > 16 {
			ext = ""
		}
		stem := name[:len(name)-len(ext)]
		cut := maxFilenameBytes - len(ext)
		for cut > 0 && !utf8.RuneStart(stem[cut]) {
			cut--
		}
		name = stem[:cut] + ext
	}
	return name
}

var storeIDRe = regexp.MustCompile(`^[0-9A-Z]{1,32}$`)

// Files stores attachment files under Root/<agent>/<id>/<name>.
type Files struct {
	Root     string
	MaxBytes int64
}

func (f Files) dir(agent, id string) (string, error) {
	if !config.IsAgentName(agent) {
		return "", fmt.Errorf("invalid agent name %q", agent)
	}
	if !storeIDRe.MatchString(id) {
		return "", fmt.Errorf("invalid attachment id %q", id)
	}
	return filepath.Join(f.Root, agent, id), nil
}

func (f Files) max() int64 {
	if f.MaxBytes > 0 {
		return f.MaxBytes
	}
	return MaxFileBytes
}

// Save writes r as attachment id of agent under a sanitized name, refusing
// (and leaving nothing) past the cap.
func (f Files) Save(agent, id, name string, r io.Reader) (string, int64, error) {
	dir, err := f.dir(agent, id)
	if err != nil {
		return "", 0, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", 0, fmt.Errorf("creating attachment directory: %w", err)
	}
	path := filepath.Join(dir, SanitizeFilename(name))
	n, err := writeCapped(dir, path, r, f.max())
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", 0, err
	}
	return path, n, nil
}

// Copy copies the file at src (absolute; symlinks resolved; a regular file
// within the cap) into attachment id of agent, returning the stored path,
// its name and size.
func (f Files) Copy(agent, id, src string) (string, string, int64, error) {
	if !filepath.IsAbs(src) {
		return "", "", 0, fmt.Errorf("file path %q must be absolute", src)
	}
	resolved, err := filepath.EvalSymlinks(src)
	if err != nil {
		return "", "", 0, fmt.Errorf("resolving %s: %w", src, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", "", 0, fmt.Errorf("reading %s: %w", src, err)
	}
	if !info.Mode().IsRegular() {
		return "", "", 0, fmt.Errorf("%s is not a regular file", src)
	}
	if info.Size() > f.max() {
		return "", "", 0, fmt.Errorf("%s: %w", src, ErrTooLarge)
	}
	in, err := os.Open(resolved) // #nosec G304 -- the agent's own file, posted by it
	if err != nil {
		return "", "", 0, fmt.Errorf("opening %s: %w", src, err)
	}
	defer in.Close()
	name := SanitizeFilename(filepath.Base(resolved))
	path, n, err := f.Save(agent, id, name, in)
	if err != nil {
		return "", "", 0, fmt.Errorf("%s: %w", src, err)
	}
	return path, name, n, nil
}

// Remove deletes a stored file and its id directory.
func (f Files) Remove(path string) error {
	rel, err := filepath.Rel(f.Root, path)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return fmt.Errorf("%s is outside the attachment store", path)
	}
	return os.RemoveAll(filepath.Dir(path))
}

// writeCapped writes r to path through a temp file in dir, failing with
// ErrTooLarge once more than max bytes arrive.
func writeCapped(dir, path string, r io.Reader, max int64) (int64, error) {
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return 0, fmt.Errorf("creating attachment: %w", err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	n, err := io.Copy(tmp, io.LimitReader(r, max+1))
	if err != nil {
		_ = tmp.Close()
		return 0, fmt.Errorf("writing attachment: %w", err)
	}
	if n > max {
		_ = tmp.Close()
		return 0, ErrTooLarge
	}
	if err := tmp.Close(); err != nil {
		return 0, fmt.Errorf("writing attachment: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return 0, fmt.Errorf("storing attachment: %w", err)
	}
	committed = true
	return n, nil
}

// DetectMIME picks a file's media type: the declared one unless it is
// missing or generic, else by extension, else by sniffing head.
func DetectMIME(name, declared string, head []byte) string {
	if declared != "" && declared != "application/octet-stream" {
		if mt, _, err := mime.ParseMediaType(declared); err == nil {
			return mt
		}
	}
	ext := strings.ToLower(filepath.Ext(name))
	if known, ok := appMediaTypes[ext]; ok {
		return known
	}
	if byExt := mime.TypeByExtension(ext); byExt != "" {
		if mt, _, err := mime.ParseMediaType(byExt); err == nil {
			return mt
		}
	}
	if len(head) > 0 {
		if mt, _, err := mime.ParseMediaType(http.DetectContentType(head)); err == nil {
			return mt
		}
	}
	return "application/octet-stream"
}

// appMediaTypes pins the types of the formats the Leo apps send, so
// detection does not depend on the host's mime tables (Linux CI images
// often have none for m4a or heic).
var appMediaTypes = map[string]string{
	".m4a":  "audio/mp4",
	".aac":  "audio/aac",
	".mp3":  "audio/mpeg",
	".wav":  "audio/wav",
	".caf":  "audio/x-caf",
	".ogg":  "audio/ogg",
	".opus": "audio/opus",
	".heic": "image/heic",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".pdf":  "application/pdf",
}

// IsAudio reports whether a media type is audio; such uploads are voice.
func IsAudio(mt string) bool { return strings.HasPrefix(mt, "audio/") }

package agentmove

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// SessionTargets resolves, for each of m.SessionRels, the real directory the
// source rsyncs into. A store dir under the session root may be a symlink
// (a shared projects/ tree): its target is resolved here, missing dirs are
// created behind it, and nothing is ever replaced.
func (r *Receiver) SessionTargets(m Manifest) (SessionTargetsResponse, error) {
	if err := validateManifest(m); err != nil {
		return SessionTargetsResponse{}, err
	}
	h, err := harness.Get(orClaude(m.Agent.Harness))
	if err != nil {
		return SessionTargetsResponse{}, problemf(CodeHarnessUnsupported, "%v", err)
	}
	mover, ok := h.(harness.SessionMover)
	if !ok {
		return SessionTargetsResponse{}, problemf(CodeHarnessUnsupported, "%s agents cannot be moved", h.Name())
	}
	env, err := r.Agents.ArrivalEnv(receiveSpec(m, r.Clock.Now()))
	if err != nil {
		return SessionTargetsResponse{}, problemf(CodeEnvironmentMissing, "the agent's env cannot be resolved here: %v", err)
	}
	root := mover.SessionRoot(env)
	if root == "" {
		return SessionTargetsResponse{}, problemf(CodeBadRequest, "%s keeps no session files to place", h.Name())
	}
	var out SessionTargetsResponse
	for _, rel := range m.SessionRels {
		dir, err := ResolveSessionDir(root, rel, true)
		if err != nil {
			return SessionTargetsResponse{}, err
		}
		out.Targets = append(out.Targets, dir)
	}
	return out, nil
}

// ResolveSessionDir is the real directory a session path under root lives
// in: rel itself when it ends in "/", else its parent. Symlinks along the way
// are resolved (on the deepest existing ancestor); with create, the missing
// leaf dirs are made under the resolved root.
func ResolveSessionDir(root, rel string, create bool) (string, error) {
	if err := checkSessionRel(rel); err != nil {
		return "", err
	}
	dir := filepath.Join(root, rel)
	if !strings.HasSuffix(rel, "/") {
		dir = filepath.Dir(dir)
	}
	real, err := evalDeepest(dir)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", dir, err)
	}
	if create {
		if err := os.MkdirAll(real, 0o750); err != nil {
			return "", fmt.Errorf("preparing %s: %w", real, err)
		}
	}
	if err := ValidatePath(real); err != nil {
		return "", err
	}
	return real, nil
}

// StoreSymlinks lists the symlinks inside the destination store paths rels
// write into under root, relative to root and sorted: everything under a
// directory rel (links are not followed), or a file rel itself. rsync
// replaces such a link with whatever the source sends in its place, even
// with -u, so the source refuses before it stops anything. Nothing is
// created; a store path that does not exist yet holds none.
func StoreSymlinks(root string, rels []string) ([]string, error) {
	var out []string
	for _, rel := range rels {
		dir, err := ResolveSessionDir(root, rel, false)
		if err != nil {
			return nil, err
		}
		if !strings.HasSuffix(rel, "/") {
			if isSymlink(filepath.Join(dir, filepath.Base(rel))) {
				out = append(out, filepath.Clean(rel))
			}
			continue
		}
		err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			switch {
			case errors.Is(err, fs.ErrNotExist):
				return nil
			case err != nil:
				return err
			case d.Type()&fs.ModeSymlink != 0:
				sub, err := filepath.Rel(dir, p)
				if err != nil {
					return err
				}
				out = append(out, filepath.Join(rel, sub))
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("listing %s: %w", dir, err)
		}
	}
	slices.Sort(out)
	return out, nil
}

func isSymlink(p string) bool {
	info, err := os.Lstat(p)
	return err == nil && info.Mode()&os.ModeSymlink != 0
}

// checkSessionRel refuses a session path that could leave its root or pass
// for an rsync option.
func checkSessionRel(rel string) error {
	if rel == "" || strings.HasPrefix(rel, "-") || filepath.IsAbs(rel) {
		return problemf(CodeUnsafePath, "session path %q must be relative to the session root", rel)
	}
	for _, c := range strings.Split(rel, "/") {
		if c == ".." {
			return problemf(CodeUnsafePath, "session path %q has a .. component", rel)
		}
	}
	return nil
}

// evalDeepest is filepath.EvalSymlinks of path, where the trailing
// components that do not exist yet are kept as they are under the resolved
// deepest ancestor that does. A dangling symlink is an error: creating
// through it would replace it.
func evalDeepest(path string) (string, error) {
	var rest []string
	cur := filepath.Clean(path)
	for {
		real, err := filepath.EvalSymlinks(cur)
		if err == nil {
			for i := len(rest) - 1; i >= 0; i-- {
				real = filepath.Join(real, rest[i])
			}
			return real, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		if info, lerr := os.Lstat(cur); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%s is a dangling symlink", cur)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", err
		}
		rest = append(rest, filepath.Base(cur))
		cur = parent
	}
}

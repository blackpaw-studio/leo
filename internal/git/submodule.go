package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Submodule states, from <path>/.git.
const (
	// SubmoduleAbsorbed: .git is a file pointing into the superproject's
	// modules dir.
	SubmoduleAbsorbed = "absorbed"
	// SubmoduleInline: .git is a directory (a submodule never absorbed, or
	// an embedded repo). It travels inside the workspace.
	SubmoduleInline = "inline"
	// SubmoduleUninit: no .git at all.
	SubmoduleUninit = "uninit"
)

// gitlinkMode is the index mode of a submodule entry.
const gitlinkMode = "160000"

// Submodule is one gitlink of a repository, found recursively.
type Submodule struct {
	// Path is relative to the scanned top-level dir.
	Path string `json:"path"`
	// Name is its .gitmodules name, "" for an embedded repo.
	Name string `json:"name"`
	// Parent is the Path of the submodule holding this one, "" at top level.
	Parent string `json:"parent,omitempty"`
	State  string `json:"state"`
	// GitDir is the absolute git dir of an absorbed submodule.
	GitDir  string `json:"gitdir,omitempty"`
	HeadSHA string `json:"head_sha,omitempty"`
	// HeadRef is the full branch ref, "" when detached.
	HeadRef string `json:"head_ref,omitempty"`
	URL     string `json:"url,omitempty"`
	Active  string `json:"active,omitempty"`
}

// Submodules scans dir's gitlinks (index entries of mode 160000, not
// .gitmodules) recursively through absorbed submodules. Children come
// before their parent, so relinking in order goes deepest first.
func Submodules(ctx context.Context, dir string) ([]Submodule, error) {
	return scanSubmodules(ctx, dir, "")
}

func scanSubmodules(ctx context.Context, dir, prefix string) ([]Submodule, error) {
	paths, err := gitlinks(ctx, dir)
	if err != nil || len(paths) == 0 {
		return nil, err
	}
	names, err := submoduleNames(ctx, dir)
	if err != nil {
		return nil, err
	}
	var out []Submodule
	for _, p := range paths {
		s := Submodule{Path: prefix + p, Name: names[p], Parent: strings.TrimSuffix(prefix, "/")}
		subDir := filepath.Join(dir, p)
		info, err := os.Lstat(filepath.Join(subDir, ".git"))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			s.State = SubmoduleUninit
		case err != nil:
			return nil, err
		case info.IsDir():
			s.State = SubmoduleInline
		default:
			s.State = SubmoduleAbsorbed
			if s.GitDir, err = AbsoluteGitDir(ctx, subDir); err != nil {
				return nil, err
			}
			children, err := scanSubmodules(ctx, subDir, s.Path+"/")
			if err != nil {
				return nil, err
			}
			out = append(out, children...)
		}
		if s.State != SubmoduleUninit {
			head, err := HeadState(ctx, subDir)
			if err != nil {
				return nil, fmt.Errorf("submodule %s: %w", s.Path, err)
			}
			s.HeadSHA, s.HeadRef = head.SHA, head.Ref
		}
		if s.Name != "" {
			s.URL = configGet(ctx, dir, "submodule."+s.Name+".url")
			s.Active = configGet(ctx, dir, "submodule."+s.Name+".active")
		}
		out = append(out, s)
	}
	return out, nil
}

// gitlinks lists dir's submodule paths from the index.
func gitlinks(ctx context.Context, dir string) ([]string, error) {
	out, err := ExecGit(ctx, dir, "ls-files", "--stage", "-z")
	if err != nil {
		return nil, fmt.Errorf("git ls-files --stage: %w: %s", err, strings.TrimSpace(string(out)))
	}
	var paths []string
	for _, entry := range bytes.Split(out, []byte{0}) {
		meta, path, ok := bytes.Cut(entry, []byte{'\t'})
		if ok && bytes.HasPrefix(meta, []byte(gitlinkMode+" ")) {
			paths = append(paths, string(path))
		}
	}
	return paths, nil
}

// submoduleNames maps each .gitmodules path to its submodule name.
func submoduleNames(ctx context.Context, dir string) (map[string]string, error) {
	names := map[string]string{}
	gm := filepath.Join(dir, ".gitmodules")
	if _, err := os.Stat(gm); errors.Is(err, fs.ErrNotExist) {
		return names, nil
	}
	out, err := ExecGit(ctx, dir, "config", "--file", gm, "-z", "--get-regexp", `^submodule\..*\.path$`)
	if err != nil {
		if exitCode(err) == 1 { // no entries
			return names, nil
		}
		return nil, fmt.Errorf("reading .gitmodules: %w: %s", err, strings.TrimSpace(string(out)))
	}
	for _, entry := range bytes.Split(out, []byte{0}) {
		key, val, ok := bytes.Cut(entry, []byte{'\n'})
		if !ok {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(string(key), "submodule."), ".path")
		names[string(val)] = name
	}
	return names, nil
}

// configGet is `config --get key` in dir, "" when unset or on error.
func configGet(ctx context.Context, dir, key string) string {
	out, err := ExecGit(ctx, dir, "config", "--get", key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ModulesDir is dir's private submodule git dir root: <common>/modules in
// the main worktree, <common>/worktrees/<id>/modules in a linked one.
func ModulesDir(ctx context.Context, dir string) (string, error) {
	return GitPath(ctx, dir, "modules")
}

// RelinkSubmodule points the submodule at <ws>/<path> at gitDir: it writes
// <path>/.git (temp file + rename) and core.worktree as relative paths, then
// refreshes the submodule's index. Both pointers encode the worktree's depth
// and admin id, so a move between worktrees rewrites them.
func RelinkSubmodule(ctx context.Context, ws, path, gitDir string) error {
	subWT := filepath.Join(ws, path)
	toGitDir, err := filepath.Rel(resolve(subWT), resolve(gitDir))
	if err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(subWT, ".git"), "gitdir: "+toGitDir+"\n"); err != nil {
		return fmt.Errorf("submodule %s: writing .git: %w", path, err)
	}
	toWT, err := filepath.Rel(resolve(gitDir), resolve(subWT))
	if err != nil {
		return err
	}
	// --file, never --git-dir: git would chdir to the stale core.worktree
	// first and die.
	if _, err := gitOut(ctx, ws, "config", "--file", filepath.Join(gitDir, "config"), "core.worktree", toWT); err != nil {
		return fmt.Errorf("submodule %s: %w", path, err)
	}
	return RefreshIndex(ctx, subWT)
}

func writeFileAtomic(path, content string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".git.leo-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ForkSubmoduleBranch creates branch at sha (refusing an existing one) and
// checks it out by pointing HEAD at it; files and index are untouched.
func ForkSubmoduleBranch(ctx context.Context, dir, branch, sha string) error {
	ref := "refs/heads/" + branch
	if err := UpdateRefCAS(ctx, dir, ref, sha, ""); err != nil {
		return err
	}
	return SetHEAD(ctx, dir, ref, "")
}

// SetConfigIfUnset sets key in dir's config only when it has no value yet,
// reporting whether it did.
func SetConfigIfUnset(ctx context.Context, dir, key, value string) (bool, error) {
	if configGet(ctx, dir, key) != "" {
		return false, nil
	}
	if _, err := gitOut(ctx, dir, "config", key, value); err != nil {
		return false, err
	}
	return true, nil
}

// ConfigGet is `config --get key` in dir, "" when unset.
func ConfigGet(ctx context.Context, dir, key string) string { return configGet(ctx, dir, key) }

// VerifyHead checks dir's HEAD resolves to a commit.
func VerifyHead(ctx context.Context, dir string) error {
	_, err := gitOut(ctx, dir, "rev-parse", "--verify", "-q", "HEAD^{commit}")
	return err
}

// FsckConnectivity checks every object reachable in dir's repository is
// present.
func FsckConnectivity(ctx context.Context, dir string) error {
	_, err := gitOut(ctx, dir, "fsck", "--connectivity-only", "--no-dangling", "--no-progress")
	return err
}

package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ErrConfigNotFound is returned by FindConfig when no leo.yaml exists.
var ErrConfigNotFound = errors.New("leo.yaml not found")

// CreateIfAbsent writes cfg to path unless a file is already there, and
// reports whether it did. The file is 0600 and a missing parent directory is
// created 0700. The write is atomic and never replaces an existing file: the
// content goes to a temp file that is hard-linked into place, and link(2)
// fails with EEXIST when path appeared concurrently, in which case the
// existing file wins and created is false.
func CreateIfAbsent(path string, cfg *Config) (created bool, err error) {
	if _, statErr := os.Lstat(path); statErr == nil {
		return false, nil
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return false, fmt.Errorf("marshaling config: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return false, fmt.Errorf("creating config directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".leo.yaml.*")
	if err != nil {
		return false, fmt.Errorf("creating temp config: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := writeSynced(tmp, data); err != nil {
		return false, err
	}
	if err := os.Link(tmp.Name(), path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, fmt.Errorf("installing config: %w", err)
	}
	return true, nil
}

// writeSynced writes data to f (0600 from CreateTemp), flushes and closes it.
func writeSynced(f *os.File, data []byte) error {
	_, werr := f.Write(data)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return fmt.Errorf("writing temp config: %w", werr)
	}
	return nil
}

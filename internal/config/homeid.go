package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// homeIDFile holds a leo home's random identity under its state dir.
const homeIDFile = "home-id"

const homeIDBytes = 16

// HomeID returns this leo home's identity: 16 random bytes in hex, kept in
// <statePath>/home-id (mode 0600) and created on first read. A follower
// compares it with the lead's to refuse following itself through an alias
// Validate cannot see (a hostname that resolves back to this machine).
// Concurrent first reads agree: the file is published with a hard link,
// which fails rather than replaces when another caller won.
func HomeID(statePath string) (string, error) {
	path := filepath.Join(statePath, homeIDFile)
	id, err := readHomeID(path)
	if !errors.Is(err, fs.ErrNotExist) {
		return id, err
	}
	if err := os.MkdirAll(statePath, 0o700); err != nil {
		return "", fmt.Errorf("creating state dir: %w", err)
	}
	raw := make([]byte, homeIDBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating home id: %w", err)
	}
	id = hex.EncodeToString(raw)
	if err := publishHomeID(statePath, path, id); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return readHomeID(path)
		}
		return "", err
	}
	return id, nil
}

func readHomeID(path string) (string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // under the leo state dir
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(data))
	if b, err := hex.DecodeString(id); err != nil || len(b) != homeIDBytes {
		return "", fmt.Errorf("%s is corrupt (want %d hex bytes); remove it to mint a new id", path, homeIDBytes)
	}
	return id, nil
}

// publishHomeID writes id to a private temp file and hard-links it into
// place, so the file appears complete or not at all and never replaces one.
func publishHomeID(dir, path, id string) error {
	tmp, err := os.CreateTemp(dir, ".home-id.*")
	if err != nil {
		return fmt.Errorf("creating home id: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("creating home id: %w", err)
	}
	if err := writeSynced(tmp, []byte(id+"\n")); err != nil {
		return err
	}
	if err := os.Link(tmp.Name(), path); err != nil {
		return err
	}
	return syncDir(dir)
}

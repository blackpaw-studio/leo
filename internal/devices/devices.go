// Package devices pairs phones (the Leo iOS app) with this daemon and keeps
// the per-device bearer tokens they authenticate with.
//
// State lives under <state>/devices/, shared between the daemon (which
// redeems pairing codes and authenticates requests) and the CLI (which mints
// codes and revokes devices) in separate processes:
//
//   - devices.json — paired devices. Tokens are stored as SHA-256 hashes;
//     they are 256 random bits, so a fast hash is enough to make a leaked
//     file useless without making verification slow.
//   - pairing.json — outstanding pairing codes, also hashed, each with an
//     expiry.
//
// Every read-modify-write holds an exclusive flock on <dir>/lock; files are
// replaced by atomic rename, so readers never need the lock.
package devices

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

// PairingCodeTTL is how long a pairing code from `leo pair` stays redeemable.
const PairingCodeTTL = 5 * time.Minute

const (
	dirName      = "devices"
	devicesFile  = "devices.json"
	pairingFile  = "pairing.json"
	lockFile     = "lock"
	daemonIDFile = "daemon.id"

	dirMode  os.FileMode = 0o700
	fileMode os.FileMode = 0o600

	// pairingCodeBytes is 160 bits: unguessable even before the pairing
	// route's rate limit.
	pairingCodeBytes = 20
	tokenBytes       = 32
	deviceIDBytes    = 8

	maxNameRunes = 64
	maxGrantLen  = 2048
	pushKeyBytes = 32
)

var (
	// ErrInvalidCode covers unknown, expired and already-used pairing codes
	// alike, so a caller learns nothing from which one it hit.
	ErrInvalidCode = errors.New("invalid or expired pairing code")
	// ErrNotFound is returned for an unknown device id.
	ErrNotFound = errors.New("device not found")
	// ErrInvalidName rejects an empty, overlong or control-character name.
	ErrInvalidName = fmt.Errorf("device name must be 1-%d printable characters", maxNameRunes)
)

// codeEncoding is unpadded base32: case-insensitive-friendly and QR-compact.
var codeEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// Device is one paired device. TokenHash is hex SHA-256 of its bearer token.
type Device struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	TokenHash string            `json:"token_hash"`
	CreatedAt time.Time         `json:"created_at"`
	Push      *PushRegistration `json:"push,omitempty"`
}

// PushRegistration is what the app hands the daemon for push delivery: the
// relay grant and the device's AES-256 key (base64). Sending is not wired
// yet; the daemon only stores it.
type PushRegistration struct {
	Grant     string    `json:"grant"`
	Key       string    `json:"key"`
	UpdatedAt time.Time `json:"updated_at"`
}

type devicesDoc struct {
	Devices []Device `json:"devices"`
}

type pendingCode struct {
	Hash      string    `json:"hash"`
	ExpiresAt time.Time `json:"expires_at"`
}

type pairingDoc struct {
	Codes []pendingCode `json:"codes"`
}

// Store reads and writes the device state under one state directory. Safe
// for concurrent use within a process and across processes.
type Store struct {
	dir string
	now func() time.Time

	// cache backs Authenticate, which runs on every API request: the file
	// is re-read only when its identity, size or mtime changes, so a revoke
	// from the CLI takes effect on the daemon's next request.
	mu        sync.Mutex
	cacheInfo os.FileInfo
	cache     []Device
}

// NewStore returns a store rooted at <stateDir>/devices. now defaults to
// time.Now.
func NewStore(stateDir string, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{dir: filepath.Join(stateDir, dirName), now: now}
}

// NewPairingCode mints a single-use code valid for PairingCodeTTL.
func (s *Store) NewPairingCode() (code string, expiresAt time.Time, err error) {
	raw, err := randomBytes(pairingCodeBytes)
	if err != nil {
		return "", time.Time{}, err
	}
	code = codeEncoding.EncodeToString(raw)
	expiresAt = s.now().Add(PairingCodeTTL)
	err = s.withLock(func() error {
		doc, err := s.loadPairing()
		if err != nil {
			return err
		}
		doc.Codes = append(s.unexpired(doc.Codes), pendingCode{Hash: hashSecret(code), ExpiresAt: expiresAt})
		return writeJSON(filepath.Join(s.dir, pairingFile), doc)
	})
	if err != nil {
		return "", time.Time{}, err
	}
	return code, expiresAt, nil
}

// Pair redeems code for a new device named name, returning the device and
// its bearer token. The token is returned only here; the store keeps its
// hash. The code is consumed on success; a bad name leaves it redeemable.
func (s *Store) Pair(code, name string) (Device, string, error) {
	name = strings.TrimSpace(name)
	if !validName(name) {
		return Device{}, "", ErrInvalidName
	}
	var dev Device
	var token string
	err := s.withLock(func() error {
		pending, err := s.loadPairing()
		if err != nil {
			return err
		}
		live := s.unexpired(pending.Codes)
		idx := matchHash(hashSecret(code), live, func(c pendingCode) string { return c.Hash })
		if idx < 0 {
			if len(live) != len(pending.Codes) {
				_ = writeJSON(filepath.Join(s.dir, pairingFile), pairingDoc{Codes: live})
			}
			return ErrInvalidCode
		}
		// Consume the code before issuing anything, so a failure past this
		// point can never leave it redeemable twice.
		if err := writeJSON(filepath.Join(s.dir, pairingFile), pairingDoc{Codes: slices.Delete(live, idx, idx+1)}); err != nil {
			return err
		}
		dev, token, err = s.addDevice(name)
		return err
	})
	if err != nil {
		return Device{}, "", err
	}
	return dev, token, nil
}

// addDevice creates and persists a device. Callers hold the lock.
func (s *Store) addDevice(name string) (Device, string, error) {
	idRaw, err := randomBytes(deviceIDBytes)
	if err != nil {
		return Device{}, "", err
	}
	tokRaw, err := randomBytes(tokenBytes)
	if err != nil {
		return Device{}, "", err
	}
	token := hex.EncodeToString(tokRaw)
	dev := Device{ID: hex.EncodeToString(idRaw), Name: name, TokenHash: hashSecret(token), CreatedAt: s.now().UTC()}
	doc, err := s.loadDevices()
	if err != nil {
		return Device{}, "", err
	}
	doc.Devices = append(doc.Devices, dev)
	if err := writeJSON(filepath.Join(s.dir, devicesFile), doc); err != nil {
		return Device{}, "", err
	}
	return dev, token, nil
}

// Authenticate reports which device token belongs to, comparing hashes in
// constant time against every device.
func (s *Store) Authenticate(token string) (Device, bool) {
	if token == "" {
		return Device{}, false
	}
	devices, err := s.cached()
	if err != nil {
		return Device{}, false
	}
	idx := matchHash(hashSecret(token), devices, func(d Device) string { return d.TokenHash })
	if idx < 0 {
		return Device{}, false
	}
	return devices[idx], true
}

// List returns every paired device in pairing order.
func (s *Store) List() ([]Device, error) {
	doc, err := s.loadDevices()
	if err != nil {
		return nil, err
	}
	return doc.Devices, nil
}

// Revoke removes device id; its token stops authenticating immediately.
func (s *Store) Revoke(id string) error {
	return s.updateDevices(func(devices []Device) ([]Device, error) {
		idx := slices.IndexFunc(devices, func(d Device) bool { return d.ID == id })
		if idx < 0 {
			return nil, ErrNotFound
		}
		return slices.Delete(devices, idx, idx+1), nil
	})
}

// SetPush replaces device id's push registration.
func (s *Store) SetPush(id string, reg PushRegistration) error {
	if err := ValidatePush(reg); err != nil {
		return err
	}
	reg.UpdatedAt = s.now().UTC()
	return s.updateDevices(func(devices []Device) ([]Device, error) {
		idx := slices.IndexFunc(devices, func(d Device) bool { return d.ID == id })
		if idx < 0 {
			return nil, ErrNotFound
		}
		out := slices.Clone(devices)
		out[idx].Push = &reg
		return out, nil
	})
}

// ValidatePush checks a registration's shape: a non-empty bounded grant and
// a base64 256-bit key.
func ValidatePush(reg PushRegistration) error {
	if reg.Grant == "" || len(reg.Grant) > maxGrantLen {
		return fmt.Errorf("grant must be 1-%d bytes", maxGrantLen)
	}
	key, err := base64.StdEncoding.DecodeString(reg.Key)
	if err != nil || len(key) != pushKeyBytes {
		return fmt.Errorf("key must be %d bytes, base64-encoded", pushKeyBytes)
	}
	return nil
}

func (s *Store) updateDevices(fn func([]Device) ([]Device, error)) error {
	return s.withLock(func() error {
		doc, err := s.loadDevices()
		if err != nil {
			return err
		}
		next, err := fn(doc.Devices)
		if err != nil {
			return err
		}
		return writeJSON(filepath.Join(s.dir, devicesFile), devicesDoc{Devices: next})
	})
}

// cached returns the device list, re-reading devices.json only when it has
// changed since the last read.
func (s *Store) cached() ([]Device, error) {
	path := filepath.Join(s.dir, devicesFile)
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cacheInfo != nil && os.SameFile(s.cacheInfo, info) &&
		s.cacheInfo.Size() == info.Size() && s.cacheInfo.ModTime().Equal(info.ModTime()) {
		return s.cache, nil
	}
	doc, err := s.loadDevices()
	if err != nil {
		return nil, err
	}
	s.cacheInfo, s.cache = info, doc.Devices
	return s.cache, nil
}

func (s *Store) unexpired(codes []pendingCode) []pendingCode {
	now := s.now()
	return slices.DeleteFunc(slices.Clone(codes), func(c pendingCode) bool { return !now.Before(c.ExpiresAt) })
}

func (s *Store) loadDevices() (devicesDoc, error) {
	var doc devicesDoc
	return doc, readJSON(filepath.Join(s.dir, devicesFile), &doc)
}

func (s *Store) loadPairing() (pairingDoc, error) {
	var doc pairingDoc
	return doc, readJSON(filepath.Join(s.dir, pairingFile), &doc)
}

// withLock runs fn holding an exclusive flock on the store's lock file.
func (s *Store) withLock(fn func() error) error {
	if err := os.MkdirAll(s.dir, dirMode); err != nil {
		return fmt.Errorf("devices: creating %s: %w", s.dir, err)
	}
	f, err := os.OpenFile(filepath.Join(s.dir, lockFile), os.O_CREATE|os.O_RDWR, fileMode)
	if err != nil {
		return fmt.Errorf("devices: opening lock: %w", err)
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("devices: locking: %w", err)
	}
	defer unix.Flock(int(f.Fd()), unix.LOCK_UN) //nolint:errcheck
	return fn()
}

// matchHash returns the index of the item whose hash equals want, comparing
// against every item in constant time, or -1.
func matchHash[T any](want string, items []T, hashOf func(T) string) int {
	found := -1
	for i, item := range items {
		if subtle.ConstantTimeCompare([]byte(want), []byte(hashOf(item))) == 1 {
			found = i
		}
	}
	return found
}

// EnsureDaemonID returns this daemon's stable random id from
// <stateDir>/daemon.id, creating it on first use. The app keys its paired
// daemons on it.
func EnsureDaemonID(stateDir string) (string, error) {
	path := filepath.Join(stateDir, daemonIDFile)
	if b, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(b)); id != "" {
			return id, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("devices: reading daemon id: %w", err)
	}
	raw, err := randomBytes(16)
	if err != nil {
		return "", err
	}
	id := hex.EncodeToString(raw)
	if err := os.MkdirAll(stateDir, dirMode); err != nil {
		return "", fmt.Errorf("devices: creating %s: %w", stateDir, err)
	}
	if err := writeFileAtomic(path, []byte(id+"\n")); err != nil {
		return "", err
	}
	return id, nil
}

func validName(name string) bool {
	if name == "" || utf8.RuneCountInString(name) > maxNameRunes {
		return false
	}
	return !strings.ContainsFunc(name, func(r rune) bool { return !unicode.IsPrint(r) })
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("devices: generating random bytes: %w", err)
	}
	return b, nil
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("devices: reading %s: %w", path, err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("devices: parsing %s: %w", path, err)
	}
	return nil
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("devices: encoding %s: %w", path, err)
	}
	return writeFileAtomic(path, append(b, '\n'))
}

// writeFileAtomic writes data to a 0600 temp file beside path and renames it
// into place.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("devices: creating temp file: %w", err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("devices: writing %s: %w", path, err)
	}
	if err := tmp.Chmod(fileMode); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("devices: chmod %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("devices: closing %s: %w", path, err)
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("devices: replacing %s: %w", path, err)
	}
	return nil
}

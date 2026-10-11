package web

import (
	"errors"
	"math"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/blackpaw-studio/leo/internal/devices"
)

// Pairing rate limits. Codes carry 160 random bits, so these are defense in
// depth, not the main barrier: they cap how fast anyone can even try, per
// peer and across all peers (behind a tunnel every request shares one peer
// address, so the global cap is the one that binds there).
const (
	pairWindow       = time.Minute
	pairPerPeerLimit = 5
	pairGlobalLimit  = 20
	// pairPeerTableMax bounds the per-peer table; when it is full, expired
	// windows are swept, and a still-full table refuses new peers.
	pairPeerTableMax = 1024
	maxPairBody      = 4 << 10
	maxPushBody      = 8 << 10
)

// fixedWindow counts attempts in the window starting at start.
type fixedWindow struct {
	start time.Time
	count int
}

// roll starts a fresh window once the current one has elapsed.
func (w *fixedWindow) roll(now time.Time) {
	if now.Sub(w.start) >= pairWindow {
		*w = fixedWindow{start: now}
	}
}

func (w *fixedWindow) retryAfter(now time.Time) time.Duration {
	return w.start.Add(pairWindow).Sub(now)
}

// pairLimiter rate-limits POST /api/v1/pair per peer address and globally.
// Every attempt counts, successful or not.
type pairLimiter struct {
	mu     sync.Mutex
	global fixedWindow
	peers  map[string]*fixedWindow
}

func newPairLimiter() *pairLimiter {
	return &pairLimiter{peers: make(map[string]*fixedWindow)}
}

// allow records an attempt from peer at now, reporting whether it may
// proceed and, if not, how long until it may retry.
//
// The global budget is checked before a peer is added to the table, and a
// full table refuses new peers, so rotating source addresses (IPv6) can
// neither grow the table without bound nor make the sweep quadratic. A peer
// over its own limit does not spend the global budget.
func (l *pairLimiter) allow(peer string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.global.roll(now)
	if l.global.count >= pairGlobalLimit {
		return false, l.global.retryAfter(now)
	}
	w := l.peers[peer]
	if w == nil {
		if len(l.peers) >= pairPeerTableMax {
			l.sweep(now)
		}
		if len(l.peers) >= pairPeerTableMax {
			return false, pairWindow
		}
		w = &fixedWindow{start: now}
		l.peers[peer] = w
	}
	w.roll(now)
	if w.count >= pairPerPeerLimit {
		return false, w.retryAfter(now)
	}
	w.count++
	l.global.count++
	return true, 0
}

// sweep drops peers whose window has elapsed.
func (l *pairLimiter) sweep(now time.Time) {
	for k, w := range l.peers {
		if now.Sub(w.start) >= pairWindow {
			delete(l.peers, k)
		}
	}
}

type pairRequest struct {
	PairingCode string `json:"pairing_code"`
	DeviceName  string `json:"device_name"`
}

// handleAPIPair redeems a pairing code from `leo pair` for a device token.
// Unauthenticated by design — the code is the credential — so it is rate
// limited before the code is examined.
// POST /api/v1/pair {pairing_code, device_name} -> {device_id, token}
func (s *Server) handleAPIPair(w http.ResponseWriter, r *http.Request) {
	if s.devices == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiResponse{Error: "device pairing is not available on this daemon"})
		return
	}
	if ok, wait := s.pairLimiter.allow(peerHost(r), s.now()); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		writeJSON(w, http.StatusTooManyRequests, apiResponse{Error: "too many pairing attempts; try again later"})
		return
	}
	var req pairRequest
	if !decodeControlBody(w, r, maxPairBody, false, &req) {
		return
	}
	dev, token, err := s.devices.Pair(req.PairingCode, req.DeviceName)
	switch {
	case errors.Is(err, devices.ErrInvalidCode):
		writeJSON(w, http.StatusUnauthorized, apiResponse{Error: err.Error()})
		return
	case errors.Is(err, devices.ErrInvalidName):
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: "pairing failed"})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: map[string]string{"device_id": dev.ID, "token": token}})
}

type pushRequest struct {
	Grant string `json:"grant"`
	Key   string `json:"key"`
}

// handleAPIDevicePush stores a device's push registration, replacing any
// previous one. Only the device itself may write it: the operator and agent
// tokens are refused, as is any other device's.
// PUT /api/v1/devices/{id}/push {grant, key}
func (s *Server) handleAPIDevicePush(w http.ResponseWriter, r *http.Request) {
	dev, ok := s.authenticateDevice(r)
	if !ok || dev.ID != r.PathValue("id") {
		writeJSON(w, http.StatusForbidden, apiResponse{Error: "a device may only register its own push credentials"})
		return
	}
	var req pushRequest
	if !decodeControlBody(w, r, maxPushBody, false, &req) {
		return
	}
	reg := devices.PushRegistration{Grant: req.Grant, Key: req.Key}
	if err := devices.ValidatePush(reg); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	switch err := s.devices.SetPush(dev.ID, reg); {
	case errors.Is(err, devices.ErrNotFound):
		writeJSON(w, http.StatusNotFound, apiResponse{Error: err.Error()})
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: "storing push registration failed"})
	default:
		writeJSON(w, http.StatusOK, apiResponse{OK: true})
	}
}

// authenticateDevice resolves r's bearer to a paired device.
func (s *Server) authenticateDevice(r *http.Request) (devices.Device, bool) {
	if s.devices == nil {
		return devices.Device{}, false
	}
	return s.devices.Authenticate(extractBearer(r.Header.Get("Authorization")))
}

// isDeviceToken reports whether tok belongs to a paired device.
func (s *Server) isDeviceToken(tok string) bool {
	if s.devices == nil {
		return false
	}
	_, ok := s.devices.Authenticate(tok)
	return ok
}

// isOperatorBearer reports whether tok carries operator privileges: the
// operator's api.token or a paired device's token.
func (s *Server) isOperatorBearer(tok string) bool {
	// Evaluate both so timing does not reveal which kind matched.
	isAPI := matchesAnyToken(tok, []string{s.apiToken})
	isDevice := s.isDeviceToken(tok)
	return isAPI || isDevice
}

// peerHost is r's remote address without the port.
func peerHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

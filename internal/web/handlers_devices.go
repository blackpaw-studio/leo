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
	// pairPeerTableMax bounds the per-peer table; past it, expired windows
	// are swept before a new peer is added.
	pairPeerTableMax = 1024
	maxPairBody      = 4 << 10
	maxPushBody      = 8 << 10
)

// fixedWindow counts attempts in the window starting at start.
type fixedWindow struct {
	start time.Time
	count int
}

func (w *fixedWindow) take(now time.Time, limit int) (bool, time.Duration) {
	if now.Sub(w.start) >= pairWindow {
		*w = fixedWindow{start: now}
	}
	if w.count >= limit {
		return false, w.start.Add(pairWindow).Sub(now)
	}
	w.count++
	return true, 0
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
func (l *pairLimiter) allow(peer string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.peers[peer]
	if w == nil {
		l.sweep(now)
		w = &fixedWindow{start: now}
		l.peers[peer] = w
	}
	// Check the peer first so one noisy peer burns only its own budget.
	if ok, wait := w.take(now, pairPerPeerLimit); !ok {
		return false, wait
	}
	return l.global.take(now, pairGlobalLimit)
}

func (l *pairLimiter) sweep(now time.Time) {
	if len(l.peers) < pairPeerTableMax {
		return
	}
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

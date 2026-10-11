package push

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// attemptTimeout bounds each POST to the relay.
	attemptTimeout = 10 * time.Second
	// retryBackoff is the pause before the single retry.
	retryBackoff = time.Second
	// The relay caps ciphertext at 2 KiB of base64; these caps keep the
	// worst case (4-byte runes) well under it.
	maxAgentRunes = 64
	maxTitleRunes = 160
	// maxResponseDrain bounds how much of a relay response is read so the
	// connection can be reused.
	maxResponseDrain = 4 << 10
)

// Alert is one "an agent needs you" event, before encryption.
type Alert struct {
	Agent string
	// Kind is the daemon's attention state string.
	Kind string
	// Title is the human summary shown as the notification body.
	Title string
	At    time.Time
}

// Sender delivers an Alert to every registered device. Implementations must
// honor ctx cancellation and never panic on bad registrations.
type Sender interface {
	Send(ctx context.Context, a Alert)
}

// payload is the encrypted plaintext (contract v1).
type payload struct {
	V        int    `json:"v"`
	DaemonID string `json:"daemon_id"`
	Agent    string `json:"agent"`
	Kind     string `json:"kind"`
	Title    string `json:"title"`
	TS       int64  `json:"ts"`
}

type relayRequest struct {
	Grant      string `json:"grant"`
	Ciphertext string `json:"ciphertext"`
	CollapseID string `json:"collapse_id"`
}

// outcome classifies one relay attempt.
type outcome int

const (
	outcomeDelivered outcome = iota
	outcomeRetry             // 5xx or network error
	outcomeGone              // 410: the registration is dead
	outcomeRejected          // any other non-2xx: not retried
)

// RelaySender is the Sender that encrypts per device and POSTs to each
// device's relay. It is safe for concurrent use.
type RelaySender struct {
	registry Registry
	daemonID string
	client   *http.Client
	now      func() time.Time
	sleep    func(context.Context, time.Duration) error
	logger   *log.Logger
}

// RelayOption configures a RelaySender.
type RelayOption func(*RelaySender)

// WithHTTPClient sets the client used for relay requests (redirects are
// still refused).
func WithHTTPClient(c *http.Client) RelayOption {
	return func(s *RelaySender) { s.client = c }
}

// WithTransport sets the client's transport.
func WithTransport(rt http.RoundTripper) RelayOption {
	return func(s *RelaySender) { s.client = &http.Client{Transport: rt} }
}

// WithClock sets the clock behind the payload's ts.
func WithClock(now func() time.Time) RelayOption {
	return func(s *RelaySender) { s.now = now }
}

// WithSleep sets the pause between the attempt and its retry.
func WithSleep(sleep func(context.Context, time.Duration) error) RelayOption {
	return func(s *RelaySender) { s.sleep = sleep }
}

// WithLogger sets the logger. It only ever receives device ids, statuses and
// transport error kinds — never keys, grants or alert content.
func WithLogger(l *log.Logger) RelayOption {
	return func(s *RelaySender) { s.logger = l }
}

// NewRelaySender returns a RelaySender pushing for daemonID to the devices
// registry lists.
func NewRelaySender(registry Registry, daemonID string, opts ...RelayOption) *RelaySender {
	s := &RelaySender{
		registry: registry,
		daemonID: daemonID,
		client:   &http.Client{},
		now:      time.Now,
		sleep:    sleepContext,
		logger:   log.New(io.Discard, "", 0),
	}
	for _, opt := range opts {
		opt(s)
	}
	// A relay answers directly; following a redirect would let a bad relay
	// URL bounce the daemon's request elsewhere.
	c := *s.client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	s.client = &c
	return s
}

// Send pushes a to every registered device concurrently and returns when all
// have been attempted.
func (s *RelaySender) Send(ctx context.Context, a Alert) {
	regs, err := s.registry.Registrations()
	if err != nil {
		s.logger.Printf("push: listing registrations: %v", err)
		return
	}
	plain, err := s.plaintext(a)
	if err != nil {
		s.logger.Printf("push: encoding alert: %v", err)
		return
	}
	collapse := CollapseID(s.daemonID, a.Agent)
	var wg sync.WaitGroup
	for _, reg := range regs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.deliver(ctx, reg, plain, collapse)
		}()
	}
	wg.Wait()
}

func (s *RelaySender) plaintext(a Alert) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	err := enc.Encode(payload{
		V:        payloadVersion,
		DaemonID: s.daemonID,
		Agent:    clip(a.Agent, maxAgentRunes),
		Kind:     a.Kind,
		Title:    clip(a.Title, maxTitleRunes),
		TS:       a.At.Unix(),
	})
	return bytes.TrimRight(buf.Bytes(), "\n"), err
}

// deliver sends plain to one device, retrying once on a 5xx or network
// error, and forgets the registration on a 410.
func (s *RelaySender) deliver(ctx context.Context, reg Registration, plain []byte, collapse string) {
	endpoint, ok := pushEndpoint(reg.RelayURL)
	if !ok || len(reg.Key) != keyBytes {
		s.logger.Printf("push: device %s: skipping unusable registration", reg.DeviceID)
		return
	}
	ciphertext, err := seal(reg.Key, plain)
	if err != nil {
		s.logger.Printf("push: device %s: encrypting: %v", reg.DeviceID, err)
		return
	}
	body, err := json.Marshal(relayRequest{Grant: reg.Grant, Ciphertext: ciphertext, CollapseID: collapse})
	if err != nil {
		return
	}
	result := s.attempt(ctx, reg.DeviceID, endpoint, body)
	if result == outcomeRetry {
		if s.sleep(ctx, retryBackoff) != nil {
			return
		}
		result = s.attempt(ctx, reg.DeviceID, endpoint, body)
	}
	if result == outcomeGone {
		if err := s.registry.Forget(reg.DeviceID, reg.Grant); err != nil {
			s.logger.Printf("push: device %s: dropping dead registration: %v", reg.DeviceID, err)
		}
	}
}

// attempt makes one POST under its own timeout.
func (s *RelaySender) attempt(ctx context.Context, deviceID, endpoint string, body []byte) outcome {
	ctx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return outcomeRejected
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		s.logger.Printf("push: device %s: relay unreachable: %v", deviceID, transportError(err))
		return outcomeRetry
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseDrain))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return outcomeDelivered
	case resp.StatusCode == http.StatusGone:
		s.logger.Printf("push: device %s: relay reports the registration is gone", deviceID)
		return outcomeGone
	case resp.StatusCode >= 500:
		s.logger.Printf("push: device %s: relay answered %d", deviceID, resp.StatusCode)
		return outcomeRetry
	default:
		s.logger.Printf("push: device %s: relay rejected the push with %d", deviceID, resp.StatusCode)
		return outcomeRejected
	}
}

// pushEndpoint is relayURL + "/push", only for a well-formed https URL.
func pushEndpoint(relayURL string) (string, bool) {
	u, err := url.Parse(relayURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return "", false
	}
	return strings.TrimRight(relayURL, "/") + "/push", true
}

// transportError drops the request URL from a client error, keeping only
// what went wrong.
func transportError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// clip replaces control characters with spaces and truncates to max runes.
func clip(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

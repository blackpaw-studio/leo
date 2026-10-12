package chat

import (
	"crypto/rand"
	"io"
	"strings"
	"sync"
	"time"
)

// crockford is the ULID alphabet (Crockford base32, no I L O U).
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// IDSource mints message and attachment ids.
type IDSource interface {
	New() string
}

// ULIDSource mints ULIDs: 48 bits of millisecond time then 80 random bits,
// as 26 Crockford base32 characters. Within one millisecond the random part
// is incremented, so ids from one source always sort in mint order — the
// message store pages by id.
type ULIDSource struct {
	now     func() time.Time
	entropy io.Reader

	mu     sync.Mutex
	lastMS uint64
	last   [10]byte
}

// NewULIDSource returns a source reading time from now (nil: time.Now) and
// randomness from entropy (nil: crypto/rand).
func NewULIDSource(now func() time.Time, entropy io.Reader) *ULIDSource {
	if now == nil {
		now = time.Now
	}
	if entropy == nil {
		entropy = rand.Reader
	}
	return &ULIDSource{now: now, entropy: entropy}
}

// New returns the next id.
func (s *ULIDSource) New() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ms := uint64(s.now().UnixMilli()) // #nosec G115 -- post-1970 clock
	if ms <= s.lastMS {
		ms = s.lastMS
		if increment(&s.last) {
			// The random part wrapped: borrow the next millisecond so
			// order still holds.
			ms++
			s.lastMS = ms
		}
	} else {
		if _, err := io.ReadFull(s.entropy, s.last[:]); err != nil {
			// A failing entropy source still yields unique, ordered ids
			// from this source: fall back to incrementing.
			increment(&s.last)
		}
		s.lastMS = ms
	}
	var raw [16]byte
	for i := 0; i < 6; i++ {
		raw[i] = byte(ms >> (40 - 8*i)) // #nosec G115 -- deliberate truncation to one byte of the 48-bit time
	}
	copy(raw[6:], s.last[:])
	return encodeULID(raw)
}

// increment adds one to b, reporting whether it wrapped to zero.
func increment(b *[10]byte) (wrapped bool) {
	for i := len(b) - 1; i >= 0; i-- {
		b[i]++
		if b[i] != 0 {
			return false
		}
	}
	return true
}

// encodeULID writes 128 bits as 26 base32 characters, most significant
// first (the leading character carries only 3 bits).
func encodeULID(raw [16]byte) string {
	var out [26]byte
	// Treat raw as a 130-bit big-endian number padded with two leading zero bits.
	for i := 25; i >= 0; i-- {
		bit := (25 - i) * 5 // bit offset from the least significant end
		var v byte
		for j := 0; j < 5; j++ {
			pos := bit + j
			if pos >= 128 {
				break
			}
			byteIdx := 15 - pos/8
			if raw[byteIdx]&(1<<(pos%8)) != 0 {
				v |= 1 << j
			}
		}
		out[i] = crockford[v]
	}
	return string(out[:])
}

// ulidTime decodes the millisecond timestamp of a ULID.
func ulidTime(id string) (time.Time, bool) {
	if len(id) != 26 {
		return time.Time{}, false
	}
	var ms uint64
	for _, c := range id[:10] {
		v := strings.IndexRune(crockford, c)
		if v < 0 {
			return time.Time{}, false
		}
		ms = ms<<5 | uint64(v) // #nosec G115 -- v < 32
	}
	return time.UnixMilli(int64(ms)).UTC(), true // #nosec G115 -- 48-bit value
}

// validID reports whether id is shaped like a ULID, so ids from requests can
// be used in paths and queries without further escaping.
func validID(id string) bool {
	if len(id) != 26 {
		return false
	}
	for _, c := range id {
		if !strings.ContainsRune(crockford, c) {
			return false
		}
	}
	return true
}

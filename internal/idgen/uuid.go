package idgen

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// UUIDv7 returns a new UUIDv7 string formatted per RFC 9562 §5.7
// (xxxxxxxx-xxxx-7xxx-yxxx-xxxxxxxxxxxx), where the leading 48 bits encode
// the current Unix time in milliseconds and the remaining 74 bits are
// cryptographically random. UUIDv7 is time-sortable, which makes it suitable
// as a primary key for append-only tables like comments where natural time
// ordering on scan is desirable.
//
// Panics if the system's cryptographic RNG fails, matching the contract of
// the previous google/uuid.Must(uuid.NewV7()) call site.
func UUIDv7() string {
	var b [16]byte

	// Fill the random portion (bytes 6..15). Bytes 0..5 will be overwritten
	// with the timestamp below, so drawing random into them is harmless.
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("idgen: crypto/rand read failed: %v", err))
	}

	// 48-bit Unix timestamp in milliseconds, big-endian, in bytes 0..5.
	ms := uint64(time.Now().UnixMilli())
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)

	// Version 7: top 4 bits of byte 6 must be 0b0111.
	b[6] = (b[6] & 0x0f) | 0x70
	// IETF variant: top 2 bits of byte 8 must be 0b10.
	b[8] = (b[8] & 0x3f) | 0x80

	// Format as 8-4-4-4-12 hex.
	var out [36]byte
	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:36], b[10:16])
	return string(out[:])
}

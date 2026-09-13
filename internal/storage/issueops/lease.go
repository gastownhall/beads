package issueops

import (
	"crypto/rand"
	"encoding/binary"
	"time"
)

func freshRowLock() int64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return time.Now().UnixNano() | 1
	}
	v := int64(binary.LittleEndian.Uint64(b[:]))
	if v == 0 {
		v = 1
	}
	return v
}

// FreshRowLock returns a fresh non-zero row_lock token.
func FreshRowLock() int64 {
	return freshRowLock()
}

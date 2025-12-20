package idgen

import (
	"crypto/sha256"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// base36Alphabet is the character set for base36 encoding (0-9, a-z).
const base36Alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"

// EncodeBase36 converts a byte slice to a base36 string of specified length.
// Matches the algorithm used for bd hash IDs.
func EncodeBase36(data []byte, length int) string {
	// Convert bytes to big integer
	num := new(big.Int).SetBytes(data)

	// Convert to base36
	var result strings.Builder
	base := big.NewInt(36)
	zero := big.NewInt(0)
	mod := new(big.Int)

	// Build the string in reverse
	chars := make([]byte, 0, length)
	for num.Cmp(zero) > 0 {
		num.DivMod(num, base, mod)
		chars = append(chars, base36Alphabet[mod.Int64()])
	}

	// Reverse the string
	for i := len(chars) - 1; i >= 0; i-- {
		result.WriteByte(chars[i])
	}

	// Pad with zeros if needed
	str := result.String()
	if len(str) < length {
		str = strings.Repeat("0", length-len(str)) + str
	}

	// Truncate to exact length if needed (keep least significant digits)
	if len(str) > length {
		str = str[len(str)-length:]
	}

	return str
}

// GenerateHashID creates a hash-based ID for an issue.
// Uses base36 encoding (0-9, a-z) for better information density than hex.
// The length parameter is expected to be 3-8; other values fall back to a 3-char byte width.
func GenerateHashID(prefix, title, description, creator string, timestamp time.Time, length, nonce int) string {
	// Combine inputs into a stable content string
	// Include nonce to handle hash collisions
	content := fmt.Sprintf("%s|%s|%s|%d|%d", title, description, creator, timestamp.UnixNano(), nonce)

	// Hash the content
	hash := sha256.Sum256([]byte(content))

	// Determine how many bytes to use based on desired output length
	var numBytes int
	switch length {
	case 3:
		numBytes = 2 // 2 bytes = 16 bits ≈ 3.09 base36 chars
	case 4:
		numBytes = 3 // 3 bytes = 24 bits ≈ 4.63 base36 chars
	case 5:
		numBytes = 4 // 4 bytes = 32 bits ≈ 6.18 base36 chars
	case 6:
		numBytes = 4 // 4 bytes = 32 bits ≈ 6.18 base36 chars
	case 7:
		numBytes = 5 // 5 bytes = 40 bits ≈ 7.73 base36 chars
	case 8:
		numBytes = 5 // 5 bytes = 40 bits ≈ 7.73 base36 chars
	default:
		numBytes = 3 // default to 3 chars
	}

	shortHash := EncodeBase36(hash[:numBytes], length)

	return fmt.Sprintf("%s-%s", prefix, shortHash)
}

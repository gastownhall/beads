package linear

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

const idempotencyPrefix = "<!-- bd-idempotency: "
const idempotencySuffix = " -->"

// GenerateIdempotencyMarker produces a deterministic HTML comment marker for
// embedding in Linear issue descriptions. The marker enables dedup queries
// after sync interruptions: if the marker already exists in Linear, we skip
// creation and return the existing issue.
//
// Hash inputs are intentionally limited to immutable fields (beadID,
// creatorEmail, createdAtNano). Title is excluded because it can change
// after creation, which would break dedup on rename.
func GenerateIdempotencyMarker(beadID, creatorEmail string, createdAtNano int64) string {
	h := sha256.New()
	h.Write([]byte(beadID))
	h.Write([]byte(creatorEmail))
	h.Write([]byte(fmt.Sprintf("%d", createdAtNano)))
	hash := hex.EncodeToString(h.Sum(nil))[:12]
	return idempotencyPrefix + hash + idempotencySuffix
}

// AppendIdempotencyMarker appends a marker to a description, separated by a
// newline. If the description is empty, the marker becomes the entire body.
func AppendIdempotencyMarker(description, marker string) string {
	if description == "" {
		return marker
	}
	return description + "\n" + marker
}

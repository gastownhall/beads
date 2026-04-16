package idgen

import (
	"regexp"
	"testing"
	"time"
)

var uuidv7Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestUUIDv7FormatAndVersion(t *testing.T) {
	id := UUIDv7()
	if len(id) != 36 {
		t.Fatalf("len(id) = %d, want 36", len(id))
	}
	if !uuidv7Pattern.MatchString(id) {
		t.Errorf("UUIDv7 %q does not match RFC 9562 format", id)
	}
}

func TestUUIDv7Uniqueness(t *testing.T) {
	const n = 10_000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		id := UUIDv7()
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate UUIDv7 after %d iterations: %s", i, id)
		}
		seen[id] = struct{}{}
	}
}

func TestUUIDv7TimeOrdered(t *testing.T) {
	// UUIDv7 is time-sortable by design: IDs generated later should never
	// sort before earlier IDs at millisecond granularity. Allow one tick
	// for the second ID.
	a := UUIDv7()
	time.Sleep(2 * time.Millisecond)
	b := UUIDv7()
	if a >= b {
		t.Errorf("expected %q < %q (UUIDv7 should be time-sortable)", a, b)
	}
}

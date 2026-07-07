package storage

import (
	"context"
	"strconv"
	"strings"
)

type childCounterReservationKey struct{}

type childCounterReservation struct {
	parentID string
	childNum int
}

// WithReservedChildCounter marks ctx as carrying a child-counter reservation
// from a prior GetNextChildID call for the given child ID.
func WithReservedChildCounter(ctx context.Context, parentID, childID string) context.Context {
	childText, ok := strings.CutPrefix(childID, parentID+".")
	if !ok {
		return ctx
	}
	childNum, err := strconv.Atoi(childText)
	if err != nil {
		return ctx
	}
	return context.WithValue(ctx, childCounterReservationKey{}, childCounterReservation{
		parentID: parentID,
		childNum: childNum,
	})
}

// HasReservedChildCounter reports whether ctx carries a reservation matching
// the parent and child number about to be committed.
func HasReservedChildCounter(ctx context.Context, parentID string, childNum int) bool {
	reservation, ok := ctx.Value(childCounterReservationKey{}).(childCounterReservation)
	if !ok {
		return false
	}
	return reservation.parentID == parentID && reservation.childNum == childNum
}

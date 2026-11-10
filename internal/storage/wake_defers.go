package storage

import "context"

// ExpiredDeferWaker lets ready-role decorators preserve the backend's lazy
// wake before selecting work. The sweep uses its own transaction and is
// advisory: implementations warn on failure rather than failing the claim.
type ExpiredDeferWaker interface {
	WakeExpiredDefersAdvisory(context.Context)
}

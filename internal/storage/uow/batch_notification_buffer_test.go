package uow

import "testing"

type unwrappingTestUOW struct {
	UnitOfWork
	inner UnitOfWork
}

func (u unwrappingTestUOW) Unwrap() UnitOfWork { return u.inner }

// TestBatchNotificationBufferIsFoundBeneathADecorator pins that a batch's
// re-close suppression (markBatchNotifications / rewindBatchNotifications)
// reaches the notifying unit of work through a decorator that forwards Unwrap
// — the external-dependency batch-close guard sits exactly there — instead of
// silently turning suppression off, which would let a batch's idempotent
// re-close fire the workspace's close hook (ga-2yaqp.1).
func TestBatchNotificationBufferIsFoundBeneathADecorator(t *testing.T) {
	notifying := &notifyingUOW{rec: &recorder{}}
	decorated := unwrappingTestUOW{inner: unwrappingTestUOW{inner: notifying}}

	notifying.rec.entries = append(notifying.rec.entries, mutationEntry{})
	mark := markBatchNotifications(decorated)
	if mark != 1 {
		t.Fatalf("mark through two decorators = %d, want 1 (the buffer beneath)", mark)
	}
	notifying.rec.entries = append(notifying.rec.entries, mutationEntry{})
	rewindBatchNotifications(decorated, mark)
	if got := len(notifying.rec.entries); got != 1 {
		t.Fatalf("entries after rewind through decorators = %d, want 1", got)
	}
	if got := markBatchNotifications(unwrappingTestUOW{}); got != -1 {
		t.Errorf("mark with no buffer anywhere = %d, want -1", got)
	}
}

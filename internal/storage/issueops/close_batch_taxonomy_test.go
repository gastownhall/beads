package issueops

import (
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	publicops "github.com/steveyegge/beads/issueops"
)

func TestCloseRefusalStaysPerItemPhaseOutranksSentinel(t *testing.T) {
	if !CloseRefusalStaysPerItem(storage.ErrNotFound) {
		t.Fatal("plain ErrNotFound must stay per-item")
	}
	if !CloseRefusalStaysPerItem(fmt.Errorf("resolve x: %w", storage.ErrCloseBlocked)) {
		t.Fatal("wrapped ErrCloseBlocked must stay per-item")
	}
	if CloseRefusalStaysPerItem(publicops.MarkPostWrite(storage.ErrNotFound)) {
		t.Fatal("post-write ErrNotFound must fail the request, not read as a refusal")
	}
	if CloseRefusalStaysPerItem(fmt.Errorf("close batch item x: %w", publicops.MarkPostWrite(storage.ErrNotFound))) {
		t.Fatal("post-write marker must survive outer wrapping")
	}
	if !errors.Is(publicops.MarkPostWrite(storage.ErrNotFound), storage.ErrNotFound) {
		t.Fatal("marker must not hide the underlying sentinel from errors.Is")
	}
	if publicops.MarkPostWrite(nil) != nil {
		t.Fatal("publicops.MarkPostWrite(nil) must stay nil")
	}
	if CloseRefusalStaysPerItem(errors.New("driver: bad conn")) {
		t.Fatal("infra error must fail the request")
	}
}

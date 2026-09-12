package issueops

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	publicops "github.com/steveyegge/beads/issueops"
)

func TestCloseRefusalStaysPerItemPhaseOutranksSentinel(t *testing.T) {
	for _, err := range []error{sql.ErrNoRows, fmt.Errorf("resolve x: %w", sql.ErrNoRows)} {
		if !CloseRefusalStaysPerItem(err) {
			t.Errorf("lookup miss %v must stay per-item", err)
		}
		if CloseRefusalStaysPerItem(publicops.MarkPostWrite(err)) {
			t.Errorf("post-write miss %v must fail the request", err)
		}
	}
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

func TestBatchCloseHydrationDisarmPreservesNewerArm(t *testing.T) {
	firstDisarm := FailBatchCloseHydrationOf("first")
	defer firstDisarm()
	secondDisarm := FailBatchCloseHydrationOf("second")
	defer secondDisarm()
	firstDisarm()
	if InducedBatchCloseHydrationFailure("second") == nil {
		t.Fatal("disarming an older probe must not clear a newer probe")
	}
	secondDisarm()
	if err := InducedBatchCloseHydrationFailure("second"); err != nil {
		t.Fatalf("probe still armed after its own disarm: %v", err)
	}
}

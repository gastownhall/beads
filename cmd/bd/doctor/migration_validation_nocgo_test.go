//go:build !cgo

package doctor

import (
	"testing"
)

func TestCheckMigrationReadiness_NoCGO(t *testing.T) {
	check, result := CheckMigrationReadiness("/tmp/nonexistent")

	if check.Status != StatusOK {
		t.Errorf("status = %q, want %q", check.Status, StatusOK)
	}

	if check.Message != "N/A (requires CGO for Dolt)" {
		t.Errorf("message = %q, want %q", check.Message, "N/A (requires CGO for Dolt)")
	}

	if result.Ready {
		t.Error("expected Ready = false in nocgo mode")
	}

	if len(result.Errors) == 0 {
		t.Error("expected error message about CGO")
	}
}

func TestCheckMigrationCompletion_NoCGO(t *testing.T) {
	check, result := CheckMigrationCompletion("/tmp/nonexistent")

	if check.Status != StatusOK {
		t.Errorf("status = %q, want %q", check.Status, StatusOK)
	}

	if result.Ready {
		t.Error("expected Ready = false in nocgo mode")
	}
}

func TestCheckDoltLocks_NoCGO(t *testing.T) {
	check := CheckDoltLocks("/tmp/nonexistent")

	if check.Status != StatusOK {
		t.Errorf("status = %q, want %q", check.Status, StatusOK)
	}

	if check.Message != "N/A (requires CGO for Dolt)" {
		t.Errorf("message = %q, want %q", check.Message, "N/A (requires CGO for Dolt)")
	}
}

func TestMigrationValidationResult_NoCGO(t *testing.T) {
	// Test that the struct is properly defined even without CGO
	result := MigrationValidationResult{
		Phase:   "test",
		Ready:   false,
		Backend: "unknown",
	}

	if result.Phase != "test" {
		t.Errorf("Phase = %q, want %q", result.Phase, "test")
	}
}

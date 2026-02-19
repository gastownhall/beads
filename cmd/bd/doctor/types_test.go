package doctor

import (
	"testing"
)

func TestStatusConstants(t *testing.T) {
	// Verify status constants have expected values
	if StatusOK != "ok" {
		t.Errorf("StatusOK = %q, want %q", StatusOK, "ok")
	}
	if StatusWarning != "warning" {
		t.Errorf("StatusWarning = %q, want %q", StatusWarning, "warning")
	}
	if StatusError != "error" {
		t.Errorf("StatusError = %q, want %q", StatusError, "error")
	}
}

func TestDoctorCheckStruct(t *testing.T) {
	check := DoctorCheck{
		Name:    "Test",
		Status:  StatusOK,
		Message: "All good",
		Detail:  "Details here",
		Fix:     "Fix suggestion",
	}

	if check.Name != "Test" {
		t.Errorf("Name = %q, want %q", check.Name, "Test")
	}
	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q", check.Status, StatusOK)
	}
	if check.Message != "All good" {
		t.Errorf("Message = %q, want %q", check.Message, "All good")
	}
}

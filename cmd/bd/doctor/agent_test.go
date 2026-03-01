package doctor

import "testing"

func TestEnrichForAgent_KnownCheck(t *testing.T) {
	dc := DoctorCheck{
		Name:     "Installation",
		Status:   StatusError,
		Message:  "No .beads/ directory found",
		Fix:      "Run 'bd init --prefix test' to initialize beads",
		Category: CategoryCore,
	}

	ad := EnrichForAgent(dc)

	if ad.Name != "Installation" {
		t.Errorf("Name = %q, want %q", ad.Name, "Installation")
	}
	if ad.Status != StatusError {
		t.Errorf("Status = %q, want %q", ad.Status, StatusError)
	}
	if ad.Severity != "blocking" {
		t.Errorf("Severity = %q, want %q", ad.Severity, "blocking")
	}
	if ad.Explanation == "" {
		t.Error("Explanation is empty, want non-empty prose")
	}
	if ad.Observed == "" {
		t.Error("Observed is empty, want non-empty")
	}
	if ad.Expected == "" {
		t.Error("Expected is empty, want non-empty")
	}
	if len(ad.Commands) == 0 {
		t.Error("Commands is empty, want at least one remediation command")
	}
	if len(ad.SourceFiles) == 0 {
		t.Error("SourceFiles is empty, want at least one source reference")
	}
}

func TestEnrichForAgent_UnknownCheck(t *testing.T) {
	dc := DoctorCheck{
		Name:     "Some Future Check",
		Status:   StatusWarning,
		Message:  "something is off",
		Detail:   "more details here",
		Fix:      "run something",
		Category: CategoryData,
	}

	ad := EnrichForAgent(dc)

	if ad.Name != "Some Future Check" {
		t.Errorf("Name = %q, want %q", ad.Name, "Some Future Check")
	}
	if ad.Status != StatusWarning {
		t.Errorf("Status = %q, want %q", ad.Status, StatusWarning)
	}
	if ad.Severity != "degraded" {
		t.Errorf("Severity = %q, want %q", ad.Severity, "degraded")
	}
	if ad.Explanation == "" {
		t.Error("Explanation is empty even for unknown check, want generic enrichment")
	}
	if ad.Observed == "" {
		t.Error("Observed is empty, want message + detail")
	}
	if len(ad.Commands) == 0 {
		t.Error("Commands is empty, want fix string as command")
	}
}

func TestEnrichForAgent_OKCheck(t *testing.T) {
	dc := DoctorCheck{
		Name:    "Database",
		Status:  StatusOK,
		Message: "version 0.76.4",
	}

	ad := EnrichForAgent(dc)

	// OK checks should still enrich (the caller decides whether to include them)
	if ad.Status != StatusOK {
		t.Errorf("Status = %q, want %q", ad.Status, StatusOK)
	}
}

func TestSeverityFromStatus(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{StatusError, "blocking"},
		{StatusWarning, "degraded"},
		{StatusOK, "advisory"},
	}
	for _, tt := range tests {
		got := severityFromStatus(tt.status)
		if got != tt.want {
			t.Errorf("severityFromStatus(%q) = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestEnrichForAgent_AllEnrichersExist(t *testing.T) {
	// Verify that every enricher in the map produces a non-empty result
	for name, fn := range agentEnrichers {
		dc := DoctorCheck{
			Name:    name,
			Status:  StatusWarning,
			Message: "test message",
			Detail:  "test detail",
			Fix:     "test fix",
		}
		e := fn(dc)
		if e.severity == "" {
			t.Errorf("enricher for %q returned empty severity", name)
		}
		if e.explanation == "" {
			t.Errorf("enricher for %q returned empty explanation", name)
		}
		if e.expected == "" {
			t.Errorf("enricher for %q returned empty expected", name)
		}
	}
}

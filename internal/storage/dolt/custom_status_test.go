package dolt

import (
	"strings"
	"testing"
)

func TestBuildReadyIssuesView(t *testing.T) {
	tests := []struct {
		name         string
		wantContains []string
	}{
		{
			name:         "uses table-backed view",
			wantContains: []string{"i.status = 'open'", "custom_statuses WHERE category = 'active'"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql := BuildReadyIssuesView()
			for _, want := range tt.wantContains {
				if !strings.Contains(sql, want) {
					t.Errorf("expected SQL to contain %q, got:\n%s", want, sql)
				}
			}
			if !strings.Contains(sql, "CREATE OR REPLACE VIEW ready_issues") {
				t.Errorf("expected valid CREATE VIEW statement")
			}
		})
	}
}

func TestBuildBlockedIssuesView(t *testing.T) {
	tests := []struct {
		name         string
		wantContains []string
	}{
		{
			name:         "uses table-backed view with CTE",
			wantContains: []string{"NOT IN ('closed', 'pinned')", "done_frozen", "custom_statuses WHERE category IN ('done', 'frozen')"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql := BuildBlockedIssuesView()
			for _, want := range tt.wantContains {
				if !strings.Contains(sql, want) {
					t.Errorf("expected SQL to contain %q, got:\n%s", want, sql)
				}
			}
			if !strings.Contains(sql, "CREATE OR REPLACE VIEW blocked_issues") {
				t.Errorf("expected valid CREATE VIEW statement")
			}
		})
	}
}

func TestEscapeSQL(t *testing.T) {
	// escapeSQL is legacy but retained for backward compatibility
	tests := []struct {
		input string
		want  string
	}{
		{"review", "review"},
		{"it's", "it''s"},
		{"a'b'c", "a''b''c"},
		{"normal-status_123", "normal-status_123"},
		// SQL injection attempts
		{"'; DROP TABLE issues; --", "''; DROP TABLE issues; --"},
		{"review' OR '1'='1", "review'' OR ''1''=''1"},
	}
	for _, tt := range tests {
		got := escapeSQL(tt.input)
		if got != tt.want {
			t.Errorf("escapeSQL(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestBuildReadyIssuesViewIsStatic(t *testing.T) {
	// The view is now static (table-backed) — same SQL on every call
	sql1 := BuildReadyIssuesView()
	sql2 := BuildReadyIssuesView()
	if sql1 != sql2 {
		t.Error("BuildReadyIssuesView should return identical SQL on every call")
	}
}

func TestBuildBlockedIssuesViewIsStatic(t *testing.T) {
	// The view is now static (table-backed) — same SQL on every call
	sql1 := BuildBlockedIssuesView()
	sql2 := BuildBlockedIssuesView()
	if sql1 != sql2 {
		t.Error("BuildBlockedIssuesView should return identical SQL on every call")
	}
}

func TestViewsReferenceCustomStatusesTable(t *testing.T) {
	readySQL := BuildReadyIssuesView()
	blockedSQL := BuildBlockedIssuesView()

	if !strings.Contains(readySQL, "custom_statuses") {
		t.Error("ready_issues view should reference custom_statuses table")
	}
	if !strings.Contains(blockedSQL, "custom_statuses") {
		t.Error("blocked_issues view should reference custom_statuses table")
	}
}

func TestBlockedViewUsesCTE(t *testing.T) {
	sql := BuildBlockedIssuesView()
	if !strings.Contains(sql, "WITH done_frozen AS") {
		t.Error("blocked_issues view should use done_frozen CTE")
	}
	// Verify the CTE is referenced, not the raw subquery repeated
	count := strings.Count(sql, "SELECT name FROM custom_statuses WHERE category IN ('done', 'frozen')")
	if count != 1 {
		t.Errorf("expected custom_statuses subquery exactly once (in CTE), found %d times", count)
	}
}

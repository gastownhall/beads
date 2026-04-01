package dolt

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestBuildReadyIssuesView(t *testing.T) {
	tests := []struct {
		name           string
		customStatuses []types.CustomStatus
		wantContains   []string
		wantNotContain []string
	}{
		{
			name:           "no custom statuses uses table-backed view",
			customStatuses: nil,
			wantContains:   []string{"i.status = 'open'", "custom_statuses WHERE category = 'active'"},
		},
		{
			name: "custom statuses param is ignored (table-backed)",
			customStatuses: []types.CustomStatus{
				{Name: "review", Category: types.CategoryActive},
			},
			wantContains: []string{"custom_statuses WHERE category = 'active'"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql := BuildReadyIssuesView(tt.customStatuses)
			for _, want := range tt.wantContains {
				if !strings.Contains(sql, want) {
					t.Errorf("expected SQL to contain %q, got:\n%s", want, sql)
				}
			}
			for _, notWant := range tt.wantNotContain {
				if strings.Contains(sql, notWant) {
					t.Errorf("expected SQL to NOT contain %q, got:\n%s", notWant, sql)
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
		name           string
		customStatuses []types.CustomStatus
		wantContains   []string
		wantNotContain []string
	}{
		{
			name:           "no custom statuses uses table-backed view",
			customStatuses: nil,
			wantContains:   []string{"NOT IN ('closed', 'pinned')", "custom_statuses WHERE category IN ('done', 'frozen')"},
		},
		{
			name: "custom statuses param is ignored (table-backed)",
			customStatuses: []types.CustomStatus{
				{Name: "archived", Category: types.CategoryDone},
			},
			wantContains: []string{"custom_statuses WHERE category IN ('done', 'frozen')"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql := BuildBlockedIssuesView(tt.customStatuses)
			for _, want := range tt.wantContains {
				if !strings.Contains(sql, want) {
					t.Errorf("expected SQL to contain %q, got:\n%s", want, sql)
				}
			}
			for _, notWant := range tt.wantNotContain {
				if strings.Contains(sql, notWant) {
					t.Errorf("expected SQL to NOT contain %q, got:\n%s", notWant, sql)
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
	// The view is now static (table-backed) — same SQL regardless of input
	sql1 := BuildReadyIssuesView(nil)
	sql2 := BuildReadyIssuesView([]types.CustomStatus{
		{Name: "review", Category: types.CategoryActive},
		{Name: "testing", Category: types.CategoryWIP},
	})
	if sql1 != sql2 {
		t.Error("BuildReadyIssuesView should return identical SQL regardless of input")
	}
}

func TestBuildBlockedIssuesViewIsStatic(t *testing.T) {
	// The view is now static (table-backed) — same SQL regardless of input
	sql1 := BuildBlockedIssuesView(nil)
	sql2 := BuildBlockedIssuesView([]types.CustomStatus{
		{Name: "archived", Category: types.CategoryDone},
		{Name: "on-ice", Category: types.CategoryFrozen},
	})
	if sql1 != sql2 {
		t.Error("BuildBlockedIssuesView should return identical SQL regardless of input")
	}
}

func TestViewsReferenceCustomStatusesTable(t *testing.T) {
	readySQL := BuildReadyIssuesView(nil)
	blockedSQL := BuildBlockedIssuesView(nil)

	if !strings.Contains(readySQL, "custom_statuses") {
		t.Error("ready_issues view should reference custom_statuses table")
	}
	if !strings.Contains(blockedSQL, "custom_statuses") {
		t.Error("blocked_issues view should reference custom_statuses table")
	}
}

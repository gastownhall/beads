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
			name:           "no custom statuses uses static view",
			customStatuses: nil,
			wantContains:   []string{"i.status = 'open'"},
			wantNotContain: []string{"'review'"},
		},
		{
			name: "no active custom statuses uses static view",
			customStatuses: []types.CustomStatus{
				{Name: "review", Category: types.CategoryWIP},
			},
			wantContains:   []string{"i.status = 'open'"},
			wantNotContain: []string{"'review'"},
		},
		{
			name: "active custom statuses added to IN clause",
			customStatuses: []types.CustomStatus{
				{Name: "review", Category: types.CategoryActive},
				{Name: "triage", Category: types.CategoryActive},
				{Name: "testing", Category: types.CategoryWIP},
			},
			wantContains:   []string{"IN", "'open'", "'review'", "'triage'"},
			wantNotContain: []string{"'testing'"},
		},
		{
			name: "unspecified category not included",
			customStatuses: []types.CustomStatus{
				{Name: "legacy", Category: types.CategoryUnspecified},
				{Name: "review", Category: types.CategoryActive},
			},
			wantContains:   []string{"'review'"},
			wantNotContain: []string{"'legacy'"},
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
			// All views must be valid CREATE OR REPLACE VIEW statements
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
			name:           "no custom statuses uses static view",
			customStatuses: nil,
			wantContains:   []string{"NOT IN ('closed', 'pinned')"},
			wantNotContain: []string{"'archived'"},
		},
		{
			name: "done/frozen custom statuses added to NOT IN",
			customStatuses: []types.CustomStatus{
				{Name: "archived", Category: types.CategoryDone},
				{Name: "on-ice", Category: types.CategoryFrozen},
				{Name: "review", Category: types.CategoryActive},
			},
			wantContains:   []string{"'closed'", "'pinned'", "'archived'", "'on-ice'"},
			wantNotContain: []string{"'review'"},
		},
		{
			name: "only wip and active statuses - uses static view",
			customStatuses: []types.CustomStatus{
				{Name: "review", Category: types.CategoryActive},
				{Name: "testing", Category: types.CategoryWIP},
			},
			wantContains:   []string{"NOT IN ('closed', 'pinned')"},
			wantNotContain: []string{"'review'", "'testing'"},
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

func TestBuildReadyIssuesViewManyActiveStatuses(t *testing.T) {
	// 12 active statuses should all appear in the IN clause
	var statuses []types.CustomStatus
	for i := 0; i < 12; i++ {
		name := "status" + strings.Repeat("x", i+1) // statusx, statusxx, ...
		statuses = append(statuses, types.CustomStatus{Name: name, Category: types.CategoryActive})
	}

	sql := BuildReadyIssuesView(statuses)
	if !strings.Contains(sql, "IN") {
		t.Fatal("expected IN clause for multiple active statuses")
	}
	for _, s := range statuses {
		if !strings.Contains(sql, "'"+s.Name+"'") {
			t.Errorf("expected SQL to contain %q", s.Name)
		}
	}
	if !strings.Contains(sql, "'open'") {
		t.Error("expected SQL to still contain 'open'")
	}
}

func TestBuildBlockedIssuesViewManyDoneFrozen(t *testing.T) {
	var statuses []types.CustomStatus
	for i := 0; i < 5; i++ {
		statuses = append(statuses, types.CustomStatus{
			Name:     "done" + strings.Repeat("x", i+1),
			Category: types.CategoryDone,
		})
	}
	for i := 0; i < 5; i++ {
		statuses = append(statuses, types.CustomStatus{
			Name:     "frozen" + strings.Repeat("x", i+1),
			Category: types.CategoryFrozen,
		})
	}

	sql := BuildBlockedIssuesView(statuses)
	for _, s := range statuses {
		if !strings.Contains(sql, "'"+s.Name+"'") {
			t.Errorf("expected SQL to contain %q", s.Name)
		}
	}
	// Built-in exclusions should still be present
	if !strings.Contains(sql, "'closed'") {
		t.Error("expected SQL to contain 'closed'")
	}
	if !strings.Contains(sql, "'pinned'") {
		t.Error("expected SQL to contain 'pinned'")
	}
}

func TestBuildReadyIssuesViewUnspecifiedNotIncluded(t *testing.T) {
	statuses := []types.CustomStatus{
		{Name: "legacy", Category: types.CategoryUnspecified},
		{Name: "old-flow", Category: types.CategoryUnspecified},
	}

	sql := BuildReadyIssuesView(statuses)
	// Unspecified should NOT affect the ready view
	if strings.Contains(sql, "'legacy'") {
		t.Error("unspecified status 'legacy' should not appear in ready view")
	}
	if strings.Contains(sql, "'old-flow'") {
		t.Error("unspecified status 'old-flow' should not appear in ready view")
	}
	// Should use static view (no IN clause needed)
	if !strings.Contains(sql, "i.status = 'open'") {
		t.Error("expected static view with just i.status = 'open'")
	}
}

func TestBuildBlockedIssuesViewUnspecifiedNotIncluded(t *testing.T) {
	statuses := []types.CustomStatus{
		{Name: "legacy", Category: types.CategoryUnspecified},
		{Name: "old-flow", Category: types.CategoryUnspecified},
	}

	sql := BuildBlockedIssuesView(statuses)
	// Unspecified should NOT affect the blocked view
	if strings.Contains(sql, "'legacy'") {
		t.Error("unspecified status 'legacy' should not appear in blocked view")
	}
	if strings.Contains(sql, "'old-flow'") {
		t.Error("unspecified status 'old-flow' should not appear in blocked view")
	}
	// Should use static NOT IN
	if !strings.Contains(sql, "NOT IN ('closed', 'pinned')") {
		t.Error("expected static NOT IN clause")
	}
}

func TestParseStatusFallback(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  []types.CustomStatus
	}{
		{
			name:  "simple names get CategoryUnspecified",
			input: []string{"review", "testing"},
			want: []types.CustomStatus{
				{Name: "review", Category: types.CategoryUnspecified},
				{Name: "testing", Category: types.CategoryUnspecified},
			},
		},
		{
			name:  "category format parsed correctly",
			input: []string{"review:active", "testing:wip"},
			want: []types.CustomStatus{
				{Name: "review", Category: types.CategoryActive},
				{Name: "testing", Category: types.CategoryWIP},
			},
		},
		{
			name:  "mixed format",
			input: []string{"review:active", "legacy"},
			want: []types.CustomStatus{
				{Name: "review", Category: types.CategoryActive},
				{Name: "legacy", Category: types.CategoryUnspecified},
			},
		},
		{
			name:  "empty entries filtered",
			input: []string{"", "review", ""},
			want: []types.CustomStatus{
				{Name: "review", Category: types.CategoryUnspecified},
			},
		},
		{
			name:  "empty list",
			input: []string{},
			want:  nil,
		},
		{
			name:  "nil input",
			input: nil,
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseStatusFallback(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d statuses, want %d: %+v", len(got), len(tt.want), got)
			}
			for i, g := range got {
				if g.Name != tt.want[i].Name || g.Category != tt.want[i].Category {
					t.Errorf("status[%d] = {%q, %q}, want {%q, %q}",
						i, g.Name, g.Category, tt.want[i].Name, tt.want[i].Category)
				}
			}
		})
	}
}

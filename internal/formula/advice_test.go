package formula

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestMatchGlob(t *testing.T) {
	tests := []struct {
		pattern string
		stepID  string
		want    bool
	}{
		// Exact matches
		{"design", "design", true},
		{"design", "implement", false},
		{"design", "design.draft", false},

		// Wildcard all
		{"*", "design", true},
		{"*", "implement.draft", true},
		{"*", "", true},

		// Suffix patterns (*.suffix)
		{"*.implement", "shiny.implement", true},
		{"*.implement", "design.implement", true},
		{"*.implement", "implement", false},
		{"*.implement", "shiny.design", false},

		// Prefix patterns (prefix.*)
		{"shiny.*", "shiny.design", true},
		{"shiny.*", "shiny.implement", true},
		{"shiny.*", "shiny", false},
		{"shiny.*", "enterprise.design", false},

		// Complex patterns
		{"*.refine-*", "implement.refine-1", true},
		{"*.refine-*", "implement.refine-2", true},
		{"*.refine-*", "implement.draft", false},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.stepID, func(t *testing.T) {
			got := MatchGlob(tt.pattern, tt.stepID)
			if got != tt.want {
				t.Errorf("MatchGlob(%q, %q) = %v, want %v", tt.pattern, tt.stepID, got, tt.want)
			}
		})
	}
}

func TestApplyAdvice_Before(t *testing.T) {
	steps := []*Step{
		{ID: "design", Title: "Design"},
		{ID: "implement", Title: "Implement"},
	}

	advice := []*AdviceRule{
		{
			Target: "implement",
			Before: &AdviceStep{
				ID:    "lint-{step.id}",
				Title: "Lint before {step.id}",
			},
		},
	}

	result := ApplyAdvice(steps, advice)

	if len(result) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(result))
	}

	// Check order: design, lint-implement, implement
	if result[0].ID != "design" {
		t.Errorf("expected first step 'design', got %q", result[0].ID)
	}
	if result[1].ID != "lint-implement" {
		t.Errorf("expected second step 'lint-implement', got %q", result[1].ID)
	}
	if result[2].ID != "implement" {
		t.Errorf("expected third step 'implement', got %q", result[2].ID)
	}

	// Check that implement now depends on lint-implement
	if !contains(result[2].Needs, "lint-implement") {
		t.Errorf("implement should depend on lint-implement, got needs: %v", result[2].Needs)
	}
}

func TestApplyAdvice_After(t *testing.T) {
	steps := []*Step{
		{ID: "implement", Title: "Implement"},
		{ID: "submit", Title: "Submit"},
	}

	advice := []*AdviceRule{
		{
			Target: "implement",
			After: &AdviceStep{
				ID:    "test-{step.id}",
				Title: "Test after {step.id}",
			},
		},
	}

	result := ApplyAdvice(steps, advice)

	if len(result) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(result))
	}

	// Check order: implement, test-implement, submit
	if result[0].ID != "implement" {
		t.Errorf("expected first step 'implement', got %q", result[0].ID)
	}
	if result[1].ID != "test-implement" {
		t.Errorf("expected second step 'test-implement', got %q", result[1].ID)
	}
	if result[2].ID != "submit" {
		t.Errorf("expected third step 'submit', got %q", result[2].ID)
	}

	// Check that test-implement depends on implement
	if !contains(result[1].Needs, "implement") {
		t.Errorf("test-implement should depend on implement, got needs: %v", result[1].Needs)
	}
}

func TestApplyAdvice_Around(t *testing.T) {
	steps := []*Step{
		{ID: "implement", Title: "Implement"},
	}

	advice := []*AdviceRule{
		{
			Target: "implement",
			Around: &AroundAdvice{
				Before: []*AdviceStep{
					{ID: "pre-scan", Title: "Pre-scan"},
				},
				After: []*AdviceStep{
					{ID: "post-scan", Title: "Post-scan"},
				},
			},
		},
	}

	result := ApplyAdvice(steps, advice)

	if len(result) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(result))
	}

	// Check order: pre-scan, implement, post-scan
	if result[0].ID != "pre-scan" {
		t.Errorf("expected first step 'pre-scan', got %q", result[0].ID)
	}
	if result[1].ID != "implement" {
		t.Errorf("expected second step 'implement', got %q", result[1].ID)
	}
	if result[2].ID != "post-scan" {
		t.Errorf("expected third step 'post-scan', got %q", result[2].ID)
	}

	// Check dependencies
	if !contains(result[1].Needs, "pre-scan") {
		t.Errorf("implement should depend on pre-scan, got needs: %v", result[1].Needs)
	}
	if !contains(result[2].Needs, "implement") {
		t.Errorf("post-scan should depend on implement, got needs: %v", result[2].Needs)
	}
}

func TestApplyAdvice_GlobPattern(t *testing.T) {
	steps := []*Step{
		{ID: "design", Title: "Design"},
		{ID: "shiny.implement", Title: "Implement"},
		{ID: "shiny.review", Title: "Review"},
	}

	advice := []*AdviceRule{
		{
			Target: "shiny.*",
			Before: &AdviceStep{
				ID:    "log-{step.id}",
				Title: "Log {step.id}",
			},
		},
	}

	result := ApplyAdvice(steps, advice)

	// Should have: design, log-shiny.implement, shiny.implement, log-shiny.review, shiny.review
	if len(result) != 5 {
		t.Fatalf("expected 5 steps, got %d", len(result))
	}

	if result[0].ID != "design" {
		t.Errorf("expected first step 'design', got %q", result[0].ID)
	}
	if result[1].ID != "log-shiny.implement" {
		t.Errorf("expected second step 'log-shiny.implement', got %q", result[1].ID)
	}
	if result[2].ID != "shiny.implement" {
		t.Errorf("expected third step 'shiny.implement', got %q", result[2].ID)
	}
}

func TestApplyAdvice_NoMatch(t *testing.T) {
	steps := []*Step{
		{ID: "design", Title: "Design"},
		{ID: "implement", Title: "Implement"},
	}

	advice := []*AdviceRule{
		{
			Target: "nonexistent",
			Before: &AdviceStep{
				ID:    "lint",
				Title: "Lint",
			},
		},
	}

	result := ApplyAdvice(steps, advice)

	// No changes expected
	if len(result) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(result))
	}

	if result[0].ID != "design" || result[1].ID != "implement" {
		t.Errorf("steps should be unchanged")
	}
}

func TestApplyAdvice_EmptyAdvice(t *testing.T) {
	steps := []*Step{
		{ID: "design", Title: "Design"},
	}

	result := ApplyAdvice(steps, nil)

	if len(result) != 1 || result[0].ID != "design" {
		t.Errorf("empty advice should return original steps")
	}
}

func TestMatchPointcut(t *testing.T) {
	tests := []struct {
		name string
		pc   *Pointcut
		step *Step
		want bool
	}{
		{
			name: "glob match",
			pc:   &Pointcut{Glob: "*.implement"},
			step: &Step{ID: "shiny.implement"},
			want: true,
		},
		{
			name: "glob no match",
			pc:   &Pointcut{Glob: "*.implement"},
			step: &Step{ID: "shiny.design"},
			want: false,
		},
		{
			name: "type match",
			pc:   &Pointcut{Type: "bug"},
			step: &Step{ID: "fix", Type: "bug"},
			want: true,
		},
		{
			name: "type no match",
			pc:   &Pointcut{Type: "bug"},
			step: &Step{ID: "fix", Type: "task"},
			want: false,
		},
		{
			name: "label match",
			pc:   &Pointcut{Label: "security"},
			step: &Step{ID: "audit", Labels: []string{"security", "review"}},
			want: true,
		},
		{
			name: "label no match",
			pc:   &Pointcut{Label: "security"},
			step: &Step{ID: "audit", Labels: []string{"review"}},
			want: false,
		},
		{
			name: "combined match",
			pc:   &Pointcut{Glob: "*.implement", Type: "task"},
			step: &Step{ID: "shiny.implement", Type: "task"},
			want: true,
		},
		{
			name: "combined partial fail",
			pc:   &Pointcut{Glob: "*.implement", Type: "task"},
			step: &Step{ID: "shiny.implement", Type: "bug"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchPointcut(tt.pc, tt.step)
			if got != tt.want {
				t.Errorf("MatchPointcut() = %v, want %v", got, tt.want)
			}
		})
	}
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// TestApplyAdvice_SelfMatchingPrevention verifies that advice doesn't match
// steps it inserted (gt-8tmz.16).
func TestApplyAdvice_SelfMatchingPrevention(t *testing.T) {
	// An advice rule with a broad pattern that would match its own insertions
	// if self-matching weren't prevented.
	advice := []*AdviceRule{
		{
			Target: "*", // Matches everything
			Around: &AroundAdvice{
				Before: []*AdviceStep{
					{ID: "{step.id}-before", Title: "Before {step.id}"},
				},
				After: []*AdviceStep{
					{ID: "{step.id}-after", Title: "After {step.id}"},
				},
			},
		},
	}

	steps := []*Step{
		{ID: "implement", Title: "Implement"},
	}

	result := ApplyAdvice(steps, advice)

	// Without self-matching prevention, pattern "*" would match the inserted
	// "implement-before" and "implement-after" steps, causing them to also
	// get before/after steps, leading to potential infinite expansion.
	// With prevention, we should only get the original step + its advice.

	// Expected: implement-before, implement, implement-after (3 steps)
	if len(result) != 3 {
		t.Errorf("ApplyAdvice() produced %d steps, want 3. Got IDs: %v",
			len(result), getStepIDs(result))
	}

	expectedIDs := []string{"implement-before", "implement", "implement-after"}
	for i, want := range expectedIDs {
		if result[i].ID != want {
			t.Errorf("result[%d].ID = %q, want %q", i, result[i].ID, want)
		}
	}
}

func getStepIDs(steps []*Step) []string {
	ids := make([]string, len(steps))
	for i, s := range steps {
		ids[i] = s.ID
	}
	return ids
}

func TestApplyAdviceWithBudget_ChainedAspectsRefusedPastLimit(t *testing.T) {
	steps := []*Step{{ID: "work", Title: "Work"}}
	budget := NewExpansionBudget("aspect-bomb")

	var err error
	for i := 0; i < 20; i++ {
		advice := []*AdviceRule{{
			Target: "*",
			Before: &AdviceStep{
				ID:    fmt.Sprintf("aspect%d-{step.id}", i),
				Title: "Before {step.id}",
			},
		}}
		steps, err = ApplyAdviceWithBudget(steps, advice, budget)
		if err != nil {
			break
		}
	}
	if !errors.Is(err, ErrExpansionLimit) {
		t.Fatalf("err = %v, want ErrExpansionLimit", err)
	}
	if !strings.Contains(err.Error(), `formula "aspect-bomb"`) {
		t.Errorf("err = %q, want formula name", err)
	}
	if budget.used > MaxExpandedSteps {
		t.Errorf("budget.used = %d, want <= %d", budget.used, MaxExpandedSteps)
	}
}

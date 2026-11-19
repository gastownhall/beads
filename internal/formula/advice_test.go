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

	result, err := ApplyAdvice(steps, advice)
	if err != nil {
		t.Fatalf("ApplyAdvice() error = %v", err)
	}

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

	result, err := ApplyAdvice(steps, advice)
	if err != nil {
		t.Fatalf("ApplyAdvice() error = %v", err)
	}

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

	result, err := ApplyAdvice(steps, advice)
	if err != nil {
		t.Fatalf("ApplyAdvice() error = %v", err)
	}

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

	result, err := ApplyAdvice(steps, advice)
	if err != nil {
		t.Fatalf("ApplyAdvice() error = %v", err)
	}

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

	result, err := ApplyAdvice(steps, advice)
	if err != nil {
		t.Fatalf("ApplyAdvice() error = %v", err)
	}

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

	result, err := ApplyAdvice(steps, nil)
	if err != nil {
		t.Fatalf("ApplyAdvice() error = %v", err)
	}

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

	result, err := ApplyAdvice(steps, advice)
	if err != nil {
		t.Fatalf("ApplyAdvice() error = %v", err)
	}

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

// bd-xx3ma9 (#7882): advice insertions are bounded like every other cook
// transform. 5001 matched steps x 2 insertions each is 10002 advice steps,
// past MaxExpandedSteps on its own — the fresh-budget wrapper refuses before
// the over-budget steps enter the graph.
func TestApplyAdvice_OverLimitRefusesBeforeInserting(t *testing.T) {
	steps := make([]*Step, 0, 5001)
	for i := 0; i < 5001; i++ {
		steps = append(steps, &Step{ID: fmt.Sprintf("work.step%d", i), Title: "Work"})
	}
	advice := []*AdviceRule{{
		Target: "work.*",
		Before: &AdviceStep{ID: "pre-{step.id}", Title: "Pre"},
		After:  &AdviceStep{ID: "post-{step.id}", Title: "Post"},
	}}

	result, err := ApplyAdvice(steps, advice)
	if !errors.Is(err, ErrExpansionLimit) {
		t.Fatalf("ApplyAdvice() error = %v, want ErrExpansionLimit", err)
	}
	if result != nil {
		t.Errorf("ApplyAdvice() returned %d steps with the refusal; want nil", len(result))
	}
	if !strings.Contains(err.Error(), `advice around "work.`) {
		t.Errorf("refusal should name the advice insertion it refused: %v", err)
	}
}

// Two aspects applied one after the other share one budget: the second is
// refused when the first already spent the room. This is the seam cook.go
// uses to thread the aspects through the same limit as the rest of the cook.
func TestApplyAdviceWithBudget_AspectsShareOneBudget(t *testing.T) {
	steps := []*Step{
		{ID: "design", Title: "Design"},
		{ID: "implement", Title: "Implement"},
		{ID: "test", Title: "Test"},
	}
	loggingAspect := []*AdviceRule{{
		Target: "implement",
		Before: &AdviceStep{ID: "log-{step.id}", Title: "Log"},
	}}
	securityAspect := []*AdviceRule{{
		Target: "implement",
		Around: &AroundAdvice{
			Before: []*AdviceStep{{ID: "sec-pre-{step.id}", Title: "Sec pre"}},
			After:  []*AdviceStep{{ID: "sec-post-{step.id}", Title: "Sec post"}},
		},
	}}

	budget := NewStepBudgetWithLimit(4)
	if err := budget.Seed(steps); err != nil {
		t.Fatalf("Seed() error = %v", err)
	}
	first, err := ApplyAdviceWithBudget(steps, loggingAspect, budget)
	if err != nil {
		t.Fatalf("first aspect: %v", err)
	}
	// The logging aspect spent the only room: 3 seeded + 1 inserted, and the
	// security aspect's around adds 2 more.
	if _, err := ApplyAdviceWithBudget(first, securityAspect, budget); !errors.Is(err, ErrExpansionLimit) {
		t.Fatalf("second aspect error = %v, want ErrExpansionLimit", err)
	}
}

// Advice into a step's children counts against the same budget as the advice
// around the parent: a recursion that gave each nesting level a fresh budget
// would pass where the shared one must refuse.
func TestApplyAdvice_ChildrenShareTheBudget(t *testing.T) {
	parent := &Step{ID: "parent", Title: "Parent", Children: []*Step{
		{ID: "parent.child1", Title: "Child 1"},
		{ID: "parent.child2", Title: "Child 2"},
	}}
	advice := []*AdviceRule{{
		Target: "*",
		Before: &AdviceStep{ID: "pre-{step.id}", Title: "Pre"},
		After:  &AdviceStep{ID: "post-{step.id}", Title: "Post"},
	}}

	// Room for everything but the last child's second insertion: 3 seeded +
	// 2 (parent) + 2 (child1) + 2 (child2) = 9 > 8.
	budget := NewStepBudgetWithLimit(8)
	if err := budget.Seed([]*Step{parent}); err != nil {
		t.Fatalf("Seed() error = %v", err)
	}
	_, err := ApplyAdviceWithBudget([]*Step{parent}, advice, budget)
	if !errors.Is(err, ErrExpansionLimit) {
		t.Fatalf("ApplyAdviceWithBudget() error = %v, want ErrExpansionLimit", err)
	}
	if !strings.Contains(err.Error(), `advice around "parent.child2"`) {
		t.Errorf("refusal should name the step whose advice was refused: %v", err)
	}
}

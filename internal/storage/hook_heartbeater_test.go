package storage

import (
	"context"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

type stubHeartbeater struct{ result issueops.HeartbeatResult }

func (s stubHeartbeater) Heartbeat(context.Context, issueops.HeartbeatRequest) (issueops.HeartbeatResult, error) {
	return s.result, nil
}

// TestHookHeartbeaterFiresOnlyForARearm pins hook_heartbeater.go: a refresh
// fires nothing, a re-arm (a mutation) fires on_update once.
func TestHookHeartbeaterFiresOnlyForARearm(t *testing.T) {
	for _, test := range []struct {
		name    string
		rearmed bool
		want    []string
	}{
		{"a refresh fires nothing", false, nil},
		{"a re-arm fires on_update", true, []string{"update-by-id:bd-1"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := &recordingIssueOperationHooks{}
			hooked := &hookHeartbeater{
				inner: stubHeartbeater{result: issueops.HeartbeatResult{IssueID: "bd-1", Rearmed: test.rearmed}},
				hooks: recorder,
			}
			if _, err := hooked.Heartbeat(t.Context(), issueops.HeartbeatRequest{Actor: "w", IssueID: "bd-1"}); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(recorder.completions, test.want) {
				t.Fatalf("completions = %v, want %v", recorder.completions, test.want)
			}
		})
	}
}

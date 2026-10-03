package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/issueops"
)

type fakeVersionLister struct {
	storage.DoltStorage
	called   bool
	versions []storage.IssueVersion
}

func (f *fakeVersionLister) ListVersions(_ context.Context, _ string) ([]storage.IssueVersion, error) {
	f.called = true
	return f.versions, nil
}

type notAVersionLister struct{ storage.DoltStorage }

// settingsVersionLister is a store with both halves `bd versions` reads: a
// settings plane whose row is canned, and a version history that is canned.
// The nil storage.DoltStorage underneath makes anything else the command
// touches panic, so a case cannot pass through a path it did not mean to
// exercise.
type settingsVersionLister struct {
	fakeVersionLister
	cfg issueops.WorkspaceConfig
}

func (s *settingsVersionLister) WorkspaceConfig() (issueops.WorkspaceConfig, error) {
	return s.cfg, nil
}

// TestVersionsOffButRecordedStillLists is finding 3 from bee's #6661 review:
// a store that recorded for a month and was then switched off HAS versions,
// and refusing with "nothing is recorded" states a fact about this
// invocation's config as if it were a fact about the store.
func TestVersionsOffButRecordedStillLists(t *testing.T) {
	backend := &fakeVersionLister{versions: []storage.IssueVersion{{Revision: 3}, {Revision: 2}}}

	got, err := runVersions(context.Background(), backend, "bd-1", false, true)

	if err != nil {
		t.Fatalf("runVersions(recording=false, 2 rows) = %v, want the rows", err)
	}
	if len(got.Versions) != 2 {
		t.Fatalf("got %d versions, want 2 — recorded history must survive the switch going off", len(got.Versions))
	}
	if got.Recording {
		t.Error("Recording should be false so the caller can say the listing ends where recording stopped")
	}
}

// TestVersionsOffAndEmptyRefusesWithRemedy is the empty arm of the pair above,
// and together they are the whole of finding 3: recording off decides the
// FOOTER, never whether to read. Off with rows recorded lists them; off with
// nothing recorded is the one case that refuses, and it refuses with the
// remedy rather than returning an empty list.
//
// That distinction is what #5898 exists to make. An empty list is a claim
// about the bead ("it has no versions"); the truth here is a claim about the
// store ("it never recorded any"). Returning the former for the latter is the
// wrong-answer-not-an-empty-one failure.
//
// This was TestVersionsRefusesWhenFlagOff, which asserted that the read was
// refused BEFORE the backend was consulted. Finding 3 removed that behaviour
// deliberately, so the assertion went with it -- but the fixture it had been
// asserting against stayed behind, built, unused and ending in `_ = backend`,
// under a name that still promised the old contract. Renamed to what it
// actually pins, and the dead fixture is gone.
// (bee-ghosttrack, #6661 third review, finding 3.)
func TestVersionsOffAndEmptyRefusesWithRemedy(t *testing.T) {
	got, err := runVersions(context.Background(), &fakeVersionLister{}, "bd-1", false, true)

	if !errors.Is(err, errVersionedHistoryOff) {
		t.Fatalf("runVersions(recording=false, empty) error = %v, want errVersionedHistoryOff", err)
	}
	if len(got.Versions) != 0 {
		t.Errorf("returned %d versions, want none", len(got.Versions))
	}
}

// TestVersionsUnresolvedIDIsNotAnEmptyBead is finding 4: falling through on an
// unresolved id is right (a deleted bead can still have versions), but only
// until the answer is empty. Then it is "no such bead", not "none yet".
func TestVersionsUnresolvedIDIsNotAnEmptyBead(t *testing.T) {
	_, err := runVersions(context.Background(), &fakeVersionLister{}, "nosuch-id", true, false)

	if !errors.Is(err, errNoSuchBead) {
		t.Fatalf("runVersions(resolved=false, empty) error = %v, want errNoSuchBead", err)
	}
}

// TestVersionsRefusesUnsupportedBackend pins that "this backend cannot answer"
// is distinguishable from "there are no versions" -- the third shape of an
// answer, not a smaller success.
func TestVersionsRefusesUnsupportedBackend(t *testing.T) {
	_, err := runVersions(context.Background(), &notAVersionLister{}, "bd-1", true, true)

	if !errors.Is(err, errVersionsUnsupported) {
		t.Fatalf("runVersions(unsupported backend) error = %v, want errVersionsUnsupported", err)
	}
	if errors.Is(err, errVersionedHistoryOff) {
		t.Error("an unsupported backend must not be reported as the feature being off: different fixes")
	}
}

// TestVersionsEmptyIsNotAnError pins the other side: a supported, enabled
// store with nothing recorded returns an empty list and no error. That is a
// truthful answer, and the command renders the no-backfill explanation.
func TestVersionsEmptyIsNotAnError(t *testing.T) {
	got, err := runVersions(context.Background(), &fakeVersionLister{}, "bd-1", true, true)
	if err != nil {
		t.Fatalf("runVersions on an empty store = %v, want nil", err)
	}
	if len(got.Versions) != 0 {
		t.Errorf("got %d versions, want 0", len(got.Versions))
	}
}

// TestVersionsDecidesRecordingFromTheStoreNotTheProcessEnvironment is the
// reviewer's probe on #6661 (finding 2): BD_VERSIONED_HISTORY_ENABLED=1, the
// store's own setting unset, nothing recorded.
//
// `bd versions` only reads, so the variable cannot make the store record
// anything; it can only make THIS invocation claim that the store does. With
// the environment ORed in, a store that never recorded answered "No versions
// recorded for <id> yet" -- the empty list this command's three outcomes exist
// to prevent, because an empty list is a claim about the bead and the truth is
// a claim about the store. The variable still turns recording on for the writes
// of a process that has it; what this command reports is the store's own
// setting.
//
// The rows that already pass stay in the table on purpose. They are the other
// half of the claim: a store whose setting says on keeps answering "none yet",
// and the variable never switches that off.
func TestVersionsDecidesRecordingFromTheStoreNotTheProcessEnvironment(t *testing.T) {
	for _, tt := range []struct {
		name        string
		env         string
		row         string
		wantRefusal bool
	}{
		{"env on, row unset (the reviewer's probe)", "1", "", true},
		{"env on, row false", "1", "false", true},
		{"env off, row unset", "", "", true},
		{"env on, row true", "1", "true", false},
		{"env off, row true", "", "true", false},
		{"env 0 does not switch off a row that says true", "0", "true", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// The variable has to be in place before viper is initialised, and
			// viper has to be initialised at all: config.GetBool answers false
			// until it is, which would make every env-on row pass whatever the
			// code did with the variable.
			t.Setenv("BD_VERSIONED_HISTORY_ENABLED", tt.env)
			initConfigForTest(t)
			st := &settingsVersionLister{cfg: &settingsPlane{value: tt.row}}

			recording := versionedHistoryEnabled(context.Background(), st)
			got, err := runVersions(context.Background(), st, "bd-1", recording, true)

			if tt.wantRefusal {
				if !errors.Is(err, errVersionedHistoryOff) {
					t.Fatalf("env=%q store row=%q, nothing recorded: runVersions = (%+v, %v), want errVersionedHistoryOff: the process environment must not make a store that never recorded look like one that is recording", tt.env, tt.row, got, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("env=%q store row=%q, nothing recorded: runVersions error = %v, want an empty listing", tt.env, tt.row, err)
			}
			if !got.Recording {
				t.Errorf("env=%q store row=%q: Recording = false, want true for a store whose own setting says on", tt.env, tt.row)
			}
		})
	}
}

// TestVersionsHelpDoesNotOfferTheEnvironmentAsAReadSwitch is the other half of
// finding 2. The help text told people to run
// `BD_VERSIONED_HISTORY_ENABLED=1 bd versions <id>` "for one run". A read
// records nothing, so that line could only ever make the answer wrong; and
// once the command reports the store's own setting it does not work at all.
func TestVersionsHelpDoesNotOfferTheEnvironmentAsAReadSwitch(t *testing.T) {
	if strings.Contains(versionsCmd.Long, "BD_VERSIONED_HISTORY_ENABLED=1 bd versions") {
		t.Errorf("bd versions help still suggests BD_VERSIONED_HISTORY_ENABLED=1 bd versions <id>; a read cannot record anything, so the variable cannot make it answer for a store that never recorded.\nhelp:\n%s", versionsCmd.Long)
	}
}

// TestVersionListerPeelsDecorators is the regression guard for the bug this
// command shipped with first: cmd/bd holds the DECORATED store, VersionLister
// is an optional capability none of the decorators forwards, and asserting
// against the wrapper reported "unsupported" for a store that could serve
// versions. The capability must be found through the decorator chain.
func TestVersionListerPeelsDecorators(t *testing.T) {
	inner := &fakeVersionLister{versions: []storage.IssueVersion{{Revision: 7}}}
	decorated := storage.NewHookFiringStore(inner, nil)

	lister, ok := versionListerFor(decorated)
	if !ok {
		t.Fatal("versionListerFor could not see through the decorator chain")
	}
	got, err := lister.ListVersions(context.Background(), "bd-1")
	if err != nil {
		t.Fatalf("ListVersions through decorator = %v", err)
	}
	if len(got) != 1 || got[0].Revision != 7 {
		t.Errorf("got %+v, want the inner store's revision 7", got)
	}
}

// TestRestrictionLabelsAreDistinct pins that the four removal reasons render
// as four different sentences. Collapsing them into one "gone" would erase
// the distinction R10 requires, and in particular "unknown" (this store has
// no lineage knowledge) is NOT "gone" -- the conformance suite pins that a
// never-synced store answers Unknown rather than Gone.
func TestRestrictionLabelsAreDistinct(t *testing.T) {
	seen := map[string]string{}
	for _, r := range []string{"gone_retention", "gone_erasure", "gone_reorganization", "unknown"} {
		label := restrictionLabel(r)
		if label == "" {
			t.Errorf("restrictionLabel(%q) is empty", r)
		}
		if prev, dup := seen[label]; dup {
			t.Errorf("restrictionLabel(%q) and (%q) both render %q; they are different answers", r, prev, label)
		}
		seen[label] = r
	}
	if restrictionLabel("unknown") == restrictionLabel("gone_erasure") {
		t.Error("unknown must not read as gone: no lineage knowledge is not erasure")
	}
}

// TestRestrictionLabelAnswersAnythingOutsideTheVocabularyAsUnknown is finding 3 of the
// #6661 review, on the label's side. The as-of read answers a stored restriction outside
// the four-value vocabulary as unknown, and so is "live", which is never stored; the
// label printed such a string raw, so a listing could show "quarantined" for a version
// the as-of read calls unknown. It renders as unknown does.
func TestRestrictionLabelAnswersAnythingOutsideTheVocabularyAsUnknown(t *testing.T) {
	want := restrictionLabel("unknown")
	for _, r := range []string{"", "live", "gone-retention", "quarantined"} {
		if got := restrictionLabel(r); got != want {
			t.Errorf("restrictionLabel(%q) = %q, want %q (what unknown renders as): a value outside the vocabulary is not an answer", r, got, want)
		}
	}
}

// TestVersionsJSONDoesNotCollideWithIssueRevision pins that the versions
// payload does not emit a "revision" key.
//
// types.Issue already ships `json:"revision"` and it is the row-lock CAS
// token -- an unrelated value, and a STRING where this one is a number. One
// key carrying two meanings, in the place machines read, is the `bd version`
// collision again with worse consequences: a script that learned one shape
// gets the other and cannot tell. Raised by bee-ghosttrack's consumer read on
// #5898, who reported a couple of hundred script files parsing bd --json.
func TestVersionsJSONDoesNotCollideWithIssueRevision(t *testing.T) {
	blob, err := json.Marshal(storage.IssueVersion{Revision: 7, IssueID: "bd-1"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var generic map[string]any
	if err := json.Unmarshal(blob, &generic); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if _, collides := generic["revision"]; collides {
		t.Errorf(`IssueVersion emits a "revision" key; types.Issue already uses that key for the row-lock token. Got: %s`, blob)
	}
	got, ok := generic["local_revision"]
	if !ok {
		t.Fatalf(`IssueVersion has no "local_revision" key. Got: %s`, blob)
	}
	if n, isNum := got.(float64); !isNum || n != 7 {
		t.Errorf("local_revision = %v, want 7", got)
	}
}

// TestVersionsJSONIsOneObjectWhetherOrNotAnythingWasRecorded is finding 4:
// `bd versions <id> --json` printed `null` for a store with nothing recorded
// yet (a nil slice through outputJSON) and never said whether recording was on,
// so a JSON consumer could tell neither "empty" from "no such list" nor a
// listing that ends where recording was switched off from one that is current.
//
// The shape is one object for every answer that is not an error:
// {"versions": [...], "recording": bool}, with [] for the empty list, never
// null. It goes through outputJSON, the way the command does, so the check sees
// what a script sees.
func TestVersionsJSONIsOneObjectWhetherOrNotAnythingWasRecorded(t *testing.T) {
	t.Setenv("BD_JSON_ENVELOPE", "")
	rows := []storage.IssueVersion{{Revision: 2, IssueID: "bd-1"}, {Revision: 1, IssueID: "bd-1"}}

	for _, tt := range []struct {
		name         string
		backend      *fakeVersionLister
		recording    bool
		wantVersions int
	}{
		{"nothing recorded yet", &fakeVersionLister{}, true, 0},
		{"recorded, recording on", &fakeVersionLister{versions: rows}, true, 2},
		{"recorded, recording since switched off", &fakeVersionLister{versions: rows}, false, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := runVersions(context.Background(), tt.backend, "bd-1", tt.recording, true)
			if err != nil {
				t.Fatalf("runVersions: %v", err)
			}

			out := captureStdout(t, func() error { return outputJSON(got) })

			var payload map[string]json.RawMessage
			if err := json.Unmarshal([]byte(out), &payload); err != nil {
				t.Fatalf("--json output is not one JSON object: %v\n%s", err, out)
			}

			rawVersions, ok := payload["versions"]
			if !ok {
				t.Fatalf(`--json output has no "versions" key. Got: %s`, out)
			}
			var list []json.RawMessage
			if err := json.Unmarshal(rawVersions, &list); err != nil {
				t.Fatalf(`"versions" is not an array: %v. Got: %s`, err, out)
			}
			if list == nil {
				t.Errorf(`"versions" is null, want [] for an empty list. Got: %s`, out)
			}
			if len(list) != tt.wantVersions {
				t.Errorf(`"versions" has %d entries, want %d. Got: %s`, len(list), tt.wantVersions, out)
			}

			rawRecording, ok := payload["recording"]
			if !ok {
				t.Fatalf(`--json output has no "recording" key. Got: %s`, out)
			}
			var recording bool
			if err := json.Unmarshal(rawRecording, &recording); err != nil {
				t.Fatalf(`"recording" is not a boolean: %v. Got: %s`, err, out)
			}
			if recording != tt.recording {
				t.Errorf(`"recording" = %v, want %v. Got: %s`, recording, tt.recording, out)
			}
		})
	}
}

// TestVersionWithAnArgumentPointsAtVersions pins the guard on `bd version`.
//
// `bd version` prints bd's own build and `bd versions <id>` lists a bead's
// recorded versions: one character apart, unrelated meanings. Before the guard,
// a mistyped `bd version <id>` silently printed the build and exited 0, which
// reads as "this bead has no versions". It now exits 1 and names the command
// that was probably meant (#6661 review, finding 8). The guard is deliberate,
// and it is a change from the exit 0 it replaced, so both halves are pinned:
// the exit code and the hint, for the argument the user typed.
//
// The hint is matched as two fragments rather than one string so that spacing
// inside the message can change without losing the pin.
func TestVersionWithAnArgumentPointsAtVersions(t *testing.T) {
	pinJSONOutput(t, false)

	var err error
	stderr := captureStderr(t, func() {
		err = versionCmd.RunE(versionCmd, []string{"bd-123"})
	})

	code, isExitError := exitCodeFromError(err)
	if !isExitError || code != 1 {
		t.Fatalf("bd version bd-123 returned %v (exit error: %v, code: %d), want an exit error with code 1", err, isExitError, code)
	}
	for _, want := range []string{"Did you mean", "bd versions bd-123"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("bd version bd-123 stderr is missing %q. Got: %q", want, stderr)
		}
	}
}

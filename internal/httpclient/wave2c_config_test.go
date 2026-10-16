// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/wave2c_config_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// The unit half of client wave 2c's config writes: what the client SENDS, and
// which refusals it raises without dialing at all.
//
// The served tier runs the whole twenty-two-case contract and three pins of its
// own, so what is left here is the two facts a correct server hides. A client
// that skipped its own validation would still pass every contract case — the
// server refuses the same requests, at its edge or in its role — while
// spending a round trip to be told what the role's own contract already calls
// invalid, and binding the classification to a problem body a future server
// release might spell differently. And a client that sent the wrong METHOD or
// the wrong PATH would fail loudly against a server but says nothing about
// which of the two was wrong; a recorded request does.

// TestSettingWriteValidationMatchesTheSharedValidator is the drift pin for the
// one validator this package had to copy.
//
// depguard denies internal/workapi to this package — a client that could build a
// filter is a client whose narrowing no server-side gate can observe — but not
// to a test file, which is the exemption releaser.go and counter.go already use
// for the same reason. Driving both bodies over one table is what makes them
// agree case by case rather than in prose.
//
// THE VALUE IS PART OF THE TABLE, not only the key, because three of the rules
// are about a value: its length, counted in BYTES where the key's is counted in
// characters; `status.custom`, which is projected into a lookup table reads
// consult first, so a value that cannot be projected must not become a row; and
// `types.retained`, which is read by every sweep, so a value the type-list
// parse would read as other than the list written must not become one either.
// That rule calls the shared validator through its storageops delegate rather
// than restating it; its rows still put a refused and an accepted value on each
// of the validator's rules, so a client that stopped calling it, or called it
// on the wrong key, fails a row.
func TestSettingWriteValidationMatchesTheSharedValidator(t *testing.T) {
	for _, tc := range []struct {
		name  string
		key   string
		value string
	}{
		{"an ordinary key", "notes", "core"},
		{"an empty key", "", "x"},
		{"a blank key", "   ", "x"},
		{"the protected key", issueops.SettingKeyIssuePrefix, "bd"},
		{"the protected key, dashed", settingKeyIssuePrefixDashed, "bd"},
		{"a parseable custom status", issueops.SettingKeyStatusCustom, "awaiting_review:active"},
		{"an unparseable custom status", issueops.SettingKeyStatusCustom, "open"},
		{"an empty custom status", issueops.SettingKeyStatusCustom, ""},
		{"a custom type list", issueops.SettingKeyTypesCustom, "not:a:status"},
		{"a key that only LOOKS protected", "issue_prefix_backup", "bd"},
		{"an empty retained value", issueops.SettingKeyTypesRetained, ""},
		{"a blank retained value", issueops.SettingKeyTypesRetained, "  "},
		{"an empty retained array", issueops.SettingKeyTypesRetained, "[]"},
		{"a retained built-in", issueops.SettingKeyTypesRetained, "chore"},
		{"a retained JSON array of built-ins", issueops.SettingKeyTypesRetained, `["chore","task"]`},
		{"a retained comma list of custom types", issueops.SettingKeyTypesRetained, "op, fact"},
		{"a retained mixed-case custom type", issueops.SettingKeyTypesRetained, "OpRecord"},
		{"a retained built-in in another case", issueops.SettingKeyTypesRetained, "Chore"},
		{"a retained alias", issueops.SettingKeyTypesRetained, "feat"},
		{"a retained null", issueops.SettingKeyTypesRetained, "null"},
		{"a retained lone comma", issueops.SettingKeyTypesRetained, ","},
		{"a retained empty name", issueops.SettingKeyTypesRetained, `[""]`},
		{"a retained unclosed bracket", issueops.SettingKeyTypesRetained, "["},
		{"a retained bracketed list that is not JSON", issueops.SettingKeyTypesRetained, "[op]"},
		{"a retained array of numbers", issueops.SettingKeyTypesRetained, "[1]"},
		{"a retained name with a space", issueops.SettingKeyTypesRetained, "op fact"},
		{"a retained quoted name", issueops.SettingKeyTypesRetained, `"op"`},
		{"a key of exactly the column's characters", strings.Repeat("\u00e9", types.MaxFieldLen), "v"},
		{"a key past the column", strings.Repeat("k", types.MaxFieldLen+1), "v"},
		{"a value of exactly the column's bytes", "custom.big", strings.Repeat("v", types.MaxTextBytes)},
		{"a value past the column", "custom.big", strings.Repeat("v", types.MaxTextBytes+1)},
		{"a value past the column in bytes but not in characters", "custom.big", strings.Repeat("\u00e9", types.MaxTextBytes/2+1)},
		// config.IsYamlOnlyKey, GH#7559: exact-match (a tracker secret) and
		// prefix-match (a whole family, e.g. routing.*) both refuse here now.
		{"a yaml-only key, exact match", "github.token", "ghp_x"},
		{"a yaml-only key, prefix match", "routing.default", "core"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mine := validateSettingWrite(tc.key, tc.value)
			_, theirs := workapi.ValidateSettingWrite(tc.key, tc.value)
			if (mine == nil) != (theirs == nil) {
				t.Fatalf("validateSettingWrite(%q, %q) = %v but workapi.ValidateSettingWrite = %v: "+
					"this leg refuses a different set from the one every other leg refuses", tc.key, tc.value, mine, theirs)
			}
			if mine != nil && !errors.Is(mine, issueops.ErrValidation) {
				t.Errorf("validateSettingWrite(%q, %q) = %v, which does not match ErrValidation", tc.key, tc.value, mine)
			}
		})
	}
}

// TestSettingKeyValidationMatchesTheSharedValidator is the same pin for the
// narrower rule the READ and the REMOVE share.
//
// It is a separate case because the two validators refuse different sets on
// purpose: the protected key is refused on a write and NOT on a remove, which is
// shipped behavior on every implementation (bd-yby99.34) rather than something
// this client decides. A single table would have hidden that.
func TestSettingKeyValidationMatchesTheSharedValidator(t *testing.T) {
	for _, key := range []string{"routing.default", "", "   ", issueops.SettingKeyIssuePrefix, settingKeyIssuePrefixDashed} {
		mine := validateSettingKey(key)
		_, theirs := workapi.ValidateSettingKey(key)
		if (mine == nil) != (theirs == nil) {
			t.Errorf("validateSettingKey(%q) = %v but workapi.ValidateSettingKey = %v", key, mine, theirs)
		}
		if mine != nil && !errors.Is(mine, issueops.ErrValidation) {
			t.Errorf("validateSettingKey(%q) = %v, which does not match ErrValidation", key, mine)
		}
	}
}

// TestConfigWritesRefuseBeforeTheDial is the ordering half of the rule above.
//
// Agreeing with the shared validator is worth nothing if the refusal happens
// after the request has already been sent: the point of restating a role rule is
// that an invalid request does not consume a write slot to be told so. This
// drives every refusing shape against a recording transport and asserts nothing
// left the process.
func TestConfigWritesRefuseBeforeTheDial(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(issueops.WorkspaceConfig) error
	}{
		{"set with an empty key", func(c issueops.WorkspaceConfig) error {
			_, err := c.SetSetting(context.Background(), issueops.SetSettingRequest{Key: "  ", Value: "x"})
			return err
		}},
		{"set of the protected key", func(c issueops.WorkspaceConfig) error {
			_, err := c.SetSetting(context.Background(), issueops.SetSettingRequest{Key: issueops.SettingKeyIssuePrefix, Value: "bd"})
			return err
		}},
		{"set of an unparseable custom status", func(c issueops.WorkspaceConfig) error {
			_, err := c.SetSetting(context.Background(), issueops.SetSettingRequest{Key: issueops.SettingKeyStatusCustom, Value: "open"})
			return err
		}},
		{"set of a retained value that names no type", func(c issueops.WorkspaceConfig) error {
			_, err := c.SetSetting(context.Background(), issueops.SetSettingRequest{Key: issueops.SettingKeyTypesRetained, Value: "null"})
			return err
		}},
		{"set of a value past the column", func(c issueops.WorkspaceConfig) error {
			_, err := c.SetSetting(context.Background(), issueops.SetSettingRequest{Key: "custom.big", Value: strings.Repeat("v", types.MaxTextBytes+1)})
			return err
		}},
		{"set of a key past the column", func(c issueops.WorkspaceConfig) error {
			_, err := c.SetSetting(context.Background(), issueops.SetSettingRequest{Key: "custom." + strings.Repeat("k", types.MaxFieldLen), Value: "v"})
			return err
		}},
		{"unset with an empty key", func(c issueops.WorkspaceConfig) error {
			_, err := c.UnsetSetting(context.Background(), issueops.UnsetSettingRequest{Key: ""})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, w := recordingStore(t)
			settings, err := store.WorkspaceConfig()
			if err != nil {
				t.Fatalf("WorkspaceConfig(): %v", err)
			}
			if err := tc.call(settings); !errors.Is(err, issueops.ErrValidation) {
				t.Fatalf("refusal = %v, want ErrValidation", err)
			}
			if len(w.dispatched) != 0 {
				t.Errorf("the refused request still dialed %v; a request the role calls invalid must not reach the server", w.dispatched)
			}
		})
	}
}

// TestConfigWritesDialTheKeyInThePath pins the two requests member for member:
// the verb, the path the key becomes, and the body that carries the value alone.
//
// The KEY IS ESCAPED into one segment, which is why the case uses one that would
// otherwise change the request's shape. A slash would push the segment onto a
// path the server routes elsewhere, and a colon would make the custom-method
// split ambiguous — both are escapeSegment's job, and both are invisible against
// a server that simply 404s.
func TestConfigWritesDialTheKeyInThePath(t *testing.T) {
	store, w := recordingStore(t)
	settings, err := store.WorkspaceConfig()
	if err != nil {
		t.Fatalf("WorkspaceConfig(): %v", err)
	}
	const key = "routing/mode:beta"

	if _, err := settings.SetSetting(context.Background(), issueops.SetSettingRequest{Key: key, Value: "core"}); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	set := w.requests[len(w.requests)-1]
	if set.Op != wire.OpSetSetting || set.Method != http.MethodPut {
		t.Errorf("the write dialed %s %s, want %s PUT", set.Op, set.Method, wire.OpSetSetting)
	}
	if want := "/v0/beads/config/routing%2Fmode%3Abeta"; set.Path != want {
		t.Errorf("the write dialed %q, want %q", set.Path, want)
	}
	if len(set.Query) != 0 {
		t.Errorf("the write carried a query (%v); this operation takes none and refuses one", set.Query)
	}
	body, ok := set.Body.(apigen.SetSettingRequest)
	if !ok {
		t.Fatalf("the write body is %T, want the generated SetSettingRequest", set.Body)
	}
	if body.Value != "core" {
		t.Errorf("the write body carried %q, want %q", body.Value, "core")
	}

	if _, err := settings.UnsetSetting(context.Background(), issueops.UnsetSettingRequest{Key: key}); err != nil {
		t.Fatalf("UnsetSetting: %v", err)
	}
	unset := w.requests[len(w.requests)-1]
	if unset.Op != wire.OpUnsetSetting || unset.Method != http.MethodDelete {
		t.Errorf("the remove dialed %s %s, want %s DELETE", unset.Op, unset.Method, wire.OpUnsetSetting)
	}
	if unset.Path != set.Path {
		t.Errorf("the remove dialed %q and the write dialed %q; both name one resource", unset.Path, set.Path)
	}
	if unset.Body != nil {
		t.Errorf("the remove carried a body (%v); the key is the whole request", unset.Body)
	}
}

// TestSetSettingTranscribesTheAnswerRatherThanEchoingTheRequest is the arm no
// server can prove.
//
// The operation promises that the stored value equals the value sent for every
// key it accepts, so against a correct server a client that transcribed the
// answer and one that echoed its own request are byte-identical — which is
// exactly why the difference has to be pinned against a transport that answers
// something else. A client that echoed would report a value the workspace does
// not hold the day a server normalized one, and would do it silently.
//
// The REDACTED arm is the other half, and it is the one place the echo IS the
// right answer: the projection withholds the value it just accepted, so there is
// nothing to transcribe and the request is the only honest source. Both arms in
// one case so neither can be satisfied by a body that always does one of them.
//
// BOTH ARMS USE AN ORDINARY KEY. The redacted/not-redacted branch is driven by
// the fake answer's own Redacted field, not by the key name, and since GH#7559
// a real credential-bearing key (or any other config.IsYamlOnlyKey key) never
// reaches this transport at all — validateSettingWrite refuses it client-side
// first. That doctrine has its own pin (TestSettingWriteValidationMatchesTheSharedValidator);
// this case is only about what the client does with the answer once a write is
// accepted.
func TestSetSettingTranscribesTheAnswerRatherThanEchoingTheRequest(t *testing.T) {
	const sent = "what the caller sent"

	stored := "what the server stored"
	plain := &settingAnswerWire{answer: apigen.Setting{Key: "notes", Value: &stored}}
	res, err := settingsOver(t, plain).SetSetting(context.Background(),
		issueops.SetSettingRequest{Key: "notes", Value: sent})
	if err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if res.Value != stored {
		t.Errorf("SetSetting reported %q, want the answer's %q: the result is what the workspace holds, "+
			"and echoing the request would report a value it does not", res.Value, stored)
	}
	if res.Key != "notes" {
		t.Errorf("SetSetting reported key %q, want the answer's", res.Key)
	}

	withheld := &settingAnswerWire{answer: apigen.Setting{Key: "custom.thing", Redacted: true}}
	res, err = settingsOver(t, withheld).SetSetting(context.Background(),
		issueops.SetSettingRequest{Key: "custom.thing", Value: sent})
	if err != nil {
		t.Fatalf("SetSetting on an answer that redacts: %v", err)
	}
	if res.Value != sent {
		t.Errorf("SetSetting on a withheld answer reported %q, want the request's %q: \"\" would say the plane "+
			"stores nothing under a key it certainly stores something under", res.Value, sent)
	}
}

// settingAnswerWire is a transport that answers one prepared Setting, which is
// how the case above drives a server that says something the request did not.
type settingAnswerWire struct {
	recordingWire

	answer apigen.Setting
}

func (w *settingAnswerWire) Do(ctx context.Context, req wire.Request, out any) error {
	if err := w.recordingWire.Do(ctx, req, out); err != nil {
		return err
	}
	if body, ok := out.(*apigen.Setting); ok {
		*body = w.answer
	}
	return nil
}

func settingsOver(t *testing.T, w WireClient) issueops.WorkspaceConfig {
	t.Helper()
	settings, err := New(testTarget(t), w, &apigen.ContextResponse{}).WorkspaceConfig()
	if err != nil {
		t.Fatalf("WorkspaceConfig(): %v", err)
	}
	return settings
}

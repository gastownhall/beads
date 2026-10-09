// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/roles_aux.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The aggregate and workspace-state roles: StatsReporter whole, and
// WorkspaceConfig whole (design D8 rows 6 and 11).
//
// D8 ROW 11 IS NO LONGER PARTIAL. The row's per-METHOD refusal — an accessor
// that refused would have taken the reads down with the writes, and the reads
// are what `bd config get`, the routing probe and the id resolver's prefix
// lookups run on — was the posture for as long as v0 published no config write.
// Upstream #5596 published both verbs, client wave ga-jpywb dials them, and the
// role now answers every method it declares. What the split bought while it
// lasted is still worth knowing: a per-method refusal puts the method's own name
// in the sentinel, which is what RunUnsupportedContract and the D7 choke point
// both read, so the next partial role should be spelled the same way.
//
// The MEMORY plane is not here: its role is served whole — reads and writes —
// by memories.go, and a read-only twin alongside it would be a second answer to
// the same accessor.

// httpStatsReporter serves both summaries from getStats.
type httpStatsReporter struct{ store *Store }

// Stats answers the workspace-wide summary.
//
// SkipBlocked travels as the hint it is. The server forwards it to its own role,
// which may or may not take it, and the answer says which by whether
// BlockedIssues is populated — so this client neither promises the saving nor
// reports one, and reads the outcome off the same member every other caller
// does.
func (r httpStatsReporter) Stats(ctx context.Context, req issueops.StatsRequest) (issueops.StatsResult, error) {
	q := url.Values{}
	if req.SkipBlocked {
		q.Set("skip_blocked", "true")
	}
	return r.dial(ctx, q)
}

// AssigneeStats answers one actor's summary.
//
// The actor is used AS WRITTEN — no trimming, no folding, no alias expansion —
// on this side and on the server's.
func (r httpStatsReporter) AssigneeStats(ctx context.Context, req issueops.AssigneeStatsRequest) (issueops.StatsResult, error) {
	if strings.TrimSpace(req.Assignee) == "" {
		// The one refusal made here rather than dialed. An empty assignee would
		// be sent as an ABSENT parameter, which is the workspace-wide question —
		// a different answer, not an error — so the request has to be refused
		// before it becomes one.
		return issueops.StatsResult{}, fmt.Errorf("%w: an assignee must not be empty; the workspace-wide summary is Stats", issueops.ErrValidation)
	}
	q := url.Values{}
	q.Set("assignee", req.Assignee)
	return r.dial(ctx, q)
}

func (r httpStatsReporter) dial(ctx context.Context, q url.Values) (issueops.StatsResult, error) {
	var body apigen.StatsResponse
	if err := r.store.dispatch(ctx, wire.Request{
		Op:     wire.OpGetStats,
		Method: http.MethodGet,
		Path:   wire.PathStats,
		Query:  q,
	}, &body); err != nil {
		return issueops.StatsResult{}, err
	}
	return issueops.StatsResult{Summary: body.Summary}, nil
}

// httpWorkspaceConfig serves the read half of the settings role.
type httpWorkspaceConfig struct{ store *Store }

// GetSetting reads one stored setting.
//
// It goes through the raw GetConfig rather than dialing again, so this backend
// gives ONE answer per key. That matters for the redacted case: a withheld value
// is not the same fact as an unset one, and GetConfig reports it as
// RedactedSettingError — L9's "absent WITH REASON" — where returning "" would
// tell a caller the workspace stores nothing there. An unset key and a key
// stored empty stay conflated, which is the role contract's own rule.
func (c httpWorkspaceConfig) GetSetting(ctx context.Context, req issueops.GetSettingRequest) (issueops.SettingResult, error) {
	if strings.TrimSpace(req.Key) == "" {
		// Refused here because the operation carries the key in the PATH: an
		// empty segment would join to the collection and turn a read of one
		// setting into a read of all of them.
		return issueops.SettingResult{}, fmt.Errorf("%w: a setting key must not be empty", issueops.ErrValidation)
	}
	value, err := c.store.GetConfig(ctx, req.Key)
	if err != nil {
		return issueops.SettingResult{}, err
	}
	return issueops.SettingResult{Key: req.Key, Value: value}, nil
}

// ListSettings reads every stored setting.
//
// Redacted keys are PRESENT with an empty value, and this is the one place the
// withholding cannot be reported as an error: an enumeration that failed because
// one key of forty is credential-bearing would take the whole listing down.
// Omitting them instead would say the workspace stores nothing under that key,
// which is false and is the reading a caller enumerating configuration acts on —
// so the key is listed and its value is the empty string GetSetting refuses to
// pretend is real.
func (c httpWorkspaceConfig) ListSettings(ctx context.Context, _ issueops.ListSettingsRequest) (issueops.ListSettingsResult, error) {
	var body apigen.SettingsPage
	if err := c.store.dispatch(ctx, wire.Request{
		Op:     wire.OpListSettings,
		Method: http.MethodGet,
		Path:   wire.PathSettings,
	}, &body); err != nil {
		return issueops.ListSettingsResult{}, err
	}
	settings := make(map[string]string, len(body.Items))
	for _, item := range body.Items {
		settings[item.Key] = settingValue(item)
	}
	return issueops.ListSettingsResult{Settings: settings}, nil
}

// SetSetting stores one setting, on PUT /v0/beads/config/{key}.
//
// THE KEY IS THE PATH AND THE VALUE IS THE WHOLE BODY, which is what makes this
// the smallest write on the surface: one member out, one row back. There is no
// actor and no guard, because this plane records no history entry to attribute a
// write on and holds no row version to compare — so a write here is neither
// attributable nor conditional on ANY backend, and the wire is not narrowing
// anything by omitting them.
//
// IT IS NOT IDEMPOTENT-SHORT-CIRCUITED and must not become so. Storing the value
// already there still performs the write and its projection: the role's own
// contract says a no-op detection would make the repair of a normalized table
// that had drifted from its row depend on the row having changed, which is
// precisely the state that needs repairing. Nothing here compares first, and
// there is no `changed` member on the answer to compare against — the response
// to the second PUT is byte-identical to the first, which is the shape a PUT
// should have and is deliberately NOT the `already_claimed` / `already_closed`
// shape three other writes on this surface carry.
func (c httpWorkspaceConfig) SetSetting(ctx context.Context, req issueops.SetSettingRequest) (issueops.SetSettingResult, error) {
	if err := validateSettingWrite(req.Key, req.Value); err != nil {
		return issueops.SetSettingResult{}, err
	}
	path, err := wire.SettingPath(req.Key)
	if err != nil {
		return issueops.SetSettingResult{}, invalid("%v", err)
	}
	var body apigen.Setting
	if err := c.store.dispatch(ctx, wire.Request{
		Op:     wire.OpSetSetting,
		Method: http.MethodPut,
		Path:   path,
		Body:   apigen.SetSettingRequest{Value: req.Value},
	}, &body); err != nil {
		return issueops.SetSettingResult{}, err
	}
	return issueops.SetSettingResult{Key: body.Key, Value: storedSettingValue(req.Value, body)}, nil
}

// UnsetSetting removes one setting, on DELETE /v0/beads/config/{key}.
//
// REMOVING A KEY NOTHING SET SUCCEEDS, twice over: the role states an intended
// END STATE rather than an act performed, and the wire agrees — there is no 404
// on this operation at all, which is the one place it diverges from the memory
// delete beside it. So a caller clearing configuration it is not sure was ever
// written classifies no error to learn it was already absent.
//
// THE PROTECTED KEY IS NOT REFUSED HERE, unlike on the write above, and the
// asymmetry is shipped behavior on every implementation rather than something
// this client introduces (bd-yby99.34). Restating it as a refusal would make
// this leg stricter than the contract it implements, which is the one direction
// a client-side rule must never take.
func (c httpWorkspaceConfig) UnsetSetting(ctx context.Context, req issueops.UnsetSettingRequest) (issueops.UnsetSettingResult, error) {
	if err := validateSettingKey(req.Key); err != nil {
		return issueops.UnsetSettingResult{}, err
	}
	path, err := wire.SettingPath(req.Key)
	if err != nil {
		return issueops.UnsetSettingResult{}, invalid("%v", err)
	}
	var body apigen.RemovedSetting
	if err := c.store.dispatch(ctx, wire.Request{
		Op:     wire.OpUnsetSetting,
		Method: http.MethodDelete,
		Path:   path,
	}, &body); err != nil {
		return issueops.UnsetSettingResult{}, err
	}
	return issueops.UnsetSettingResult{Key: body.Key}, nil
}

// validateSettingKey is the rule a key a caller READS or REMOVES has to meet,
// and it is the only rule at that end: a key has to name something.
//
// It is the client-side twin of internal/workapi.ValidateSettingKey, redeclared
// rather than imported for releaser.go's reason — depguard denies
// internal/workapi to this package — and pinned against it by
// TestSettingWriteValidationMatchesTheSharedValidator, from a test file where
// the rule does not apply.
//
// IT IS ALSO PATH SAFETY, which is why it could not have been left to the
// server even if the role did not state it: the key is one PATH SEGMENT, so an
// empty one would join to the collection and turn a write of one setting into a
// request against all of them.
func validateSettingKey(key string) error {
	if strings.TrimSpace(key) == "" {
		return invalid("config key must not be empty")
	}
	return nil
}

// validateSettingWrite is the role's own write rules, restated: the key rule
// above, the protected key, and the one value whose shape is checked because it
// is PROJECTED rather than merely stored.
//
// ALL THREE ARE THE ROLE'S AND NOT THE SERVER'S, which is the line this function
// draws and the reason it stops where it does. The server applies its own EDGE
// bounds on top — a key past 255 characters on the write (but not on the
// remove), a value past types.MaxTextBytes BYTES, a media type — and this client
// restates none of them: those are one server release away from moving, and a
// second copy here would either drift or refuse a request a newer server would
// take. They travel as the 400 the mapper turns into the same ErrValidation, and
// the difference that makes to a local leg is ledgered (L-config-bounds).
//
// The three below cannot travel that way for the reason every restated role rule
// on this seam cannot: a request the role's own contract calls invalid must not
// spend a round trip to be told so, and must not bind its classification to a
// problem body a future server release might spell differently.
func validateSettingWrite(key, value string) error {
	if err := validateSettingKey(key); err != nil {
		return err
	}
	// The prefix is owned by `bd init --prefix`, `bd bootstrap` and
	// `bd rename-prefix`, each of which does work this plane cannot: rewriting
	// existing ids, or seeding a workspace that has none. Storing a new one here
	// would leave the beads created before the write and the beads created after
	// it disagreeing about their own namespace, with nothing to reconcile them.
	// BOTH SPELLINGS refuse, and the dashed one for a different reason than the
	// underscored: nothing reads it, so writing it would report success and be
	// unobservable.
	if key == issueops.SettingKeyIssuePrefix || key == settingKeyIssuePrefixDashed {
		return invalid("%q is set by bd init --prefix, bd bootstrap or bd rename-prefix, not by a config write: "+
			"storing it here would leave existing ids under the old prefix with nothing to reconcile them", key)
	}
	// status.custom is PROJECTED into custom_statuses, which reads consult
	// first, so a value that cannot be projected must not become a row. The
	// parse belongs to the ROLE — every leg refuses it — rather than to a front
	// door, because a value one door accepted and another refused would be half
	// applied by whichever wrote first.
	if key == issueops.SettingKeyStatusCustom && value != "" {
		if _, err := types.ParseCustomStatusConfig(value); err != nil {
			return invalid("invalid %s value: %v", key, err)
		}
	}
	return nil
}

// settingKeyIssuePrefixDashed is the second spelling of the protected key. The
// role names only the underscored one as a constant and spells this one inline;
// it is named here so the refusal and the test that pins it read one string.
const settingKeyIssuePrefixDashed = "issue-prefix"

// storedSettingValue is what the workspace now holds, read off the answer.
//
// THE PROJECTION IS TRANSCRIBED RATHER THAN INTERPRETED. The response to a write
// is byte-identical to the read that follows it — one `wireSetting` serves both
// — so a stored value comes back on the same optional member, absent for the
// same three facts, and this reads it with the same helper the reads use.
//
// A REDACTED KEY IS THE ONE PLACE THE PROJECTION CANNOT ANSWER, and the fallback
// is the request rather than a guess. The operation PERMITS writing a
// credential-bearing key — refusing would leave a workspace's credentials
// visible as present and permanently unconfigurable through this door — and then
// withholds the value from its own answer, exactly as the read withholds it. So
// `value` is absent on a write that certainly stored something, and reporting ""
// would tell a caller the plane holds nothing there: the write-side spelling of
// the confusion RedactedSettingError exists to prevent on the read side (L9).
//
// Answering with the value the caller SENT is not an interpretation of what
// redaction hid. It is the operation's own promise, stated on the request member:
// the stored value equals the value sent for every key this plane accepts,
// because the one stored key with a normalization step is `issue_prefix` and
// that is the one key the role refuses. There is no third option — the result
// type carries no "withheld" spelling, and erroring on a write that LANDED would
// be a worse answer than either.
func storedSettingValue(sent string, body apigen.Setting) string {
	if body.Redacted {
		return sent
	}
	return settingValue(body)
}

// settingValue reads the wire's optional value.
//
// The member is absent for three different facts — nothing stored, the empty
// string stored, and a credential withheld — and `redacted` separates only the
// third. All three are "" to this role by its own contract; see GetSetting.
func settingValue(s apigen.Setting) string {
	if s.Value == nil {
		return ""
	}
	return *s.Value
}

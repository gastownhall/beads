package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The four settings operations. Each one decodes its parameters, hands the whole
// request to the workspace-settings role, and shapes the answer onto the wire.
//
// WHAT IS NOT HERE, as in reads.go: no key is routed to a source, no value is
// parsed, no projection is performed, no unit of work is opened. All of that is
// inside issueops.WorkspaceConfig's implementation, which `bd config` reaches
// through the same accessor. This surface cannot answer the multi-source
// questions `bd config show`, `drift`, `apply` and `validate` ask — three of
// their five sources are files on the CLIENT's filesystem — so it publishes no
// operation that pretends to.
//
// THE ONE THING THIS FILE DECIDES THAT THE ROLE DOES NOT is redaction, and that
// is a wire decision rather than a storage one. The CLI prints stored values in
// full because its caller already holds the database; a value on this surface
// travels to whoever the port admits, over no TLS, and a bearer does not narrow
// it — where one is configured at all it is a single shared token that names
// nobody, so it decides WHO may call and never what a caller who is let in may
// read. A withheld value is OMITTED rather than masked, so a client can never
// mistake a placeholder for configuration.
//
// REDACTION IS A RULE ABOUT DISCLOSURE, WHICH IS WHY IT DOES NOT REACH THE
// WRITES. A credential-bearing key is writable and removable here: the role
// refuses no such key, a writer already holds the value, and a refusal would
// leave a workspace's credentials visible as PRESENT and permanently
// unconfigurable through this door. What the writes owe is the projection —
// every answer that carries a stored value goes through wireSetting, so there is
// one statement of the rule and the response to a write is what the read that
// follows it returns.

// handleListSettings answers GET /v0/beads/config.
func (s *Server) handleListSettings(w http.ResponseWriter, r *http.Request) {
	if !s.requireNoQuery(w, r) {
		return
	}

	settings, err := s.workspaceConfig(r)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	result, err := settings.ListSettings(r.Context(), issueops.ListSettingsRequest{})
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	keys := make([]string, 0, len(result.Settings))
	for key := range result.Settings {
		keys = append(keys, key)
	}
	// Ordered by key, which is what makes the paginated envelope honest: the
	// order is stable across calls, so a keyset cursor over it is expressible
	// later without changing what a client already receives.
	slices.Sort(keys)

	items := make([]apigen.Setting, 0, len(keys))
	for _, key := range keys {
		items = append(items, wireSetting(key, result.Settings[key]))
	}
	writeJSON(w, apigen.SettingsPage{Items: items, HasMore: false})
}

// handleGetSetting answers GET /v0/beads/config/{key}.
func (s *Server) handleGetSetting(w http.ResponseWriter, r *http.Request) {
	if !s.requireNoQuery(w, r) {
		return
	}
	key, ok := s.settingKey(w, r)
	if !ok {
		return
	}

	settings, err := s.workspaceConfig(r)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	result, err := settings.GetSetting(r.Context(), issueops.GetSettingRequest{Key: key})
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	writeJSON(w, wireSetting(result.Key, result.Value))
}

// setSettingValueMember is the one member SetSettingRequest carries. The schema
// is additionalProperties: false, so anything else is refused BY NAME.
const setSettingValueMember = "value"

// setSettingRequestMembers is the document's member list for SetSettingRequest.
//
// THE KEY IS NOT ON IT. It is the path's, so a body carrying it too would give
// one request two anchors and a question about what to do when they disagree.
var setSettingRequestMembers = []string{setSettingValueMember}

// handleSetSetting answers PUT /v0/beads/config/{key}.
//
// WHAT IS NOT HERE, as in the reads above: no key is routed to a source, no
// value is parsed, and the projection of status.custom and types.custom into
// their normalized tables is the ROLE's, inside the same transaction as the row.
// A handler that projected for itself would put the table write outside that
// transaction, which is the split the projection exists to prevent.
//
// THE REDACTION IS THIS FILE'S, and the write half reuses it rather than
// restating it. A key whose NAME marks it credential-bearing is perfectly
// writable — the role refuses no such key, and refusing here would protect
// nothing the caller does not already hold while leaving a workspace's
// credentials permanently unconfigurable through this door. What the wire owes
// is that the RESPONSE not hand the value back: wireSetting is the same
// projection the read uses, so the answer to a write is byte-identical to the
// read that follows it, and redaction stays one rule decided on the key alone.
// The echo costs nothing either — the role promises the stored value equals the
// value sent for every key it accepts.
func (s *Server) handleSetSetting(w http.ResponseWriter, r *http.Request) {
	if !s.requireNoQuery(w, r) {
		return
	}
	if !s.requireJSONContent(w, r) {
		return
	}
	key, ok := s.settingWriteKey(w, r)
	if !ok {
		return
	}
	value, ok := s.settingValue(w, r)
	if !ok {
		return
	}

	settings, err := s.workspaceConfig(r)
	if err != nil {
		s.failSettingWrite(w, r, err)
		return
	}
	result, err := settings.SetSetting(r.Context(), issueops.SetSettingRequest{Key: key, Value: value})
	if err != nil {
		s.failSettingWrite(w, r, err)
		return
	}
	writeJSON(w, wireSetting(result.Key, result.Value))
}

// handleUnsetSetting answers DELETE /v0/beads/config/{key}.
//
// NO 404, which is the one place this diverges from DELETE
// /v0/beads/memories/{key}. That role reports whether a row was found; this one
// cannot — the storage seam discards the affected-row count on all three legs —
// and an absent key and a key stored empty are one answer on this plane anyway.
// So the operation states an intended END STATE, and a caller clearing
// configuration it is not sure was ever written does not have to classify an
// error to learn it was already absent.
//
// The key is bounded by settingKey rather than settingWriteKey: a removal is a
// COMPARISON and never an insert, so a key no column could hold simply matches
// nothing, and refusing it would be a refusal the role does not have. That is
// releaseExpectedAssignee's rule, applied to the other keyed plane.
//
// The failure path is handleGetSetting's, unchanged and for its reason: the two
// take the same parameter and judge it the same way, and the role's one refusal
// — an empty key — the path bound has already made unreachable.
func (s *Server) handleUnsetSetting(w http.ResponseWriter, r *http.Request) {
	if !s.requireNoQuery(w, r) {
		return
	}
	key, ok := s.settingKey(w, r)
	if !ok {
		return
	}

	settings, err := s.workspaceConfig(r)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	result, err := settings.UnsetSetting(r.Context(), issueops.UnsetSettingRequest{Key: key})
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	// The key and nothing else. There is deliberately no `removed` flag — the
	// seam discards the count — and deliberately no `value`: reporting what was
	// there would publish, on the one operation that withholds nothing, exactly
	// the credential the read redacts.
	writeJSON(w, apigen.RemovedSetting{Key: result.Key})
}

// settingWriteKey is settingKey plus the bound a WRITE needs, and reports
// whether the request may proceed.
//
// THE LENGTH CHECK IS THE WRITE'S ALONE, and the asymmetry with the read and the
// removal beside it is the hazard rather than an oversight. `config.key` is
// VARCHAR(255) and this operation INSERTS into it, so an over-long key is an
// error from the column — a 500 for a request the caller could have fixed. The
// read and the removal COMPARE the same value and never store it, so a key no
// column could hold matches nothing and already has an answer; refusing it there
// would be a refusal the role does not have, which is releaseExpectedAssignee's
// rule exactly.
//
// It is keyed on storage's own constant rather than a second copy of the
// schema's number, the actor precedent.
func (s *Server) settingWriteKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key, ok := s.settingKey(w, r)
	if !ok {
		return "", false
	}
	if types.CheckFieldLen("key", key) != nil {
		s.fail(w, r, InvalidArgument("key", ReasonInvalidValue,
			fmt.Sprintf("`key` is %d characters; storage holds at most %d",
				utf8.RuneCountInString(key), types.MaxFieldLen)))
		return "", false
	}
	return key, true
}

// settingValue decodes the body's one member.
//
// NOTHING IS TRIMMED OR FILTERED. Two of the keys this plane holds carry
// structured configuration a filter would corrupt, and the role's promise is
// that a successful write stored the value that was sent.
//
// IT IS BOUNDED, THOUGH, and that is settingWriteKey's rule applied to the other
// half of the row rather than a second opinion about it. `config.value` is a
// TEXT column, so a value past 65535 BYTES is an error from the column — a
// generic 500 for a request the caller could have fixed, which is the exact
// failure the key's bound exists to prevent. Keyed on storage's own constant,
// and counted in BYTES because that is how the column counts.
//
// It is NOT widened the way the ephemeral comment column was. A comment is a
// document — a stack trace, a diff, a transcript — and the planes disagreeing
// about one was a bug. A setting is a VALUE: nothing this plane holds is a
// megabyte of configuration, and the narrow bound is the honest description of
// what a settings row is for. decodeJSONObjectBody's 1 MiB still applies above
// it and is now never the binding limit here.
//
// THE EMPTY STRING IS ACCEPTED and is stored. It reads back indistinguishably
// from a key nothing ever set, which is this plane's shipped conflation rather
// than something this operation introduces — the document says so, and a caller
// that meant "remove it" has DELETE.
//
// Explicit `null` is a 400 naming the member rather than a clear, the rule every
// optional member on this surface follows and, here, the one that keeps the
// clear on the operation that performs one.
func (s *Server) settingValue(w http.ResponseWriter, r *http.Request) (string, bool) {
	members, res := decodeJSONObjectBody(w, r)
	if res != nil {
		s.fail(w, r, *res)
		return "", false
	}
	if offender, unknown := unknownMember(members, setSettingRequestMembers); unknown {
		s.failUnknownMember(w, r, offender, setSettingRequestMembers)
		return "", false
	}
	raw, ok := members[setSettingValueMember]
	if !ok {
		s.fail(w, r, InvalidArgument(setSettingValueMember, ReasonInvalidValue,
			"`"+setSettingValueMember+"` is required"))
		return "", false
	}
	var value *string
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		s.fail(w, r, InvalidArgument(setSettingValueMember, ReasonInvalidValue,
			"`"+setSettingValueMember+"` must be a string"))
		return "", false
	}
	if types.CheckTextLen(setSettingValueMember, *value) != nil {
		s.fail(w, r, InvalidArgument(setSettingValueMember, ReasonInvalidValue,
			fmt.Sprintf("`%s` is %d bytes; storage holds at most %d",
				setSettingValueMember, len(*value), types.MaxTextBytes)))
		return "", false
	}
	return *value, true
}

// failSettingWrite answers a failed write.
//
// It draws the ErrValidation-is-a-400 line the sweep, delete, tree, edges,
// batch-create and memory handlers each draw, in the same shape and for the same
// reason: the role performs request validation this handler does not duplicate,
// and widening ClassifyError instead would change what every other operation
// returns for an error it has never produced.
//
// NO `param`, and this is failMemoryErr's posture rather than a shortcut. Two of
// the role's refusals are reachable here and they are about different members —
// the protected key, and a status.custom value that does not parse — and telling
// them apart takes the role's protected-key VOCABULARY, which this package
// cannot hold: depguard denies internal/workapi from every non-test file here,
// precisely so a second copy of a shared rule cannot exist to drift. The detail
// carries the role's own sentence, which names what to send instead; the
// refusals this handler owns all name their member above.
func (s *Server) failSettingWrite(w http.ResponseWriter, r *http.Request, err error) {
	if !errors.Is(err, issueops.ErrValidation) {
		s.failErr(w, r, err)
		return
	}
	s.fail(w, r, InvalidArgument("", ReasonInvalidValue, err.Error()))
}

// settingKey validates the path parameter and reports whether the request may
// proceed.
//
// The refusal is a 400 rather than the 404 the issue routes give a malformed
// id: this operation HAS no 404, because a key nothing stored and a key stored
// empty are one answer here.
//
// A control character is refused rather than looked up because a percent-escape
// in the path decodes to one, and a key carrying a newline could only have come
// from a client assembling paths by concatenation.
func (s *Server) settingKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.PathValue("key")
	switch {
	case strings.TrimSpace(key) == "":
		s.fail(w, r, InvalidArgument("key", ReasonInvalidValue, "`key` is empty after trimming"))
		return "", false
	case strings.ContainsFunc(key, isControlChar):
		requestInfo(r.Context()).refuse(key)
		s.fail(w, r, InvalidArgument("key", ReasonInvalidValue, "`key` must not contain control characters"))
		return "", false
	}
	return key, true
}

// wireSetting projects one stored setting onto the wire, withholding the value
// when the KEY marks the setting as credential-bearing.
//
// The predicate is internal/config's, not re-implemented: it is the same rule
// `bd config set` uses to refuse writing a secret into a git-tracked file, so
// the set of keys this surface protects and the set the CLI warns about cannot
// drift apart. It is a decision about the key ALONE — no
// value is inspected — which is stated on the wire in `Setting.redacted` so
// that no operator concludes a credential stored under an innocuous name is
// covered by it.
func wireSetting(key, value string) apigen.Setting {
	if config.IsSecretKey(key) {
		return apigen.Setting{Key: key, Redacted: true}
	}
	setting := apigen.Setting{Key: key, Redacted: false}
	// Never as an empty string: the role answers "" for a key nothing stored
	// and for a key stored empty alike, so emitting one would publish a value
	// that may not exist. Absent is the documented spelling for both.
	if value != "" {
		setting.Value = &value
	}
	return setting
}

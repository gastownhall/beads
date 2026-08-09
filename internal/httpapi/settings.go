package httpapi

import (
	"net/http"
	"slices"
	"strings"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/issueops"
)

// The two settings operations. Each one decodes its parameters, hands the whole
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
// full because its caller already holds the database; this server has no
// authentication at all, so every process that can reach the port would
// otherwise be able to read a stored credential. A withheld value is OMITTED
// rather than masked, so a client can never mistake a placeholder for
// configuration.

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

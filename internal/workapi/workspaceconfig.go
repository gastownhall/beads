package workapi

import (
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/kvkeys"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The single definition of what a write to the workspace's durable settings
// plane is allowed to be and of what a READ of it may carry — either by
// enumeration or by name — beside BuildListFilter and BuildCountFilter.
//
// It lives here for the reason those do: three implementations of
// issueops.WorkspaceConfig have to agree about it, and it is checkable without
// a database. The conformance contract is left to pin what only a real backend
// can show — that the row and its projection land together.
//
// It is deliberately NOT the whole of `bd config set`. Which SOURCE a key
// belongs to (config.yaml, git config, this plane) is resolved by the front
// door; see issueops/workspaceconfig.go.

// ValidateSettingKey checks a key a caller wants to READ or REMOVE, and
// returns it unchanged.
//
// The only rule at this end is that a key has to name something: an empty key
// answered with the empty value an unset key returns would report "not set"
// for a question nobody asked.
func ValidateSettingKey(key string) (string, error) {
	if strings.TrimSpace(key) == "" {
		return "", fmt.Errorf("%w: config key must not be empty", issueops.ErrValidation)
	}
	return key, nil
}

// ValidateSettingWrite checks a key and value a caller wants to STORE, and
// returns the value as it will be stored, so that a body cannot store a
// different string from the one that was checked.
func ValidateSettingWrite(key, value string) (string, error) {
	if _, err := ValidateSettingKey(key); err != nil {
		return "", err
	}
	// THE LENGTH BOUNDS ARE THE ROLE'S, so every leg refuses the same set.
	// The Dolt schema stores config.key in VARCHAR(255) and config.value in
	// TEXT, so a row past either bound was already refused below this rule —
	// but by the column, as a driver error no caller can classify, while the
	// HTTP operation refused the same write as a typed, member-named
	// ErrValidation at its edge. Stating the bound here makes every leg's
	// answer the typed refusal, and holds for a backend whose columns are
	// unbounded text, where nothing below the edge would refuse it at all.
	// Keyed on storage's own constants: the key in CHARACTERS, the value in
	// BYTES, which is how each column counts. Both errors also carry
	// types.ErrFieldTooLong, so a caller can tell "too long" from the other
	// refusals here without matching a message.
	//
	// The bound is on the WRITE only. A read or a removal compares the key and
	// never stores it, so an over-long key there matches nothing and already
	// has an answer (ValidateSettingKey).
	if err := types.CheckFieldLen("config key", key); err != nil {
		return "", fmt.Errorf("%w: %w", issueops.ErrValidation, err)
	}
	if err := types.CheckTextLen("config value", value); err != nil {
		return "", fmt.Errorf("%w: %w", issueops.ErrValidation, err)
	}
	// config.YamlOnlyKeys — startup settings and the tracker secrets
	// (github.token, linear.api_key, jira.api_token, gitlab.token, ado.pat) —
	// must live in the workspace's local config.yaml, never in this plane's
	// row-per-key storage: that storage is the Dolt config table, which is a
	// SYNCED table (bd dolt push/pull, DOLT_CLONE, DOLT_PULL all replicate
	// it). `bd config set` already refuses these at its own door; this is
	// the only other door onto this plane (the settings HTTP surface bd
	// serve and its dashboard expose), and it had no such rule, so a secret
	// PUT through it landed in the synced table and reached every remote on
	// the next push (GH#7559).
	if config.IsYamlOnlyKey(key) {
		return "", fmt.Errorf("%w: %q must be set in the workspace's local config.yaml (bd config set %s <value>), not written here: "+
			"this plane is the Dolt config table, which syncs to every remote on push/pull",
			issueops.ErrValidation, key, key)
	}
	// The prefix is owned by bd init --prefix, bd bootstrap and bd
	// rename-prefix. Refused HERE rather than at the front door because `bd
	// config set` is not the only door that reaches this plane: before this
	// role existed `bd config set-many issue_prefix=x` walked past the guard
	// and re-prefixed the workspace.
	if key == issueops.SettingKeyIssuePrefix || key == "issue-prefix" {
		return "", fmt.Errorf("%w: %q is set by bd init --prefix, bd bootstrap or bd rename-prefix, not by a config write: "+
			"storing it here would leave existing ids under the old prefix with nothing to reconcile them",
			issueops.ErrValidation, key)
	}
	// status.custom is PROJECTED into custom_statuses, which reads consult
	// first, so a value that cannot be projected must not become a row.
	// Checking here rather than leaving it to SyncCustomStatusesTable is what
	// makes the refusal a validation error rather than a storage failure.
	if key == issueops.SettingKeyStatusCustom && value != "" {
		if _, err := types.ParseCustomStatusConfig(value); err != nil {
			return "", fmt.Errorf("%w: invalid %s value: %v", issueops.ErrValidation, key, err)
		}
	}
	// types.retained is a SAFETY setting read leniently by every sweep, so a
	// value that would read as names no row carries is refused here, where
	// every WorkspaceConfig body passes and where `bd config set-many`, which
	// writes its batch outside the role, validates each pair before any write.
	if key == issueops.SettingKeyTypesRetained {
		if err := ValidateRetainedTypesValue(value); err != nil {
			return "", fmt.Errorf("%w: invalid %s value: %v", issueops.ErrValidation, key, err)
		}
	}
	return value, nil
}

// FilterSettingsEnumeration takes the rows a store handed back and returns the
// ones the settings enumeration is allowed to carry: everything except the KV
// plane.
//
// THE KV PLANE RIDES IN THE SAME TABLE AND IS NOT SETTINGS. Generic `bd kv`
// keys and the `bd remember` memories nested under them are USER DATA stored as
// config rows beneath kvkeys.Prefix, and an enumeration that returned them
// published that data on `bd config list` and on GET /v0/beads/config alike —
// the latter reachable by anything a shared bearer admits, if one is
// configured at all, and redacting on the KEY NAME while a memory's content is
// in the VALUE. `bd config list` carrying kv rows was never a
// design; it fell out of one storage table holding two planes.
//
// THE WHOLE PREFIX GOES, not just kv.memory.: "settings minus one user-data
// namespace but including the other" is not a rule anyone could state in a doc
// comment. Nothing is lost by it — the purpose-built views over those rows
// (`bd kv list`, `bd memories`) read the store directly and never come through
// this role.
//
// IT IS APPLIED HERE, beside the validators, because both WorkspaceConfig
// bodies call it and two doors onto one plane must answer the same. Filtering
// in the HTTP handler instead would leave `bd config list` printing memories
// into every terminal and transcript while the HTTP door claimed they were not
// settings.
//
// IT IS ONE HALF OF THE READ FIREWALL. The other half is
// FilterSettingsPointRead, which answers the caller who names an exact key, and
// both decide with KeyIsOnTheKVPlane so the boundary is stated once. An
// exclusion that stopped at the enumeration left the disclosure standing for
// anyone who could guess a key — and the guess is cheap, since `bd memories`
// derives its keys from the content it stores.
//
// The result is always a fresh map, empty rather than nil, because
// ListSettingsResult.Settings promises a caller can range over the answer
// without a guard and at least one store path returns a nil map when it finds
// no rows.
func FilterSettingsEnumeration(stored map[string]string) map[string]string {
	settings := make(map[string]string, len(stored))
	for key, value := range stored {
		if KeyIsOnTheKVPlane(key) {
			continue
		}
		settings[key] = value
	}
	return settings
}

// FilterSettingsPointRead answers a GetSetting that names a key on the KV
// plane, and reports whether it answered. A caller gets (result, false) for
// every key the settings role owns, and reads on.
//
// THE ANSWER IS THE ABSENT-KEY ANSWER, EXACTLY: the echoed key, an empty value
// and a nil error, which is what SettingResult.Value documents for a key
// nothing ever stored. Returning it rather than an error is what makes the
// refusal indistinguishable from absence — there is no ErrNotFound on this role
// to reach for, and a bespoke error would tell the caller that the key it
// guessed exists, which is most of what it wanted to know.
//
// IT IS DECIDED BEFORE THE STORE IS ASKED. Both bodies call it between the key
// validator and their read, so a refused key costs no query and, on the
// unit-of-work leg, opens no transaction.
//
// THE ANSWER IS THE ROLE'S, NOT THE PLANE'S. `bd kv get`, `bd remember` and
// `bd memories` read those rows through their own front doors, which do not
// come through here, so nothing a user stored becomes unreachable — it stops
// being reachable through the door marked "settings". The WRITES are untouched:
// SetSetting and UnsetSetting still take a verbatim `kv.` key, which is the
// escape hatch for a wedged memory.
func FilterSettingsPointRead(key string) (issueops.SettingResult, bool) {
	if !KeyIsOnTheKVPlane(key) {
		return issueops.SettingResult{}, false
	}
	return issueops.SettingResult{Key: key}, true
}

// KeyIsOnTheKVPlane reports whether a config-table key names user data rather
// than a setting.
//
// It is the single statement of where the plane boundary runs. Both filters
// above decide with it, and so does `bd config show`, whose database section
// reads the table raw and would otherwise print the plane back into every
// terminal the two filters were added to keep it out of.
//
// THE WHOLE PREFIX, ANCHORED. Not kvkeys.MemoryConfigKeyPrefix, which is the
// narrower constant and the tempting one; not an unanchored match, which would
// take `custom.mentions.kv.somewhere` with it; and "kv." carries the dot, so
// `kvetch` is a setting.
func KeyIsOnTheKVPlane(key string) bool {
	return strings.HasPrefix(key, kvkeys.Prefix)
}

// Package workapi holds the work-query contract shared by every bd frontend.
//
// Filter construction, defaults, validation, and response shaping live here
// so the CLI and any other frontend answer the same question the same way
// instead of drifting through parallel copies. Storage sentinels are
// normalized on the way out for the same reason: a frontend should not have
// to know that the store seam reports a missing issue as storage.ErrNotFound
// while the domain seam reports it as a wrapped sql.ErrNoRows (see
// GetIssueOrWisp). The package therefore depends on neither frontend:
// it must not import github.com/spf13/cobra or net/http, and it must not read
// process-local state (client cwd, environment) that is meaningless in a
// long-lived server. internal/config is available only for workspace-scoped
// reads such as GetCustomTypesFromYAML.
//
// The boundary is enforced mechanically, not by review: see the
// workapi-frontend-boundary depguard rule in .golangci.yml and the
// banned-accessor check in scripts/ci/pr-policy.sh.
package workapi

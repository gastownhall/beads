package doctor

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltremote"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

var querySQLRemotesForDoctor = querySQLRemotes

// CheckDoltRemoteGitOrigin classifies Dolt remotes whose URL points at the
// git origin repository. The git-backed origin — the remote named origin
// whose URL uses a Git transport — is the canonical sync transport bd
// itself wires (Dolt data travels on refs/dolt/data of the git origin), so
// it is OK, never a collision. What still warns is any other remote pointing
// there: a non-origin alias, or an origin whose URL matches without using a
// Git transport.
func CheckDoltRemoteGitOrigin(repoPath string) DoctorCheck {
	name := "Dolt Remote vs Git Origin"
	beadsDir := ResolveBeadsDirForRepo(repoPath)

	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil || cfg.GetBackend() != configfile.BackendDolt {
		return DoctorCheck{
			Name:     name,
			Status:   StatusOK,
			Message:  "N/A (not using Dolt backend)",
			Category: CategoryDolt,
		}
	}

	originURL := gitOriginRemoteURL(repoPath)
	if originURL == "" {
		return DoctorCheck{
			Name:     name,
			Status:   StatusOK,
			Message:  "No git origin configured",
			Category: CategoryDolt,
		}
	}

	sqlRemotes, sqlErr := querySQLRemotesForDoctor(beadsDir)
	if sqlErr != nil {
		// Can't check; skip silently.
		return DoctorCheck{
			Name:     name,
			Status:   StatusOK,
			Message:  "Could not query Dolt remotes (server may not be running)",
			Category: CategoryDolt,
		}
	}

	normalizedOrigin := doltremote.CanonicalForComparison(originURL)
	var aliases []string
	originWrongTransport := false
	gitSyncOrigin := false
	for _, r := range sqlRemotes {
		if doltremote.CanonicalForComparison(r.URL) != normalizedOrigin {
			continue
		}
		if doltutil.IsGitSyncRemote(r.Name, r.URL) {
			// The canonical git-backed sync remote: refs/dolt/data on the
			// git origin, the topology bd itself wires.
			gitSyncOrigin = true
			continue
		}
		if r.Name == "origin" {
			// Matches the git origin repository but not over a Git
			// transport: a raw http(s), file or cloud URL addresses a Dolt
			// remote server or native storage, not the git repository.
			originWrongTransport = true
			continue
		}
		aliases = append(aliases, r.Name)
	}

	if !originWrongTransport && len(aliases) == 0 {
		if gitSyncOrigin {
			return DoctorCheck{
				Name:     name,
				Status:   StatusOK,
				Message:  "Dolt remote origin uses the git origin for git-backed sync",
				Detail:   "The git-backed origin carries the Dolt data ref (refs/dolt/data) on the git origin, the canonical cross-machine sync transport.",
				Category: CategoryDolt,
			}
		}
		return DoctorCheck{
			Name:     name,
			Status:   StatusOK,
			Message:  "No Dolt remote matches git origin",
			Category: CategoryDolt,
		}
	}

	var affected []string
	var detail, fix []string
	if originWrongTransport {
		affected = append(affected, "origin")
		detail = append(detail, "origin matches the git origin URL but does not use a Git transport: git-backed sync requires a git+ URL, for example git+https://example.com/org/repo.git.")
		fix = append(fix, "Configure origin for Git-backed sync: 'bd dolt remote remove origin' and 'bd dolt remote add origin git+<git origin URL>', so Dolt data travels on the git origin's refs/dolt/data ref.")
	}
	if len(aliases) > 0 {
		affected = append(affected, aliases...)
		detail = append(detail, fmt.Sprintf("origin is the default sync remote; %s point(s) at the git origin repository under another name, a federation peer with an unexpected role for that URL.", strings.Join(aliases, ", ")))
		fix = append(fix, "Review the matching remote(s): remove an unintended one with 'bd dolt remote remove <name>', or point it at its own repository. The git-backed origin remains the remote for default sync.")
	}

	return DoctorCheck{
		Name:     name,
		Status:   StatusWarning,
		Message:  fmt.Sprintf("%d Dolt remote(s) match the git origin URL: %s", len(affected), strings.Join(affected, ", ")),
		Detail:   strings.Join(detail, "\n"),
		Fix:      strings.Join(fix, "\n"),
		Category: CategoryDolt,
	}
}

func gitOriginRemoteURL(repoPath string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// querySQLRemotes gets remotes from the SQL server.
func querySQLRemotes(beadsDir string) ([]storage.RemoteInfo, error) {
	db, _, err := openDoltDB(beadsDir)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.Query("SELECT name, url FROM dolt_remotes")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var remotes []storage.RemoteInfo
	for rows.Next() {
		var r storage.RemoteInfo
		if err := rows.Scan(&r.Name, &r.URL); err != nil {
			return nil, err
		}
		remotes = append(remotes, r)
	}
	return remotes, rows.Err()
}

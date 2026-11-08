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

// CheckDoltRemoteGitOrigin reviews the Dolt remotes whose URL matches the git
// origin. The remote named origin over a git transport is the default
// git-backed sync remote bd itself wires up: Dolt keeps its data under
// refs/dolt/data, apart from the source branches, so it is OK (GH#6150).
// Any other matching remote is reported.
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
	gitSyncOrigin, originOffGit := false, false
	for _, r := range sqlRemotes {
		if doltremote.CanonicalForComparison(r.URL) != normalizedOrigin {
			continue
		}
		switch {
		case doltutil.IsGitSyncRemote(r.Name, r.URL):
			gitSyncOrigin = true
		case r.Name == "origin":
			originOffGit = true
		default:
			aliases = append(aliases, r.Name)
		}
	}

	if len(aliases) == 0 && !originOffGit {
		message := "No Dolt remote matches git origin"
		if gitSyncOrigin {
			message = "Dolt remote origin is the git-backed sync remote for the git origin"
		}
		return DoctorCheck{
			Name:     name,
			Status:   StatusOK,
			Message:  message,
			Category: CategoryDolt,
		}
	}

	var messages, details, fixes []string
	if originOffGit {
		messages = append(messages, "1 Dolt remote(s) match the git origin URL: origin")
		details = append(details, "Using the git origin as a Dolt remote causes git and Dolt sync to share the same endpoint, which can cause conflicts.")
		fixes = append(fixes, "Remove the conflicting remote(s) with 'bd dolt remote remove <name>', or set dolt.local-only=true to disable remote sync.")
	}
	if len(aliases) > 0 {
		names := strings.Join(aliases, ", ")
		messages = append(messages, fmt.Sprintf("%d Dolt remote(s) besides origin match the git origin URL: %s", len(aliases), names))
		details = append(details, "bd syncs through the Dolt remote named origin by default and treats every other remote as a federation peer, even one that points at the same git repository.")
		fixes = append(fixes, fmt.Sprintf("Review %s: if a remote only duplicates the git origin, remove it with 'bd dolt remote remove <name>' and sync through origin.", names))
	}
	if gitSyncOrigin {
		details = append(details, "Dolt remote origin is the git-backed sync remote and needs no change.")
	}

	return DoctorCheck{
		Name:     name,
		Status:   StatusWarning,
		Message:  strings.Join(messages, "; "),
		Detail:   strings.Join(details, " "),
		Fix:      strings.Join(fixes, " "),
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

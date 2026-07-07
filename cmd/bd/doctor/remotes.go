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
)

var querySQLRemotesForDoctor = querySQLRemotes

// CheckDoltRemoteGitOrigin warns when any configured Dolt remote URL matches
// the git origin — a likely misconfiguration since beads syncs via Dolt, not git.
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
	var colliding []string
	for _, r := range sqlRemotes {
		if doltremote.CanonicalForComparison(r.URL) == normalizedOrigin {
			colliding = append(colliding, r.Name)
		}
	}

	if len(colliding) == 0 {
		return DoctorCheck{
			Name:     name,
			Status:   StatusOK,
			Message:  "No Dolt remote matches git origin",
			Category: CategoryDolt,
		}
	}

	return DoctorCheck{
		Name:     name,
		Status:   StatusWarning,
		Message:  fmt.Sprintf("%d Dolt remote(s) match the git origin URL: %s", len(colliding), strings.Join(colliding, ", ")),
		Detail:   "Using the git origin as a Dolt remote causes git and Dolt sync to share the same endpoint, which can cause conflicts.",
		Fix:      "Remove the conflicting remote(s) with 'bd dolt remote remove <name>', or set dolt.local-only=true to disable remote sync.",
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

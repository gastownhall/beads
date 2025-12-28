package doctor

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/utils"
)

func CheckJSONLIntegrity(path string) DoctorCheck {
	beadsDir := filepath.Join(path, ".beads")

	// Resolve JSONL path.
	jsonlPath := ""
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil {
		if cfg.JSONLExport != "" && !isSystemJSONLFilename(cfg.JSONLExport) {
			p := cfg.JSONLPath(beadsDir)
			if _, err := os.Stat(p); err == nil {
				jsonlPath = p
			}
		}
	}
	if jsonlPath == "" {
		// Fall back to a best-effort discovery within .beads/.
		p := utils.FindJSONLInDir(beadsDir)
		if _, err := os.Stat(p); err == nil {
			jsonlPath = p
		}
	}
	if jsonlPath == "" {
		return DoctorCheck{Name: "JSONL Integrity", Status: StatusOK, Message: "N/A (no JSONL file)"}
	}

	// Best-effort scan for malformed lines.
	f, err := os.Open(jsonlPath) // #nosec G304 -- jsonlPath is within the workspace
	if err != nil {
		return DoctorCheck{
			Name:    "JSONL Integrity",
			Status:  StatusWarning,
			Message: "Unable to read JSONL file",
			Detail:  err.Error(),
		}
	}
	defer f.Close()

	var malformed int
	var examples []string
	scanner := bufio.NewScanner(f)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var v struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(line), &v); err != nil || v.ID == "" {
			malformed++
			if len(examples) < 5 {
				if err != nil {
					examples = append(examples, fmt.Sprintf("line %d: %v", lineNo, err))
				} else {
					examples = append(examples, fmt.Sprintf("line %d: missing id", lineNo))
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return DoctorCheck{
			Name:    "JSONL Integrity",
			Status:  StatusWarning,
			Message: "Unable to scan JSONL file",
			Detail:  err.Error(),
		}
	}
	if malformed == 0 {
		return DoctorCheck{
			Name:    "JSONL Integrity",
			Status:  StatusOK,
			Message: fmt.Sprintf("%s looks valid", filepath.Base(jsonlPath)),
		}
	}

	// If we have a database, we can auto-repair by re-exporting from DB.
	dbPath := filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.Database != "" {
		dbPath = cfg.DatabasePath(beadsDir)
	}
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:    "JSONL Integrity",
			Status:  StatusError,
			Message: fmt.Sprintf("%s has %d malformed line(s)", filepath.Base(jsonlPath), malformed),
			Detail:  strings.Join(examples, "\n"),
			Fix:     "Restore the JSONL file from git or from a backup (no database available for auto-repair).",
		}
	}

	return DoctorCheck{
		Name:    "JSONL Integrity",
		Status:  StatusError,
		Message: fmt.Sprintf("%s has %d malformed line(s)", filepath.Base(jsonlPath), malformed),
		Detail:  strings.Join(examples, "\n"),
		Fix:     "Run 'bd doctor --fix' to back up the JSONL and regenerate it from the database.",
	}
}

func isSystemJSONLFilename(name string) bool {
	switch name {
	case "deletions.jsonl", "interactions.jsonl", "molecules.jsonl":
		return true
	default:
		return false
	}
}

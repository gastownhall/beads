package configfile

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/remedy"
)

// ValidateDatabaseName refuses a dolt_database that is not a single
// filesystem component: bd joins the name under its data directories (the
// embedded store, the hyphen migration, a bootstrap clone), so a separator,
// NUL, "." or "..", an absolute path or a volume would reach outside them.
// Dots and hyphens stay valid; they are sanitized for SQL elsewhere. This is
// not SQL identifier validation.
func ValidateDatabaseName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") ||
		filepath.IsAbs(name) || filepath.VolumeName(name) != "" {
		return fmt.Errorf("invalid dolt_database %s: it must be a single directory name, without path separators", remedy.PrintedValue(name))
	}
	return nil
}

package dolt

import (
	"strings"
)

func escapeSQL(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

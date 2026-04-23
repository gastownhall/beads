package main

import "strings"

// sanitizeDBName replaces hyphens and dots with underscores for
// SQL-idiomatic embedded Dolt database names (GH#2142, GH#3231).
func sanitizeDBName(name string) string {
	name = strings.ReplaceAll(name, "-", "_")
	name = strings.ReplaceAll(name, ".", "_")
	return name
}

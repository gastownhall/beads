//go:build !linux && !darwin && !windows

package doltserver

// portHolderSource is empty where no port-holder lookup exists.
const portHolderSource = ""

// resolvePortHolderInDir cannot observe anything here, which is
// PortHolderUndetermined — an absence of evidence, never evidence of absence.
func resolvePortHolderInDir(port int, dir string) (int, string, PortHolderOutcome) {
	return 0, "", PortHolderUndetermined
}

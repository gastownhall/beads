package configfile

import "fmt"

// Workspace data models a metadata.json graph_mode marker can name.
// GraphModeDependency is the Issue-and-dependency workspace every bd opens;
// an absent marker means the same thing. GraphModeLink is the persisted
// graph workspace format written by graph-preview builds.
const (
	GraphModeDependency = "dependency"
	GraphModeLink       = "link"
)

// GetGraphMode resolves the persisted workspace model. Unknown values must not
// fall back to the Issue store: they may describe a format this client cannot
// safely open. A missing config or marker retains the historical default.
//
// A recognized mode is not necessarily one this build can open: callers that
// open storage must still refuse anything other than GraphModeDependency.
func (c *Config) GetGraphMode() (string, error) {
	if c == nil || c.GraphMode == "" {
		return GraphModeDependency, nil
	}
	switch c.GraphMode {
	case GraphModeDependency, GraphModeLink:
		return c.GraphMode, nil
	default:
		return "", fmt.Errorf("unsupported workspace graph_mode %q; use a compatible bd build without changing the workspace marker", c.GraphMode)
	}
}

package beadsplugin

import _ "embed"

// all: is required because the manifest lives under a dot-directory.
//
//go:embed all:.copilot-plugin/plugin.json
var copilotPluginManifest string

// CopilotPluginManifest returns the checked-in Copilot plugin manifest content.
func CopilotPluginManifest() string {
	return copilotPluginManifest
}

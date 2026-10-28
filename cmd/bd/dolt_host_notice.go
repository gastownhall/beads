package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/ui"
)

// outrankedServerHostWarned keeps the GH#5961 host-mismatch notice to one per
// process per workspace: every bd command resolves its connection, and a
// reminder that outlives the first would be chatter. The notice prints from
// the store-open path (main.go) and `bd dolt show`, never at a foreign open
// (cmd/bd/create.go, foreign_workspace.go): there the launcher's
// configuration decides the dial, and those refusals name the mismatch in
// their own remedies.
var outrankedServerHostWarned = map[string]bool{}

// warnOutrankedServerHost prints the GH#5961 mismatch notice on stderr when
// this workspace's config.yaml dolt.host outranks a metadata.json
// dolt_server_host naming a DIFFERENT server (configfile.OutrankedServerHost),
// once per process per workspace.
func warnOutrankedServerHost(beadsDir string, cfg *configfile.Config) {
	writeOutrankedServerHostWarning(os.Stderr, beadsDir, cfg)
}

// writeOutrankedServerHostWarning is warnOutrankedServerHost on w, for tests.
func writeOutrankedServerHostWarning(w io.Writer, beadsDir string, cfg *configfile.Config) {
	if outrankedServerHostWarned[beadsDir] {
		return
	}
	metaHost, yamlHost, ok := configfile.OutrankedServerHost(beadsDir, cfg)
	if !ok {
		// Nothing mismatched: leave the workspace unmarked so a caller that
		// runs later with a cfg that does mismatch still prints.
		return
	}
	outrankedServerHostWarned[beadsDir] = true

	meta, yaml := remedy.EscapedValue(metaHost), remedy.EscapedValue(yamlHost)
	source := outrankedYamlHost(beadsDir, yamlHost)
	line := fmt.Sprintf("Warning: %s (%s) now outranks dolt_server_host %s in .beads/metadata.json (GH#5961)",
		yaml, source.host(), meta)
	if cfg.IsDoltServerMode() {
		line += fmt.Sprintf("; bd dials %s", yaml)
	} else {
		// The flip: the demoted metadata host used to infer server mode for
		// this workspace, and the yaml host that outranks it leaves it on
		// embedded storage.
		line += fmt.Sprintf("; bd now uses embedded storage instead of the server at %s", meta)
	}
	fmt.Fprintf(w, "%s\n", ui.RenderWarn(line))
	fmt.Fprintf(w, "  To keep %s: remove \"dolt_server_host\" from .beads/metadata.json and commit the removal (the file is tracked).\n", yaml)
	if keep, ok := source.keepRemedy(beadsDir, metaHost); ok {
		fmt.Fprintf(w, "  To keep %s: %s\n", meta, keep)
	}
}

// outrankedYamlHostKind is the layer an outranking dolt.host came from; its
// host() phrase names it in the notice's first line and keepRemedy() says
// what it takes to keep the metadata host.
type outrankedYamlHostKind int

const (
	outrankedFromUnknown outrankedYamlHostKind = iota
	outrankedFromBDHostEnv
	outrankedFromWorkspaceYaml
	outrankedFromLocalYaml
	outrankedFromUserYaml
)

func (k outrankedYamlHostKind) host() string {
	switch k {
	case outrankedFromBDHostEnv:
		return "the BD_DOLT_HOST environment variable's dolt.host"
	case outrankedFromWorkspaceYaml:
		return "config.yaml dolt.host"
	case outrankedFromLocalYaml:
		return "the dolt.host in .beads/config.local.yaml"
	case outrankedFromUserYaml:
		return "the user-level config.yaml's dolt.host"
	}
	return "config.yaml dolt.host"
}

// keepRemedy says how to keep the metadata host — the server the workspace
// used before the flip — given that `bd dolt set host` writes BOTH files
// (GH#5961): .beads/config.yaml dolt.host, which outranks it, and
// metadata.json dolt_server_host. It is offered only where it CONVERGES: a
// dolt.host in config.local.yaml or BD_DOLT_HOST still outranks what the
// command writes (or a user-level entry does, where the workspace has no
// .beads/config.yaml for the command to write), so those remedies remove the
// outranking entry first. ok is false when no converging remedy exists.
func (k outrankedYamlHostKind) keepRemedy(beadsDir, metaHost string) (string, bool) {
	// The host inside the copy-pasteable command stays raw: EscapedValue's
	// quotes are for prose naming a value an operator reads, not for a
	// command line (an IPv6 literal pastes unquoted just as bd reads it).
	host := metaHost
	_, statErr := os.Stat(configYamlInDir(beadsDir))
	switch {
	case k == outrankedFromBDHostEnv:
		return fmt.Sprintf("unset BD_DOLT_HOST, then run `bd dolt set host %s`", host), true
	case k == outrankedFromLocalYaml:
		return fmt.Sprintf("remove dolt.host from .beads/config.local.yaml, then run `bd dolt set host %s`", host), true
	case k == outrankedFromUserYaml && statErr != nil:
		return fmt.Sprintf("remove the dolt.host entry from the user-level config.yaml, then run `bd dolt set host %s`", host), true
	case statErr != nil:
		// No .beads/config.yaml and no outranking yaml layer named: the
		// metadata host already decides.
		return "", false
	default:
		// The workspace's own config.yaml, or a user-level entry a workspace
		// config.yaml shadows: the set rewrites the higher layer.
		return fmt.Sprintf("run `bd dolt set host %s` (it writes .beads/config.yaml dolt.host too)", host), true
	}
}

// outrankedYamlHost classifies the source of yamlHost, an outranking merged
// dolt.host, for the notice. Run in the same process configuration the
// caller classified the workspace under.
func outrankedYamlHost(beadsDir, yamlHost string) outrankedYamlHostKind {
	if env := os.Getenv("BD_DOLT_HOST"); env != "" && env == yamlHost {
		return outrankedFromBDHostEnv
	}
	if proj, _, err := config.WorkspaceYamlValueStrict(beadsDir, "dolt.host"); err == nil && proj != "" && configfile.SameServerHost(proj, yamlHost) {
		return outrankedFromWorkspaceYaml
	}
	if local, _, err := config.WorkspaceYamlSettingWithLocal(beadsDir, "dolt.host"); err == nil && local != "" && configfile.SameServerHost(local, yamlHost) {
		return outrankedFromLocalYaml
	}
	if user := config.GetUserYamlConfig("dolt.host"); user != "" && configfile.SameServerHost(yamlHost, user) {
		return outrankedFromUserYaml
	}
	return outrankedFromUnknown
}

// configYamlInDir is the config.yaml in beadsDir the notice's remedy names.
func configYamlInDir(beadsDir string) string {
	return filepath.Join(beadsDir, "config.yaml")
}

package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/ui"
)

// outrankedHostWarned records the workspaces warnOutrankedServerHost has
// warned about in this process, so a command that resolves its connection
// more than once warns once.
var outrankedHostWarned sync.Map

// warnOutrankedServerHost tells the user, once per process per beadsDir, that
// a config.yaml dolt.host now outranks a different dolt_server_host in
// beadsDir's git-tracked metadata.json (GH#5961), which host bd uses
// instead, and how to keep either. It prints nothing unless
// configfile.OutrankedServerHost reports the case.
func warnOutrankedServerHost(w io.Writer, beadsDir string, cfg *configfile.Config) {
	metaHost, yamlHost, ok := configfile.OutrankedServerHost(beadsDir, cfg)
	if !ok {
		return
	}
	if _, warned := outrankedHostWarned.LoadOrStore(filepath.Clean(beadsDir), true); warned {
		return
	}
	metaFile := beadsFileLabel(beadsDir, configfile.ConfigFileName)
	source, converges := doltHostSource(beadsDir, yamlHost)
	meta, yaml := remedy.EscapedValue(metaHost), remedy.EscapedValue(yamlHost)

	// The config.yaml host either is dialed or, local where the metadata
	// host was not, no longer infers server mode (OutrankedServerHost).
	effect, keep := "bd dials "+yaml, yaml
	if !cfg.IsDoltServerMode() {
		effect, keep = "bd now uses embedded storage instead of the server at "+meta, "embedded storage"
	}
	fmt.Fprintf(w, "%s %s dolt.host %s now outranks dolt_server_host %s in %s (GH#5961); %s.\n",
		ui.RenderWarn("Warning:"), source, yaml, meta, metaFile, effect)
	fmt.Fprintf(w, "  To keep %s: remove \"dolt_server_host\" from %s and commit the removal (the file is tracked).\n", keep, metaFile)
	switch {
	case converges:
		arg, note := remedy.CommandArg(runtime.GOOS, metaHost, "<host>")
		fmt.Fprintf(w, "  To keep %s: run bd dolt set host %s (it writes %s dolt.host too).%s\n",
			meta, arg, beadsFileLabel(beadsDir, config.ProjectConfigFileName), note)
	case source == "BD_DOLT_HOST":
		fmt.Fprintf(w, "  To keep %s: set BD_DOLT_HOST to it, or unset BD_DOLT_HOST.\n", meta)
	default:
		fmt.Fprintf(w, "  To keep %s: set dolt.host to it in %s, or remove dolt.host there.\n", meta, source)
	}
}

// writeWorkspaceDoltHost writes host to beadsDir's config.yaml as dolt.host,
// which outranks metadata.json's dolt_server_host (GH#5961), so a host bd is
// told to use beside metadata.json is the one it dials. It creates no
// config.yaml: updated is false when beadsDir has none.
func writeWorkspaceDoltHost(beadsDir, host string) (updated bool, err error) {
	if _, err := os.Stat(filepath.Join(beadsDir, config.ProjectConfigFileName)); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err := config.SetYamlConfigInDir(beadsDir, "dolt.host", host); err != nil {
		return false, err
	}
	return true, nil
}

// warnDoltHostOutranked warns when bd still dials another host than host,
// just written to beadsDir's metadata.json (and config.yaml, where
// writeWorkspaceDoltHost found one), under beadsDir's own configuration: it
// names the setting that outranks host and how to drop it.
func warnDoltHostOutranked(w io.Writer, beadsDir, host string) {
	dialed := host
	_ = config.WithBeadsDir(beadsDir, func() {
		dialed = (&configfile.Config{DoltServerHost: host}).GetDoltServerHost()
	})
	if configfile.SameServerHost(dialed, host) {
		return
	}
	setting, drop := "BEADS_DOLT_SERVER_HOST", "unset BEADS_DOLT_SERVER_HOST"
	if os.Getenv("BEADS_DOLT_SERVER_HOST") != dialed {
		source, _ := doltHostSource(beadsDir, dialed)
		setting, drop = source+" dolt.host", "remove dolt.host from "+source
		if source == "BD_DOLT_HOST" {
			setting, drop = source, "unset BD_DOLT_HOST"
		}
	}
	fmt.Fprintf(w, "%s %s %s outranks %s, so bd dials %s.\n  To dial %s: %s.\n",
		ui.RenderWarn("Warning:"), setting, remedy.EscapedValue(dialed), remedy.EscapedValue(host),
		remedy.EscapedValue(dialed), remedy.EscapedValue(host), drop)
}

// doltHostSource names where the merged dolt.host value host comes from for
// beadsDir: BD_DOLT_HOST, beadsDir's config.local.yaml or config.yaml, or the
// user-level config.yaml. converges reports whether `bd dolt set host`, which
// writes beadsDir's config.yaml, would make that value the merged one: true
// for beadsDir's config.yaml and for a user-level value that config.yaml
// outranks.
func doltHostSource(beadsDir, host string) (source string, converges bool) {
	if v, ok := os.LookupEnv("BD_DOLT_HOST"); ok && v == host {
		return "BD_DOLT_HOST", false
	}
	projectYaml := beadsFileLabel(beadsDir, config.ProjectConfigFileName)
	_, statErr := os.Stat(filepath.Join(beadsDir, config.ProjectConfigFileName))
	hasProjectYaml := statErr == nil
	project, _ := config.WorkspaceYamlValue(beadsDir, "dolt.host")
	if withLocal, _, err := config.WorkspaceYamlSettingWithLocal(beadsDir, "dolt.host"); err == nil && withLocal == host && project != host {
		return beadsFileLabel(beadsDir, config.LocalConfigFileName), false
	}
	if project == host {
		return projectYaml, true
	}
	return remedy.PrintedValue(config.UserConfigYamlDisplayPath()), hasProjectYaml
}

// beadsFileLabel names the file name in beadsDir as a notice prints it:
// .beads/<name> for a workspace's .beads directory, its full path otherwise.
func beadsFileLabel(beadsDir, name string) string {
	if filepath.Base(beadsDir) == ".beads" {
		return filepath.Join(".beads", name)
	}
	return remedy.PrintedValue(filepath.Join(beadsDir, name))
}

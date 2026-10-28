package configfile

import (
	"os"
	"strings"

	"github.com/steveyegge/beads/internal/config"
)

// OutrankedServerHost reports the mismatch GH#5961's precedence flip made
// operational: a merged config.yaml dolt.host over a raw metadata.json
// dolt_server_host, where the two name DIFFERENT servers. metaHost and
// yamlHost are those two hosts; ok is false in every other shape.
//
// A metadata-only host is still correct after the flip — foreign launches
// and older binaries read it — so this reports no warn-on-use. It reports
// only where the mismatch changes something the workspace does:
//
//   - BEADS_DOLT_SERVER_HOST must be unset: it outranks both hosts, so
//     nothing below decided. BD_DOLT_HOST does NOT gate here: merged into
//     GetYamlConfig, both a process here and in this workspace read it, so
//     it participates as the yaml tier rather than suppressing the comparison.
//   - The raw metadata.json must name a host (LoadForDiscovery, never
//     migrated and never carrying a server.json central merge, so a
//     server.json-only host never counts) and the merged dolt.host must
//     name a different one (SameServerHost: bracket and case differ, the
//     server does not).
//   - The host must matter: the workspace is in server mode (cfg.IsDoltServerMode —
//     the dial now follows the yaml host), or the flip changed its mode
//     (no explicit dolt_mode, and the metadata host is not local: host
//     inference once selected server mode from it and no longer does).
//   - Explicit embedded and proxied-server modes are silent: they pin the
//     storage choice, so which host a dial would resolve changes nothing.
//
// The caller prints the notice; see cmd/bd's store-open and `bd dolt show`
// paths. The getter itself (GetDoltServerHost) never warns: it has dozens
// of callers whose remedies would not converge there.
func OutrankedServerHost(beadsDir string, cfg *Config) (metaHost, yamlHost string, ok bool) {
	if os.Getenv("BEADS_DOLT_SERVER_HOST") != "" {
		return "", "", false
	}
	if cfg == nil || cfg.GetBackend() != BackendDolt {
		return "", "", false
	}
	if mode := strings.TrimSpace(cfg.DoltMode); strings.EqualFold(mode, DoltModeEmbedded) || strings.EqualFold(mode, DoltModeProxiedServer) {
		return "", "", false
	}
	// The metadata tier joins the notice only when the raw file names a host.
	metaCfg, err := LoadForDiscovery(beadsDir)
	if err != nil || metaCfg == nil {
		return "", "", false
	}
	metaHost, yamlHost = metaCfg.DoltServerHost, config.GetYamlConfig("dolt.host")
	if metaHost == "" || yamlHost == "" || SameServerHost(metaHost, yamlHost) {
		return "", "", false
	}
	if !cfg.IsDoltServerMode() && !(cfg.DoltMode == "" && !IsLocalHostString(metaHost)) {
		// Neither the dial nor the storage mode follows the yaml host: an
		// explicit metadata mode governs, and nothing flipped.
		return "", "", false
	}
	return metaHost, yamlHost, true
}

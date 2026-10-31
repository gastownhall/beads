package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
)

// outrankedHostWorkspace writes a .beads directory holding metadata and
// yaml as metadata.json and config.yaml (none for an empty yaml), loads its
// configuration as bd run inside it does, and returns it with its
// metadata.json.
func outrankedHostWorkspace(t *testing.T, metadata, yaml string) (string, *configfile.Config) {
	t.Helper()
	isolateForeignWorkspaceSignals(t, "")
	t.Setenv("BD_DOLT_HOST", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(os.Getenv("HOME"), ".config"))
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"metadata.json": metadata, "config.yaml": yaml} {
		if content == "" {
			continue
		}
		if err := os.WriteFile(filepath.Join(beadsDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("BEADS_DIR", beadsDir)
	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		t.Fatalf("configfile.Load: %v, %v", cfg, err)
	}
	return beadsDir, cfg
}

// TestOutrankedServerHostNoticePrintsOnce pins the GH#5961 notice: a
// server-mode workspace whose config.yaml dolt.host now outranks a different
// dolt_server_host in its tracked metadata.json is told so once per process,
// however often bd resolves its connection, naming both hosts, both files,
// and remedies that converge.
func TestOutrankedServerHostNoticePrintsOnce(t *testing.T) {
	beadsDir, cfg := outrankedHostWorkspace(t,
		`{"backend":"dolt","dolt_mode":"server","dolt_server_host":"127.0.0.1"}`,
		"dolt:\n  host: 192.0.2.50\n")

	var stderr bytes.Buffer
	warnOutrankedServerHost(&stderr, beadsDir, cfg)
	warnOutrankedServerHost(&stderr, beadsDir, cfg)

	out := stderr.String()
	if n := strings.Count(out, "now outranks"); n != 1 {
		t.Fatalf("notice printed %d times, want 1:\n%s", n, out)
	}
	for _, want := range []string{
		`.beads/config.yaml dolt.host "192.0.2.50" now outranks dolt_server_host "127.0.0.1" in .beads/metadata.json (GH#5961); bd dials "192.0.2.50".`,
		`To keep "192.0.2.50": remove "dolt_server_host" from .beads/metadata.json and commit the removal`,
		`To keep "127.0.0.1": run bd dolt set host 127.0.0.1 (it writes .beads/config.yaml dolt.host too).`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("notice lacks %q:\n%s", want, out)
		}
	}
}

// TestOutrankedServerHostNoticeStaysSilent pins the cases the GH#5961 notice
// leaves alone: the two files naming the same server, an explicit embedded
// workspace, and a metadata-only host, which still decides.
func TestOutrankedServerHostNoticeStaysSilent(t *testing.T) {
	for _, tc := range []struct{ name, metadata, yaml string }{
		{"same server", `{"backend":"dolt","dolt_mode":"server","dolt_server_host":"DB.example.com"}`, "dolt:\n  host: db.example.com\n"},
		{"explicit embedded", `{"backend":"dolt","dolt_mode":"embedded","dolt_server_host":"127.0.0.1"}`, "dolt:\n  host: 192.0.2.50\n"},
		{"metadata host only", `{"backend":"dolt","dolt_mode":"server","dolt_server_host":"127.0.0.1"}`, "dolt:\n  port: 3307\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir, cfg := outrankedHostWorkspace(t, tc.metadata, tc.yaml)
			var stderr bytes.Buffer
			warnOutrankedServerHost(&stderr, beadsDir, cfg)
			if stderr.Len() != 0 {
				t.Errorf("notice printed, want none:\n%s", stderr.String())
			}
		})
	}
}

// captureStdoutStderr runs fn with os.Stdout and os.Stderr captured apart.
func captureStdoutStderr(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	oldStdout, oldStderr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = outW, errW
	func() {
		defer func() { os.Stdout, os.Stderr = oldStdout, oldStderr }()
		fn()
	}()
	_ = outW.Close()
	_ = errW.Close()
	out, _ := io.ReadAll(outR)
	errOut, _ := io.ReadAll(errR)
	return string(out), string(errOut)
}

// TestSetDoltHostLandsWhereBDDials pins bd dolt set host and bd init
// --server-host against GH#5961's order, where config.yaml dolt.host
// outranks metadata.json dolt_server_host: the host is written to the
// workspace's config.yaml too, when it has one, so it is the host bd dials
// there, and --json says so. A workspace with no config.yaml gets none; a
// setting that still outranks the host just set (a user-level dolt.host,
// BD_DOLT_HOST, BEADS_DOLT_SERVER_HOST) is named in a warning.
func TestSetDoltHostLandsWhereBDDials(t *testing.T) {
	const (
		oldHost = "192.0.2.50"
		newHost = "192.0.2.60"
	)
	metadata := `{"backend":"dolt","dolt_mode":"server","dolt_server_host":"127.0.0.1"}`
	for _, tc := range []struct {
		name     string
		yaml     string // the workspace's config.yaml; "" for none
		userYaml string // the user-level config.yaml; "" for none
		env      string // an env var naming oldHost
		wantWarn string // "" for no warning
	}{
		{name: "config.yaml naming another host", yaml: "dolt:\n  host: " + oldHost + "\n"},
		{name: "config.yaml naming no host", yaml: "dolt:\n  user: root\n"},
		{name: "config.yaml beside a user-level host", yaml: "dolt:\n  user: root\n", userYaml: "dolt:\n  host: " + oldHost + "\n"},
		{name: "no config.yaml"},
		{name: "no config.yaml, user-level host", userYaml: "dolt:\n  host: " + oldHost + "\n", wantWarn: filepath.Join(".config", "bd", "config.yaml") + ` dolt.host "` + oldHost + `"`},
		{name: "no config.yaml, BD_DOLT_HOST", env: "BD_DOLT_HOST", wantWarn: `BD_DOLT_HOST "` + oldHost + `"`},
		{name: "config.yaml, BEADS_DOLT_SERVER_HOST", yaml: "dolt:\n  user: root\n", env: "BEADS_DOLT_SERVER_HOST", wantWarn: `BEADS_DOLT_SERVER_HOST "` + oldHost + `"`},
	} {
		for _, via := range []string{"bd dolt set host", "bd init --server-host"} {
			t.Run(tc.name+"/"+via, func(t *testing.T) {
				beadsDir, _ := outrankedHostWorkspace(t, metadata, tc.yaml)
				if tc.userYaml != "" {
					userCfg := filepath.Join(os.Getenv("HOME"), ".config", "bd", "config.yaml")
					if err := os.MkdirAll(filepath.Dir(userCfg), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(userCfg, []byte(tc.userYaml), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if tc.env != "" {
					t.Setenv(tc.env, oldHost)
				}
				config.ResetForTesting()
				if err := config.Initialize(); err != nil {
					t.Fatalf("config.Initialize: %v", err)
				}

				var stdout, stderr string
				if via == "bd dolt set host" {
					oldJSON := jsonOutput
					jsonOutput = true
					t.Cleanup(func() { jsonOutput = oldJSON })
					stdout, stderr = captureStdoutStderr(t, func() {
						if err := setDoltConfig("host", newHost, false); err != nil {
							t.Errorf("setDoltConfig: %v", err)
						}
					})
				} else {
					// init has saved --server-host to metadata.json by now.
					cfg, err := configfile.Load(beadsDir)
					if err != nil {
						t.Fatal(err)
					}
					cfg.DoltServerHost = newHost
					if err := cfg.Save(beadsDir); err != nil {
						t.Fatal(err)
					}
					var buf bytes.Buffer
					recordInitServerHost(&buf, beadsDir, newHost)
					stderr = buf.String()
				}

				if cfg, err := configfile.Load(beadsDir); err != nil || cfg.DoltServerHost != newHost {
					t.Errorf("metadata.json dolt_server_host = %v (%v), want %q", cfg, err, newHost)
				}
				yamlPath := filepath.Join(beadsDir, "config.yaml")
				if tc.yaml == "" {
					if _, err := os.Stat(yamlPath); !os.IsNotExist(err) {
						t.Errorf("config.yaml created (stat err %v), want none", err)
					}
				} else if got, _ := config.WorkspaceYamlValue(beadsDir, "dolt.host"); got != newHost {
					t.Errorf("config.yaml dolt.host = %q, want %q", got, newHost)
				}
				if tc.wantWarn == "" {
					var dialed string
					_ = config.WithBeadsDir(beadsDir, func() {
						dialed = (&configfile.Config{DoltServerHost: newHost}).GetDoltServerHost()
					})
					if dialed != newHost {
						t.Errorf("GetDoltServerHost under the workspace's configuration = %q, want %q", dialed, newHost)
					}
				}
				if via == "bd dolt set host" {
					var result map[string]interface{}
					if err := json.Unmarshal([]byte(stdout), &result); err != nil {
						t.Fatalf("--json output %q: %v", stdout, err)
					}
					if got, want := result["config_yaml_updated"] == true, tc.yaml != ""; got != want {
						t.Errorf("config_yaml_updated = %v, want %v (output %s)", result["config_yaml_updated"], want, stdout)
					}
				}
				switch {
				case tc.wantWarn == "" && strings.Contains(stderr, "Warning"):
					t.Errorf("warned, want no warning:\n%s", stderr)
				case tc.wantWarn != "" && !strings.Contains(stderr, tc.wantWarn+` outranks "`+newHost+`"`):
					t.Errorf("warning lacks %q outranking %q:\n%s", tc.wantWarn, newHost, stderr)
				}
			})
		}
	}
}

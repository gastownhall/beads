package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// TestBootstrapNeverDialsAnUnresolvedServerPort covers a git clone of a
// server-mode project that names no port (`bd init --server` records
// dolt_mode: server and no port), bootstrapped before any server runs. Every
// step of bd bootstrap that reaches the Dolt server (the metadata
// reconcile, the existence probe and the clone) must take the endpoint later
// commands dial, and with no port named that endpoint has none:
//
//   - When the next command starts bd's own server, nothing runs yet, so the
//     reconcile has nothing to read, the probe looks in the data directory
//     that server will serve, and the clone lands there, never on
//     127.0.0.1:3307, a port no later command dials (#7804 review).
//   - When no bd command starts it (auto-start off, a host bd does not run),
//     every step refuses with the store's advice instead of dialing
//     127.0.0.1:0 or host:3307.
//
// On main the reconcile dialed 127.0.0.1:0 and failed the whole bootstrap
// first; with it fixed alone, the probe and the clone dialed 127.0.0.1:3307.
func TestBootstrapNeverDialsAnUnresolvedServerPort(t *testing.T) {
	for _, tc := range []struct {
		name       string
		host       string
		autoStart  string
		credCmd    string // BEADS_DOLT_CREDENTIAL_COMMAND: a gateway
		socket     string // dolt_server_socket
		wantClone  bool   // clone with the dolt CLI into the data directory
		wantSocket bool   // clone through the server on the socket
		wantRefuse string // the advice every step refuses with
	}{
		{name: "bd starts its own server", wantClone: true},
		{name: "auto-start off", autoStart: "0", wantRefuse: "dolt.auto-start is off, so bd does not start this workspace's Dolt server."},
		{name: "remote host", host: "db.example.com", wantRefuse: "bd does not manage the Dolt server at db.example.com; pin its port"},
		// The store turns auto-start off for a gateway, so no bd command
		// serves a clone into .beads/dolt (the store refuses as externally
		// managed): bootstrap refuses too.
		{name: "gateway credential command", credCmd: "printf tok-gw", wantRefuse: "externally managed, so bd does not start it"},
		// A socket is an address: the clone goes through the server on it,
		// never into a data directory bd's server would serve.
		{name: "socket only", socket: filepath.Join(os.TempDir(), "beads-bootstrap-unresolved.sock"), wantSocket: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.credCmd != "" && runtime.GOOS == "windows" {
				t.Skip("the credential command is a POSIX shell command")
			}
			endpointCredentialsEnv(t, []string{"127.0.0.1", "db.example.com"}, 0, 3307, 3308)
			t.Setenv("BEADS_DOLT_AUTO_START", tc.autoStart)
			t.Setenv("BEADS_DOLT_CREDENTIAL_COMMAND", tc.credCmd)
			t.Setenv("BEADS_DIR", "")
			root := t.TempDir()
			beadsDir := filepath.Join(root, ".beads")
			if err := os.MkdirAll(beadsDir, 0o700); err != nil {
				t.Fatal(err)
			}
			cfg := &configfile.Config{
				Backend:          configfile.BackendDolt,
				DoltMode:         configfile.DoltModeServer,
				DoltServerHost:   tc.host,
				DoltServerSocket: tc.socket,
				DoltDatabase:     "unresolved_db",
				ProjectID:        "proj-unresolved",
			}
			if err := cfg.Save(beadsDir); err != nil {
				t.Fatal(err)
			}

			var serverClones []string
			dataDirClones := []string{}
			origServer, origDataDir := remoteCloneViaServer, remoteCloneIntoDataDir
			t.Cleanup(func() { remoteCloneViaServer, remoteCloneIntoDataDir = origServer, origDataDir })
			remoteCloneViaServer = func(_ context.Context, _, _ string, ep bootstrapServerProbeConfig) error {
				serverClones = append(serverClones, ep.dsn())
				return nil
			}
			remoteCloneIntoDataDir = func(_ context.Context, doltDir, _, _ string) error {
				dataDirClones = append(dataDirClones, doltDir)
				return nil
			}

			checkRefusal := func(step string, err error) {
				t.Helper()
				if err == nil {
					t.Errorf("%s: no error, want a refusal with %q", step, tc.wantRefuse)
					return
				}
				if !strings.Contains(err.Error(), tc.wantRefuse) {
					t.Errorf("%s: %v, want the advice %q", step, err, tc.wantRefuse)
				}
				// The advice names the test's own paths, whose random temp
				// suffixes can contain any digits: check only what bd wrote.
				bdText := withoutTestPaths(err.Error(), root)
				for _, banned := range []string{":0", "3307", "connection refused"} {
					if strings.Contains(bdText, banned) {
						t.Errorf("%s: %v names %q: it dialed a port nothing resolved", step, err, banned)
					}
				}
			}

			// 1. The metadata reconcile.
			_, _, reconcileErr := applyBootstrapMetadataRepair(beadsDir, cfg, false)
			// 2. The existence probe.
			_, probe, _ := existingBootstrapDBPlan(beadsDir, cfg, true, false)
			// 3. The clone.
			cloneErr := cloneFromRemote(context.Background(), beadsDir, "file:///nonexistent/remote", "unresolved_db", cfg)

			if probe.Port != 0 {
				t.Errorf("the existence probe dialed port %d; later commands resolve none", probe.Port)
			}
			if tc.wantSocket {
				if cloneErr != nil {
					t.Fatalf("clone: %v", cloneErr)
				}
				if len(serverClones) != 1 || !strings.Contains(serverClones[0], "unix("+tc.socket+")") {
					t.Errorf("server clones = %v, want one through unix(%s)", serverClones, tc.socket)
				}
				if len(dataDirClones) > 0 {
					t.Errorf("cloned into data directories %v; the socket's server is not bd's", dataDirClones)
				}
				// The reconcile's transport is main's (TCP to its port): see
				// Remaining in the PR. The probe dials the socket.
				if err := probe.Err; err != nil {
					if bdText := withoutTestPaths(err.Error(), tc.socket, root); strings.Contains(bdText, ":0") || strings.Contains(bdText, "3307") {
						t.Errorf("probe: %v dialed a port nothing resolved", err)
					}
				}
				return
			}
			if len(serverClones) > 0 {
				t.Errorf("cloned through a server (%v); later commands dial none", serverClones)
			}
			if tc.wantClone {
				if reconcileErr != nil {
					t.Errorf("reconcile: %v, want nothing to reconcile before bd's server first starts", reconcileErr)
				}
				if probe.Exists || probe.Err == nil || !strings.Contains(probe.Err.Error(), "data directory") {
					t.Errorf("probe = %+v, want no database found in the data directory", probe)
				}
				if cloneErr != nil {
					t.Fatalf("clone: %v", cloneErr)
				}
				if want := []string{filepath.Join(beadsDir, "dolt")}; len(dataDirClones) != 1 || dataDirClones[0] != want[0] {
					t.Errorf("cloned into data directories %v, want %v, the one bd's server serves", dataDirClones, want)
				}
				// Without a version witness every later command refuses a
				// server workspace whose .beads/dolt holds data as a legacy
				// (pre-1.0) one.
				if got := readLocalVersion(filepath.Join(beadsDir, localVersionFile)); got != Version {
					t.Errorf("version witness after the clone = %q, want %q", got, Version)
				}
				return
			}
			checkRefusal("reconcile", reconcileErr)
			checkRefusal("probe", probe.Err)
			checkRefusal("clone", cloneErr)
			// The refusal of an already-initialized workspace quotes the
			// probe's advice as bd composed it, line breaks and all, and its
			// one-line summary as one readable line: neither spells it as one
			// escaped value (\n, \x2c).
			if probe.Err != nil {
				refusal, refused := refuseServerModeInitPlan(BootstrapPlan{Database: "unresolved_db"}, cfg, true, probe)
				if !refused {
					t.Fatalf("refuseServerModeInitPlan did not refuse for probe %+v", probe)
				}
				if !strings.Contains(refusal.refusalDetail, probe.Err.Error()) || !strings.Contains(refusal.refusalDetail, tc.wantRefuse) {
					t.Errorf("refusal detail = %q, want the probe's advice %q verbatim", refusal.refusalDetail, probe.Err.Error())
				}
				if oneLine := strings.ReplaceAll(probe.Err.Error(), "\n", " "); !strings.Contains(refusal.Reason, oneLine) || strings.Contains(refusal.Reason, "\n") {
					t.Errorf("refusal summary = %q, want the probe's advice on one readable line, %q", refusal.Reason, oneLine)
				}
				for _, text := range []string{refusal.refusalDetail, refusal.Reason} {
					if strings.Contains(text, `\x2c`) || strings.Contains(text, `\n`) {
						t.Errorf("refusal %q spells the advice as an escaped value", text)
					}
				}
			}
			if !errors.Is(cloneErr, dolt.ErrUnresolvedServerPort) {
				t.Errorf("clone error %v does not wrap ErrUnresolvedServerPort", cloneErr)
			}
			if len(dataDirClones) > 0 {
				t.Errorf("cloned into data directories %v for a server bd does not start", dataDirClones)
			}
		})
	}
}

// TestBootstrapSettlesOnTheOwnedDataDirectory covers bd bootstrap's plan for
// a server-mode workspace whose own server is stopped and names no port: the
// database bd's server will serve is already in its data directory, so the
// plan settles there instead of probing 127.0.0.1:3307. The metadata
// reconcile that runs before the plan still refuses this shape, as main's
// did, though now with why instead of a dial of 127.0.0.1:0 (see Remaining
// in the PR).
func TestBootstrapSettlesOnTheOwnedDataDirectory(t *testing.T) {
	endpointCredentialsEnv(t, nil)
	root := t.TempDir()
	beadsDir := filepath.Join(root, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt", "settled_db", ".dolt"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeServer, DoltDatabase: "settled_db", ProjectID: "proj-settled"}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatal(err)
	}
	if _, _, err := applyBootstrapMetadataRepair(beadsDir, cfg, false); err == nil ||
		!strings.Contains(err.Error(), "Dolt server is not running") || strings.Contains(err.Error(), "127.0.0.1:0") {
		t.Fatalf("reconcile: %v, want it to say the server is not running, with no dial of port 0", err)
	}
	plan, probe, settled := existingBootstrapDBPlan(beadsDir, cfg, true, false)
	if !settled || plan.Action != "none" || !plan.HasExisting || !probe.Exists {
		t.Fatalf("plan = %+v, probe = %+v, settled = %v; want the existing database in the data directory", plan, probe, settled)
	}
	if !strings.Contains(plan.Reason, filepath.Join(beadsDir, "dolt", "settled_db")) {
		t.Errorf("reason %q does not name the data directory", plan.Reason)
	}
	if err := refuseServerModeInitForExistingProject(BootstrapPlan{BeadsDir: beadsDir}, cfg); err != nil {
		t.Errorf("refuseServerModeInitForExistingProject: %v, want the data directory's database to count", err)
	}
}

// TestBootstrapClonesIntoTheDirectoryBDsServerServes pins where bootstrap's
// owned-server clone lands to the data directory bd's own server then serves
// (doltserver.ResolveDoltDir once bootstrap has written metadata.json): an
// absolute database path counts, as it does for the server, and so do a
// relative dolt_data_dir and BEADS_DOLT_DATA_DIR. An absolute dolt_data_dir
// does not: metadata.json never stores one, so the server never serves it,
// and a fresh workspace inherits one from an enclosing workspace
// (findParentConfig), whose data directory it is.
func TestBootstrapClonesIntoTheDirectoryBDsServerServes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		ownMeta    bool
		dataDir    string // metadata dolt_data_dir; "ABS" for an absolute one
		database   string // metadata database; "ABS" for an absolute one
		envDataDir bool
		want       string // relative to beadsDir, or "ABS"/"ENV"
	}{
		{name: "default", ownMeta: true, want: "dolt"},
		{name: "relative dolt_data_dir", ownMeta: true, dataDir: "data", want: "data"},
		{name: "absolute database path", ownMeta: true, database: "ABS", want: "ABS"},
		// A fresh workspace (no metadata.json yet) whose inherited relative
		// dolt_data_dir finalize saves: its server serves that directory,
		// which ResolveDoltDir cannot see before the save.
		{name: "inherited relative dolt_data_dir", dataDir: "data", want: "data"},
		{name: "inherited absolute dolt_data_dir", dataDir: "ABS", want: "dolt"},
		{name: "inherited absolute dolt_data_dir under BEADS_DOLT_DATA_DIR", dataDir: "ABS", envDataDir: true, want: "ENV"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			endpointCredentialsEnv(t, nil)
			t.Setenv("BEADS_DOLT_DATA_DIR", "")
			beadsDir := filepath.Join(t.TempDir(), ".beads")
			if err := os.MkdirAll(beadsDir, 0o700); err != nil {
				t.Fatal(err)
			}
			abs := filepath.Join(t.TempDir(), "elsewhere")
			env := filepath.Join(t.TempDir(), "env-data")
			cfg := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeServer, DoltDatabase: "dir_db", DoltDataDir: tc.dataDir, Database: tc.database}
			if cfg.DoltDataDir == "ABS" {
				cfg.DoltDataDir = abs
			}
			if cfg.Database == "ABS" {
				cfg.Database = abs
			}
			if tc.envDataDir {
				t.Setenv("BEADS_DOLT_DATA_DIR", env)
			}
			if tc.ownMeta {
				if err := cfg.Save(beadsDir); err != nil {
					t.Fatal(err)
				}
			}
			var cloned []string
			orig := remoteCloneIntoDataDir
			t.Cleanup(func() { remoteCloneIntoDataDir = orig })
			remoteCloneIntoDataDir = func(_ context.Context, doltDir, _, _ string) error {
				cloned = append(cloned, doltDir)
				return nil
			}
			err := cloneFromRemote(context.Background(), beadsDir, "file:///nonexistent/remote", "dir_db", cfg)
			if err != nil {
				t.Fatalf("clone: %v", err)
			}
			want := filepath.Join(beadsDir, tc.want)
			switch tc.want {
			case "ABS":
				want = abs
			case "ENV":
				want = env
			}
			if len(cloned) != 1 || cloned[0] != want {
				t.Errorf("cloned into %v, want %s", cloned, want)
			}
			// What bd's server serves once bootstrap has written cfg.
			if err := cfg.Save(beadsDir); err != nil {
				t.Fatal(err)
			}
			if served := doltserver.ResolveDoltDir(beadsDir); served != want {
				t.Errorf("bd's server serves %s, the clone went to %s", served, want)
			}
		})
	}
}

// withoutTestPaths replaces each of a test's own paths (and its resolved
// form) in msg with a placeholder, so a check for a banned port number reads
// only the text bd wrote, never a random temp-directory suffix.
func withoutTestPaths(msg string, paths ...string) string {
	for _, p := range paths {
		if p == "" {
			continue
		}
		forms := []string{p, filepath.ToSlash(p)}
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			forms = append(forms, resolved, filepath.ToSlash(resolved))
		}
		for _, f := range forms {
			msg = strings.ReplaceAll(msg, f, "<path>")
		}
	}
	return msg
}

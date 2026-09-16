//go:build cgo && unix

package main

// Front-door capability matrix across storage topologies.
//
// THIS IS A CHARACTERIZATION TEST. Every expectation below records what bd
// does TODAY, including the behavior that is wrong. It is green because it
// describes the present, not because the present is good. Rows carrying a
// knownBad string are tracked defects; the slice that fixes one must flip its
// expectation in the same diff, and that flip is the review artifact proving
// the fix landed.
//
// Do NOT "fix" a failing row by relaxing it. A red row means behavior moved;
// either the move was intended (flip the row) or it is a regression.
//
// Reading the output: each cell is classified by `matrixOutcome` (what
// happened) and `matrixReason` (why it is allowed to be that way).
// REFUSED_TYPED + design is a deliberate, permanent policy refusal.
// REFUSED_TYPED + unimplemented is a capability gap a later slice closes.
// WRONG_ANSWER and DEGRADED are bugs bd currently ships. The distinction is
// the whole point of this file: the refusal surface today mixes all three and
// a reader cannot otherwise tell them apart.
//
// The `reason` column is the test's own authority. Production does not yet
// carry a Reason field on its refusals; when it does, this test should assert
// that the two agree.

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/testutil"
)

// ---------------------------------------------------------------------------
// Outcome taxonomy
// ---------------------------------------------------------------------------

type matrixOutcome string

const (
	// outcomeHonored: the command ran and the topology did not stand in its way.
	outcomeHonored matrixOutcome = "HONORED"
	// outcomeRefusedTyped: front-door refusal carrying a stable proxy.* code.
	outcomeRefusedTyped matrixOutcome = "REFUSED_TYPED"
	// outcomeRefusedUntyped: refused with a bare string and no machine-readable
	// code. A downstream JSON consumer cannot classify these.
	outcomeRefusedUntyped matrixOutcome = "REFUSED_UNTYPED"
	// outcomeWrongAnswer: exited successfully and reported something false.
	outcomeWrongAnswer matrixOutcome = "WRONG_ANSWER"
	// outcomeDegraded: exited successfully having silently skipped the work.
	outcomeDegraded matrixOutcome = "DEGRADED"
	// outcomeError: reached the command, which then failed on its own merits.
	// Not a topology refusal — the front door let it through.
	outcomeError matrixOutcome = "ERROR"
)

type matrixReason string

const (
	reasonNA = matrixReason("-")
	// reasonDesign: refusing is correct and is expected to stay that way.
	reasonDesign = matrixReason("design")
	// reasonUnimplemented: refusing is a capability gap, not a policy.
	reasonUnimplemented = matrixReason("unimplemented")
)

// expectation is the observed contract for one command on one topology.
type expectation struct {
	outcome matrixOutcome
	// code is the proxy.* capability code, for outcomeRefusedTyped only.
	code string
	// substr must appear in the combined output.
	substr string
	// absent, when set, must NOT appear in the combined output. This is how a
	// silent omission (config show dropping database-backed settings) is
	// pinned down as a wrong answer rather than a formatting difference.
	absent string
	// knownBad names the defect this cell documents. Required for
	// WRONG_ANSWER and DEGRADED; a later slice deletes it when it flips.
	knownBad string
	reason   matrixReason
}

// probe is one command exercised across every topology.
//
// Adding a command to the matrix is a single row here. `direct` applies to
// every direct-class topology and `proxied` to every proxied-class one, which
// mirrors bd's own ProxyMode axis; `override` pins a single topology when the
// two-class default is not enough. When a later slice makes managed-local
// honor something external topologies still refuse, that lands as one
// override entry, not new plumbing.
//
// notProbedOn withholds a command from named topologies, mapping each to the
// reason. It is for a command that is unsafe to RUN there, which is a
// different statement from an expectation and must not be written as one:
// deliberatelyNotProbed already says this at whole-command granularity, and
// this is the same idea one cell down. The reason is printed in the report, so
// the gap stays visible instead of reading as coverage.
type probe struct {
	name        string
	args        []string
	direct      expectation
	proxied     expectation
	override    map[string]expectation
	notProbedOn map[string]string
}

func (p probe) want(topology string, class topologyClass) expectation {
	if e, ok := p.override[topology]; ok {
		return e
	}
	if class == classProxied {
		return p.proxied
	}
	return p.direct
}

// ---------------------------------------------------------------------------
// The matrix
// ---------------------------------------------------------------------------

const (
	topoEmbedded       = "embedded"
	topoDirectLocal    = "direct-local-server"
	topoDirectExternal = "direct-external-server"
	topoProxiedLocal   = "proxied-local"
	topoProxiedTCP     = "proxied-external-tcp"
)

// capabilityProbes is the table. One row per command family from the 1.3.1
// priority order, plus control rows that must be honored everywhere.
var capabilityProbes = []probe{
	// --- control: plain CRUD must work on every topology -------------------
	{
		name:    "list",
		args:    []string{"list"},
		direct:  expectation{outcome: outcomeHonored, reason: reasonNA},
		proxied: expectation{outcome: outcomeHonored, reason: reasonNA},
	},
	{
		// `list` is the one command that carries --max-rows through the
		// proxied reader (proxy_capability.go:230).
		name:    "list --max-rows",
		args:    []string{"list", "--max-rows", "5"},
		direct:  expectation{outcome: outcomeHonored, reason: reasonNA},
		proxied: expectation{outcome: outcomeHonored, reason: reasonNA},
	},

	// --- the two verified wrong answers ------------------------------------
	{
		// dolt status reads the CLASSIC pidfile. Proxied mode writes
		// proxy-child.pid under the proxied root, so status reports "not
		// running" while the proxy is serving CRUD. Direct-external has the
		// same shape for a different reason: bd never wrote a pidfile for a
		// server it does not own, so it reports a live server as down.
		// Both direct server topologies report the truth: a bd-owned local
		// server and an externally-managed one both come back running, with
		// host/port/version. Proxied is the outlier.
		name: "dolt status",
		args: []string{"dolt", "status"},
		direct: expectation{
			outcome: outcomeHonored, substr: `"running": true`, reason: reasonNA,
		},
		proxied: expectation{
			// The proxy and its child are live and serving CRUD — every
			// honored row on this topology proves it — yet status reports
			// running=false with pid 0 and port 0.
			outcome: outcomeWrongAnswer, substr: `"running": false`,
			knownBad: "proxied status reads the classic pidfile; proxy.IsRunning is never called (design 1.2 / slice S1)",
			reason:   reasonNA,
		},
		override: map[string]expectation{
			topoEmbedded: {
				outcome: outcomeHonored, substr: `"mode": "embedded"`, reason: reasonNA,
			},
		},
	},
	{
		// collectDatabaseEntries calls ensureDirectMode, which fails under
		// proxied, and the error is swallowed with `return nil`. Every
		// database-backed setting silently disappears with no marker.
		name: "config show",
		args: []string{"config", "show"},
		direct: expectation{
			outcome: outcomeHonored, substr: `"source": "database"`, reason: reasonNA,
		},
		proxied: expectation{
			outcome: outcomeWrongAnswer, substr: `"source": "default"`, absent: `"source": "database"`,
			knownBad: "config_show.go collectDatabaseEntries swallows the proxied error and omits every DB-stored setting (design 1.5 / slice S6)",
			reason:   reasonNA,
		},
	},

	// --- dolt start: probed only where it cannot start anything -------------
	{
		// §3.5 proposes probing this against a shut-down proxy. It is probed
		// on EMBEDDED only, because embedded is the single topology where the
		// command cannot spawn a server: with no SQL server configured it
		// short-circuits before it gets that far. On all four others it
		// starts, or tries to start, a real sql-server — the notProbedOn
		// reasons below say precisely what each one does, measured by running
		// it there rather than reasoned about.
		//
		// `proxied` is deliberately left unset: both proxied topologies are
		// withheld, so nothing reads it. Slice S1 turns the proxied hazard
		// into a typed refusal; that slice fills this in and drops the two
		// proxied entries below, which is the flip that proves it landed.
		name: "dolt start",
		args: []string{"dolt", "start"},
		direct: expectation{
			outcome: outcomeError,
			substr:  "'bd dolt start' is not supported in embedded mode",
			reason:  reasonNA,
		},
		notProbedOn: map[string]string{
			topoDirectLocal: "starts a SECOND bd-managed sql-server over a workspace that ALREADY has one running, " +
				"and reports success doing it (observed: \"Dolt server started (PID …, port …)\" at exit 0). It overwrites " +
				"this workspace's .beads/dolt-server.pid with the new PID, so the first server is left with nothing " +
				"pointing at it and `bd dolt stop` can no longer reach it. Probing it would make this matrix orphan a " +
				"process on every run. This is a defect in its own right and is NOT the proxied hazard slice S1 fixes.",
			topoDirectExternal: "tries to start a LOCAL sql-server on the EXTERNAL endpoint's own port — the workspace " +
				"points at a server bd does not own, and the command does not check that before acting. It fails here only " +
				"because the container already holds the port (observed: \"cannot start dolt server on port N: port N is " +
				"busy but cannot identify the process\", exit 1). Safety by collision is not safety, and not something to " +
				"build an assertion on.",
			topoProxiedLocal: "spawns a SECOND, unmanaged dolt sql-server over the live proxy child's data directory — " +
				"the default proxied root IS .beads/dolt (design 1.2). That server is recorded in no pidfile bd's own " +
				"`dolt stop` reads, so teardown by recorded PID cannot be guaranteed and this matrix must not create one. " +
				"Slice S1 makes it a typed refusal (proxy.dolt_start.conflict); probe it here then.",
			topoProxiedTCP: "same hazard as proxied-local: a second, unmanaged sql-server over the proxied root, " +
				"recorded in no pidfile this test could tear down by PID. Slice S1 makes it a typed refusal; probe it then.",
		},
	},

	// --- doctor -------------------------------------------------------------
	{
		name: "doctor",
		args: []string{"doctor"},
		direct: expectation{
			outcome: outcomeHonored, substr: `"checks"`, reason: reasonNA,
		},
		proxied: expectation{
			outcome: outcomeRefusedTyped, code: "proxy.doctor.unsupported",
			substr: "doctor is not supported in proxied-server mode",
			reason: reasonUnimplemented,
		},
		override: map[string]expectation{
			// Embedded does not refuse: it prints guidance and exits 0, so a
			// CI gate running `bd doctor` scores a pass having checked nothing.
			topoEmbedded: {
				outcome: outcomeDegraded, substr: "not yet supported in embedded mode",
				knownBad: "doctor exits 0 on embedded without running checks; a CI gate cannot tell this from success (design 1.5 names the proxied twin; the embedded one is found by this matrix)",
				reason:   reasonNA,
			},
		},
	},

	// --- backup family (consumer priority #1, slice S3) ---------------------
	{
		name: "backup status",
		args: []string{"backup", "status"},
		direct: expectation{
			outcome: outcomeHonored, substr: `"configured"`, reason: reasonNA,
		},
		proxied: expectation{
			outcome: outcomeRefusedTyped, code: "proxy.backup.unsupported",
			substr: "backup status is not supported in proxied-server mode",
			reason: reasonUnimplemented,
		},
	},
	{
		name: "backup sync",
		args: []string{"backup", "sync"},
		direct: expectation{
			outcome: outcomeError, substr: "no backup destination configured", reason: reasonNA,
		},
		proxied: expectation{
			outcome: outcomeRefusedTyped, code: "proxy.backup.unsupported",
			substr: "backup sync is not supported in proxied-server mode",
			reason: reasonUnimplemented,
		},
	},

	// --- version-control verbs (slice S4) -----------------------------------
	{
		name: "dolt commit",
		args: []string{"dolt", "commit", "-m", "matrix probe"},
		direct: expectation{
			outcome: outcomeHonored, reason: reasonNA,
		},
		proxied: expectation{
			outcome: outcomeRefusedTyped, code: "proxy.dolt_commit.unsupported",
			substr: "dolt commit is not supported in proxied-server mode",
			reason: reasonUnimplemented,
		},
	},
	{
		name: "dolt push",
		args: []string{"dolt", "push"},
		direct: expectation{
			outcome: outcomeHonored, substr: "No remote is configured", reason: reasonNA,
		},
		proxied: expectation{
			outcome: outcomeRefusedTyped, code: "proxy.dolt_push.unsupported",
			substr: "dolt push is not supported in proxied-server mode",
			reason: reasonUnimplemented,
		},
	},
	{
		name: "dolt remote list",
		args: []string{"dolt", "remote", "list"},
		direct: expectation{
			// Renders an empty JSON array; there is no stable marker beyond
			// "exit 0 and no refusal", which is what this row asserts.
			outcome: outcomeHonored, reason: reasonNA,
		},
		proxied: expectation{
			outcome: outcomeRefusedTyped, code: "proxy.dolt_remote.unsupported",
			substr: "dolt remote list is not supported in proxied-server mode",
			reason: reasonUnimplemented,
		},
	},
	{
		name: "sync",
		args: []string{"sync"},
		direct: expectation{
			outcome: outcomeHonored, substr: `"status": "no-remote"`, reason: reasonNA,
		},
		proxied: expectation{
			// Refused three independent ways today: the maintenance table, the
			// history class, and an inline RunE string (design 1.3).
			outcome: outcomeRefusedTyped, code: "proxy.sync.unsupported",
			substr: "sync is not supported in proxied-server mode",
			reason: reasonUnimplemented,
		},
	},

	// --- refused by design: shared-history semantics are undefined ----------
	{
		name: "branch",
		args: []string{"branch"},
		direct: expectation{
			outcome: outcomeHonored, substr: `"branches"`, reason: reasonNA,
		},
		proxied: expectation{
			outcome: outcomeRefusedTyped, code: "proxy.branch.unsupported",
			substr: "branch is not supported in proxied-server mode",
			reason: reasonDesign,
		},
	},
	{
		name: "flatten",
		args: []string{"flatten"},
		direct: expectation{
			// Refuses without --force, which is its own safety interlock and
			// nothing to do with topology.
			outcome: outcomeError, substr: "irreversible", reason: reasonNA,
		},
		proxied: expectation{
			outcome: outcomeRefusedTyped, code: "proxy.flatten.unsupported",
			substr: "flatten is not supported in proxied-server mode",
			reason: reasonDesign,
		},
	},

	// --- transform family (slice S7) ----------------------------------------
	{
		name: "rename",
		args: []string{"rename", "zz-000000", "zz-111111"},
		direct: expectation{
			outcome: outcomeError, reason: reasonNA,
		},
		proxied: expectation{
			outcome: outcomeRefusedTyped, code: "proxy.transform.unsupported",
			substr: "rename is not supported in proxied-server mode",
			reason: reasonUnimplemented,
		},
	},

	// --- formula verbs (slice S7, demand-gated) ------------------------------
	{
		name: "cook",
		args: []string{"cook", "matrix-probe-no-such-formula.yaml"},
		direct: expectation{
			outcome: outcomeError, reason: reasonNA,
		},
		proxied: expectation{
			outcome: outcomeRefusedTyped, code: "proxy.formula.unsupported",
			substr: "cook is not supported in proxied-server mode",
			reason: reasonUnimplemented,
		},
	},

	// --- flag-level capability ------------------------------------------------
	{
		// The cap is refused rather than silently dropped, which is correct.
		// The fix is threading it through the UOW reader as `list` already
		// does, not lifting the refusal.
		name: "ready --max-rows",
		args: []string{"ready", "--max-rows", "5"},
		direct: expectation{
			outcome: outcomeHonored, reason: reasonNA,
		},
		proxied: expectation{
			outcome: outcomeRefusedTyped, code: "proxy.max_rows.unsupported",
			substr: "--max-rows / BEADS_MAX_ROWS is not supported in proxied-server mode",
			reason: reasonUnimplemented,
		},
	},

	// --- compact -------------------------------------------------------------
	{
		// HONORED proxied, which is NOT what proxyMaintenanceRefusals["compact"]
		// suggests on a quick read. validateProxyMaintenanceBeforeProvider
		// short-circuits to nil when the command has no --dolt flag
		// (proxy_capability.go:168-172), and root `bd compact` — the Dolt
		// history compaction command — has no such flag. So that table row is
		// unreachable from this command path. Recorded here because a reader
		// auditing the table would otherwise expect a refusal.
		name: "compact",
		args: []string{"compact"},
		direct: expectation{
			outcome: outcomeHonored, reason: reasonNA,
		},
		proxied: expectation{
			outcome: outcomeHonored, reason: reasonNA,
		},
	},

	// --- migrate schema: already lifted in 1.3.0 -----------------------------
	{
		// The one family the 1.3.0 release already lifted. It is HONORED
		// proxied, but reports no applied count — S6 makes it report one.
		name: "migrate schema",
		args: []string{"migrate", "schema"},
		direct: expectation{
			outcome: outcomeHonored, substr: "Schema", reason: reasonNA,
		},
		proxied: expectation{
			outcome: outcomeDegraded, substr: "reconciled during provider open",
			knownBad: "proxied migrate schema reports no applied count (design 1.5 / slice S6)",
			reason:   reasonNA,
		},
	},
}

// deliberatelyNotProbed records commands this matrix will not run, and why.
// Leaving them undocumented would let a reader mistake absence for coverage.
var deliberatelyNotProbed = map[string]string{
	// `dolt start` is no longer here: it is a probe row with per-topology
	// notProbedOn reasons, which is strictly more informative than withholding
	// the whole command. Embedded is probed; the other four carry their own
	// measured reason.
	"restore": "`bd restore` restores an ISSUE, not a backup; the backup verb is `bd backup restore`, which is already " +
		"covered by the backup rows. Probing it adds a row that says nothing about the backup family.",
	"backup restore": "would need a real backup destination to distinguish a topology refusal from a missing-backup error. " +
		"Covered indirectly by `backup status` / `backup sync`; revisit in slice S3 when the family gains a proxied route.",
}

// ---------------------------------------------------------------------------
// Topologies
// ---------------------------------------------------------------------------

type topologyClass string

const (
	classDirect  topologyClass = "direct"
	classProxied topologyClass = "proxied"
)

// topologyWorkspace is one initialized bd workspace plus everything needed to
// run the real binary against it and to assert proxied side effects.
type topologyWorkspace struct {
	name      string
	class     topologyClass
	dir       string
	beadsDir  string
	proxyRoot string // empty for non-proxied topologies
	env       []string
}

// run executes the real bd binary and returns stdout, stderr and the exit code.
func (w topologyWorkspace) run(t *testing.T, bd string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = w.dir
	cmd.Env = w.env
	stdout, stderr, err := runCommandBuffers(t, cmd)
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("[%s] bd %s could not run: %v\nstdout:\n%s\nstderr:\n%s",
				w.name, strings.Join(args, " "), err, stdout.String(), stderr.String())
		}
		code = ee.ExitCode()
	}
	return stdout.String(), stderr.String(), code
}

type topologySpec struct {
	name  string
	class topologyClass
	// make builds the workspace, or calls t.Skip with a reason. Construction
	// is lazy so one unavailable topology never hides the others.
	make func(t *testing.T, bd string) topologyWorkspace
}

// embeddedTopology is always available: no server, no docker, no dolt binary.
func embeddedTopology() topologySpec {
	return topologySpec{
		name: topoEmbedded, class: classDirect,
		make: func(t *testing.T, bd string) topologyWorkspace {
			dir, beadsDir, _ := bdInit(t, bd, "--prefix", "mtxe")
			return topologyWorkspace{
				name: topoEmbedded, class: classDirect,
				dir: dir, beadsDir: beadsDir, env: bdEnv(dir),
			}
		},
	}
}

// directLocalServerTopology is `bd init --server` with no port: bd spawns and
// owns a dolt sql-server under .beads on an OS-allocated ephemeral port.
//
// It needs an env that does NOT carry the package TestMain's BEADS_TEST_MODE
// (which disables auto-start and pins the port to 1) and does NOT carry
// bdEnv's BEADS_DOLT_AUTO_START=0. Teardown is `bd dolt stop`, which reads
// .beads/dolt-server.pid — the only PID this test is entitled to stop.
func directLocalServerTopology() topologySpec {
	return topologySpec{
		name: topoDirectLocal, class: classDirect,
		make: func(t *testing.T, bd string) topologyWorkspace {
			testutil.RequireDoltBinary(t)
			dir := t.TempDir()
			initGitRepoAt(t, dir)
			beadsDir := filepath.Join(dir, ".beads")
			env := directLocalServerEnv(dir)

			t.Cleanup(func() { stopDirectLocalServer(t, bd, dir, beadsDir, env) })

			cmd := exec.Command(bd, "init", "--quiet", "--backend", "dolt", "--server",
				"--prefix", "mtxdl", "--non-interactive", "--skip-agents", "--skip-hooks")
			cmd.Dir = dir
			cmd.Env = env
			stdout, stderr, err := runCommandBuffers(t, cmd)
			if err != nil {
				t.Fatalf("bd init --server (direct local) failed: %v\nstdout:\n%s\nstderr:\n%s",
					err, stdout.String(), stderr.String())
			}
			return topologyWorkspace{
				name: topoDirectLocal, class: classDirect,
				dir: dir, beadsDir: beadsDir, env: env,
			}
		},
	}
}

// directExternalServerTopology reuses newServerModeProject: `bd init --server`
// pointed at the shared test Dolt container, which bd did not start.
func directExternalServerTopology() topologySpec {
	return topologySpec{
		name: topoDirectExternal, class: classDirect,
		make: func(t *testing.T, bd string) topologyWorkspace {
			p := newServerModeProject(t, bd, "mtxdx")
			return topologyWorkspace{
				name: topoDirectExternal, class: classDirect,
				dir: p.dir, beadsDir: p.beadsDir, env: p.env,
			}
		},
	}
}

// proxiedLocalTopology is the managed-local proxy plus its child dolt server.
func proxiedLocalTopology() topologySpec {
	return topologySpec{
		name: topoProxiedLocal, class: classProxied,
		make: func(t *testing.T, bd string) topologyWorkspace {
			requireManagedLocalProxiedEnv(t)
			p := bdManagedLocalInit(t, bd, "mtxpl", 5*time.Minute)
			return topologyWorkspace{
				name: topoProxiedLocal, class: classProxied,
				dir: p.dir, beadsDir: p.beadsDir, proxyRoot: p.proxyRoot, env: bdProxiedEnv(p.dir),
			}
		},
	}
}

// proxiedExternalTCPTopology fronts the shared Dolt container with a proxy.
func proxiedExternalTCPTopology() topologySpec {
	return topologySpec{
		name: topoProxiedTCP, class: classProxied,
		make: func(t *testing.T, bd string) topologyWorkspace {
			requireProxiedServerEnv(t)
			p := newSharedProxiedProject(t, bd, "mtxpx")
			return topologyWorkspace{
				name: topoProxiedTCP, class: classProxied,
				dir: p.dir, beadsDir: p.beadsDir, proxyRoot: p.proxyRoot, env: bdProxiedEnv(p.dir),
			}
		},
	}
}

// ---------------------------------------------------------------------------
// Entry points
// ---------------------------------------------------------------------------

// TestProxiedServerTopologyCapabilityMatrix is the primary entry point.
//
// Named TestProxiedServer* so .github/scripts/proxied-test-shard.sh discovers
// it, which puts it in pr-risk.yml's `test-proxied-cmd` lane — a lane that
// runs on pull_request, not only on push-to-main.
//
// It covers four of the five topologies. managed-local is gated on
// BEADS_TEST_PROXIED_LOCAL, which that lane does not set; it is covered by
// TestManagedLocalProxiedCapabilityMatrix in the proxied-local smoke lane
// instead. Both entry points share this table.
func TestProxiedServerTopologyCapabilityMatrix(t *testing.T) {
	runCapabilityMatrix(t, []topologySpec{
		embeddedTopology(),
		directLocalServerTopology(),
		directExternalServerTopology(),
		proxiedLocalTopology(),
		proxiedExternalTCPTopology(),
	})
}

// ---------------------------------------------------------------------------
// Runner
// ---------------------------------------------------------------------------

type cellResult struct {
	topology string
	probe    string
	outcome  matrixOutcome
	reason   matrixReason
	knownBad bool
}

func runCapabilityMatrix(t *testing.T, topologies []topologySpec) {
	bd := buildEmbeddedBD(t)

	var results []cellResult
	exercised := map[string]bool{}
	skipped := map[string]string{}

	for _, spec := range topologies {
		t.Run(spec.name, func(t *testing.T) {
			// A topology that cannot be built here must say so out loud. A
			// silently skipped topology is the main way this matrix could
			// look green while proving nothing.
			defer func() {
				if t.Skipped() && skipped[spec.name] == "" {
					skipped[spec.name] = "gated off in this environment"
				}
			}()

			w := spec.make(t, bd)
			exercised[spec.name] = true

			// Refusal rows run first, behind the no-side-effect invariants:
			// the provider must never have been constructed. Ordering matters
			// because a later honored probe legitimately starts the proxy.
			refusals, others := partitionProbes(capabilityProbes, spec.name, spec.class)

			if w.class == classProxied && len(refusals) > 0 {
				if err := proxy.Shutdown(w.proxyRoot); err != nil {
					t.Logf("pre-refusal proxy.Shutdown(%s): %v", w.proxyRoot, err)
				}
			}
			before := w.snapshotArtifacts(t)

			for _, p := range refusals {
				p := p
				t.Run(p.name, func(t *testing.T) {
					want := p.want(spec.name, spec.class)
					results = append(results, assertCell(t, w, bd, p, want))
					w.assertNoProviderSideEffects(t, p, before)
				})
			}
			for _, p := range others {
				p := p
				t.Run(p.name, func(t *testing.T) {
					want := p.want(spec.name, spec.class)
					results = append(results, assertCell(t, w, bd, p, want))
				})
			}

			assertCRUDControl(t, w, bd)
		})
	}

	reportMatrix(t, topologies, results, exercised, skipped)
}

// partitionProbes puts the refusal rows for this topology first, preserving
// table order within each group.
func partitionProbes(all []probe, topology string, class topologyClass) (refusals, others []probe) {
	for _, p := range all {
		if _, withheld := p.notProbedOn[topology]; withheld {
			continue
		}
		switch p.want(topology, class).outcome {
		case outcomeRefusedTyped, outcomeRefusedUntyped:
			refusals = append(refusals, p)
		default:
			others = append(others, p)
		}
	}
	return refusals, others
}

// assertCell runs one probe and asserts the observed outcome class.
func assertCell(t *testing.T, w topologyWorkspace, bd string, p probe, want expectation) cellResult {
	t.Helper()

	// --json before the subcommand, plus BEADS_JSON=1: a few nested front
	// doors do not inherit persistent flags placed after their args.
	args := append([]string{"--json"}, p.args...)
	stdout, stderr, code := w.run(t, bd, args...)
	combined := stdout + stderr

	fail := func(format string, a ...interface{}) {
		t.Fatalf("[%s] bd %s\n  want %s (reason=%s)%s\n  %s\n  exit=%d\n  stdout:\n%s\n  stderr:\n%s",
			w.name, strings.Join(p.args, " "), want.outcome, want.reason,
			knownBadSuffix(want), fmt.Sprintf(format, a...), code, stdout, stderr)
	}

	switch want.outcome {
	case outcomeRefusedTyped:
		if code == 0 {
			fail("expected a typed refusal, but the command succeeded")
		}
		var got struct {
			Code    string `json:"code"`
			Error   string `json:"error"`
			Mutates bool   `json:"mutates"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &got); err == nil && got.Code != "" {
			if got.Code != want.code {
				fail("refusal code = %q, want %q", got.Code, want.code)
			}
			if got.Mutates {
				fail("refusal reported mutates=true; refusals must never mutate")
			}
			if want.substr != "" && !strings.Contains(got.Error, want.substr) {
				fail("refusal message %q does not contain %q", got.Error, want.substr)
			}
		} else {
			// Documented fallback: nested commands render the typed refusal
			// through the text path before config-backed JSON is applied.
			if want.substr != "" && !strings.Contains(combined, want.substr) {
				fail("no typed refusal JSON and output lacks %q", want.substr)
			}
		}

	case outcomeRefusedUntyped:
		if code == 0 {
			fail("expected an untyped refusal, but the command succeeded")
		}
		if strings.Contains(stdout, `"code"`) {
			fail("refusal carried a typed code; this row should be REFUSED_TYPED")
		}
		if want.substr != "" && !strings.Contains(combined, want.substr) {
			fail("output lacks %q", want.substr)
		}

	case outcomeHonored:
		if code != 0 {
			fail("expected success")
		}
		if strings.Contains(combined, "is not supported in proxied-server mode") {
			fail("exit 0 but the output carries a proxied refusal")
		}
		if want.substr != "" && !strings.Contains(combined, want.substr) {
			fail("output lacks %q", want.substr)
		}

	case outcomeWrongAnswer, outcomeDegraded:
		if want.knownBad == "" {
			t.Fatalf("[%s] probe %q is %s but carries no knownBad; every bug this matrix records must name itself",
				w.name, p.name, want.outcome)
		}
		if code != 0 {
			fail("expected exit 0 (that is what makes it a silent wrong answer)")
		}
		if want.substr != "" && !strings.Contains(combined, want.substr) {
			fail("output lacks the marker %q", want.substr)
		}

	case outcomeError:
		if code == 0 {
			fail("expected a non-zero exit from the command itself")
		}
		if strings.Contains(combined, "is not supported in proxied-server mode") {
			fail("this row expects the command's own error, but it was refused by topology policy")
		}
		if want.substr != "" && !strings.Contains(combined, want.substr) {
			fail("output lacks %q", want.substr)
		}

	default:
		t.Fatalf("unhandled outcome %q", want.outcome)
	}

	if want.absent != "" && strings.Contains(combined, want.absent) {
		fail("output unexpectedly contains %q", want.absent)
	}

	return cellResult{
		topology: w.name, probe: p.name,
		outcome: want.outcome, reason: want.reason, knownBad: want.knownBad != "",
	}
}

// assertCRUDControl proves the workspace is genuinely functional. Without it
// a topology whose store was broken would report a tidy column of refusals
// and look like a successful characterization.
func assertCRUDControl(t *testing.T, w topologyWorkspace, bd string) {
	t.Helper()
	t.Run("control create+show", func(t *testing.T) {
		stdout, stderr, code := w.run(t, bd, "--json", "create", "matrix control probe", "-p", "2")
		if code != 0 {
			t.Fatalf("[%s] control create failed (exit %d)\nstdout:\n%s\nstderr:\n%s", w.name, code, stdout, stderr)
		}
		var created struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &created); err != nil || created.ID == "" {
			t.Fatalf("[%s] control create did not return an id: %v\nstdout:\n%s", w.name, err, stdout)
		}
		showOut, showErr, showCode := w.run(t, bd, "--json", "show", created.ID)
		if showCode != 0 {
			t.Fatalf("[%s] control show %s failed (exit %d)\nstdout:\n%s\nstderr:\n%s",
				w.name, created.ID, showCode, showOut, showErr)
		}
		if !strings.Contains(showOut, created.ID) {
			t.Fatalf("[%s] control show did not round-trip %s\nstdout:\n%s", w.name, created.ID, showOut)
		}
	})
}

// ---------------------------------------------------------------------------
// Side-effect invariants
// ---------------------------------------------------------------------------

// snapshotArtifacts reuses the existing history-matrix snapshot so both tests
// agree on what "durable artifacts" means.
func (w topologyWorkspace) snapshotArtifacts(t *testing.T) []byte {
	t.Helper()
	if w.class != classProxied {
		return nil
	}
	return snapshotHistoryArtifacts(t, proxiedProject{beadsDir: w.beadsDir, proxyRoot: w.proxyRoot})
}

// assertNoProviderSideEffects is the regression net the design calls for: a
// refusal must be decided before any workspace side effect, so the provider
// must never have been constructed and nothing durable may have changed.
func (w topologyWorkspace) assertNoProviderSideEffects(t *testing.T, p probe, before []byte) {
	t.Helper()
	if w.class != classProxied {
		return
	}
	if _, err := os.Stat(filepath.Join(w.proxyRoot, proxy.PIDFileName)); err == nil {
		t.Fatalf("[%s] %s: proxy started for a refused command; the refusal must precede provider construction",
			w.name, p.name)
	}
	if after := w.snapshotArtifacts(t); string(before) != string(after) {
		t.Fatalf("[%s] %s: durable artifacts changed during a refusal", w.name, p.name)
	}
}

// ---------------------------------------------------------------------------
// Reporting
// ---------------------------------------------------------------------------

// reportMatrix prints the matrix so a reader can tell the classes apart
// without reading the source. This is the completion signal every later slice
// checks itself against.
func reportMatrix(t *testing.T, topologies []topologySpec, results []cellResult, exercised map[string]bool, skipped map[string]string) {
	t.Helper()

	var b strings.Builder
	b.WriteString("\n=== front-door capability matrix (observed behavior, not desired) ===\n\n")

	byProbe := map[string]map[string]cellResult{}
	for _, r := range results {
		if byProbe[r.probe] == nil {
			byProbe[r.probe] = map[string]cellResult{}
		}
		byProbe[r.probe][r.topology] = r
	}

	var names []string
	for _, spec := range topologies {
		names = append(names, spec.name)
	}

	// Wide enough for the longest label, "REFUSED_TYPED/unimplemented !".
	const cellWidth = 29

	fmt.Fprintf(&b, "%-22s", "COMMAND")
	for _, n := range names {
		fmt.Fprintf(&b, " | %-*s", cellWidth, n)
	}
	b.WriteString("\n")
	b.WriteString(strings.Repeat("-", 22+len(names)*(cellWidth+3)) + "\n")

	for _, p := range capabilityProbes {
		fmt.Fprintf(&b, "%-22s", p.name)
		for _, n := range names {
			cell, ok := byProbe[p.name][n]
			if !ok {
				// "not probed" and "not exercised" are different claims: the
				// first is a decision recorded in the table, the second is a
				// topology this run could not build.
				label := "(not exercised)"
				if _, withheld := p.notProbedOn[n]; withheld {
					label = "(not probed)"
				}
				fmt.Fprintf(&b, " | %-*s", cellWidth, label)
				continue
			}
			label := string(cell.outcome)
			if cell.reason != reasonNA && cell.reason != "" {
				label += "/" + string(cell.reason)
			}
			if cell.knownBad {
				label += " !"
			}
			fmt.Fprintf(&b, " | %-*s", cellWidth, label)
		}
		b.WriteString("\n")
	}

	counts := map[matrixOutcome]int{}
	designRefusals, unimplementedRefusals, knownBads := 0, 0, 0
	for _, r := range results {
		counts[r.outcome]++
		if r.outcome == outcomeRefusedTyped || r.outcome == outcomeRefusedUntyped {
			switch r.reason {
			case reasonDesign:
				designRefusals++
			case reasonUnimplemented:
				unimplementedRefusals++
			}
		}
		if r.knownBad {
			knownBads++
		}
	}

	fmt.Fprintf(&b, "\ncells: %d over %d commands x %d exercised topologies\n",
		len(results), len(capabilityProbes), len(exercised))
	var outcomes []string
	for o, c := range counts {
		outcomes = append(outcomes, fmt.Sprintf("%s=%d", o, c))
	}
	sort.Strings(outcomes)
	fmt.Fprintf(&b, "outcomes: %s\n", strings.Join(outcomes, "  "))
	fmt.Fprintf(&b, "refusals: %d refused-by-design, %d not-yet-implemented\n", designRefusals, unimplementedRefusals)
	fmt.Fprintf(&b, "tracked defects (knownBad, marked !): %d cells\n", knownBads)

	b.WriteString("\ntopologies exercised:\n")
	for _, spec := range topologies {
		if exercised[spec.name] {
			fmt.Fprintf(&b, "  [exercised]     %s\n", spec.name)
		} else {
			fmt.Fprintf(&b, "  [NOT EXERCISED] %s — %s\n", spec.name, skipped[spec.name])
		}
	}
	b.WriteString("\ndeliberately not probed:\n")
	var notProbed []string
	for k := range deliberatelyNotProbed {
		notProbed = append(notProbed, k)
	}
	sort.Strings(notProbed)
	for _, k := range notProbed {
		fmt.Fprintf(&b, "  %s — %s\n", k, deliberatelyNotProbed[k])
	}
	// The per-cell half of the same statement, so a "(not probed)" cell in the
	// table above always has its reason printed underneath it.
	for _, p := range capabilityProbes {
		var withheld []string
		for topology := range p.notProbedOn {
			withheld = append(withheld, topology)
		}
		sort.Strings(withheld)
		for _, topology := range withheld {
			fmt.Fprintf(&b, "  %s on %s — %s\n", p.name, topology, p.notProbedOn[topology])
		}
	}

	t.Log(b.String())

	// A run that built no topology at all proves nothing, and `go test` would
	// otherwise print "ok". The embedded topology needs no server, no docker
	// and no dolt binary, so an empty run means something is broken rather
	// than gated — unless the operator narrowed the run themselves.
	if len(exercised) == 0 && !subtestFilterActive() {
		t.Fatal("no topology could be built; this run proves nothing. See the report above.")
	}
}

// subtestFilterActive reports whether -run narrows below the top-level test,
// in which case topologies are absent by operator request rather than by
// breakage.
func subtestFilterActive() bool {
	f := flag.Lookup("test.run")
	return f != nil && strings.Contains(f.Value.String(), "/")
}

func knownBadSuffix(want expectation) string {
	if want.knownBad == "" {
		return ""
	}
	return "\n  knownBad: " + want.knownBad
}

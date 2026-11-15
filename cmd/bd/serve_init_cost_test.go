package main

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Per-package budgets for TestServePackagesInitStaysUnderBudget, each taken
// as the cheapest of servePackageInitTraceRuns starts of bd. The allocation
// bound is the gate: under GOGC=off an init makes the same allocations on
// every start, on any host, race detector or not. Today the costliest package
// under servePackageTrees makes 18 allocations in init; the BDP server
// work queued for upstream (bd serve --protocol) raises internal/httpapi to
// about 160. Parsing the embedded OpenAPI document in an init makes about
// 84,000. An init that needs more than the budget should do its work on
// first use (sync.OnceValue) instead: every bd command pays it otherwise.
// The clock bound is internal/ui's initClockBudgetMS, generous on purpose
// because a loaded host stretches clock time and not allocation counts.
const (
	servePackageInitAllocBudget   = 512
	servePackageInitClockBudgetMS = 10.0
	servePackageInitTraceRuns     = 3
)

// serveInitModulePath is the module path the bounded packages live under.
var serveInitModulePath = strings.TrimSuffix(bdModulePackage, "/cmd/bd")

// servePackageTrees are the import paths whose package, and every package
// below it, TestServePackagesInitStaysUnderBudget bounds: the http backend,
// bd serve's server and bd connect's client. A package bd does not link
// (internal/httpapi/spec today), or whose init does no work
// (internal/httpapi/apigen today), prints no inittrace line and so is within
// budget.
var servePackageTrees = []string{
	serveInitModulePath + "/backend/http",
	serveInitModulePath + "/internal/httpapi",
	serveInitModulePath + "/internal/httpclient",
}

// serveInitTraceLine matches one GODEBUG=inittrace=1 line, as documented by
// the runtime package:
// "init <pkg> @<start> ms, <clock> ms clock, <bytes> bytes, <allocs> allocs".
var serveInitTraceLine = regexp.MustCompile(`^init (\S+) @[0-9.]+ ms, ([0-9.]+) ms clock, ([0-9]+) bytes, ([0-9]+) allocs`)

// serveInitCost is one package's init cost as one inittrace line reports it.
type serveInitCost struct {
	clockMS float64
	bytes   uint64
	allocs  uint64
}

// parseServeInitTrace returns the init cost of every package an inittrace
// transcript names. Other lines (bd's own output) are skipped.
func parseServeInitTrace(out string) (map[string]serveInitCost, error) {
	costs := map[string]serveInitCost{}
	for line := range strings.SplitSeq(out, "\n") {
		m := serveInitTraceLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		clock, err := strconv.ParseFloat(m[2], 64)
		if err != nil {
			return nil, fmt.Errorf("parse init clock in %q: %w", line, err)
		}
		bytes, err := strconv.ParseUint(m[3], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse init bytes in %q: %w", line, err)
		}
		allocs, err := strconv.ParseUint(m[4], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse init allocs in %q: %w", line, err)
		}
		costs[m[1]] = serveInitCost{clockMS: clock, bytes: bytes, allocs: allocs}
	}
	return costs, nil
}

// keepCheapestServeInit folds one start's costs into best, keeping each
// package's lowest clock, bytes and allocations across starts, so that a
// scheduling stall in one start cannot fail the test.
func keepCheapestServeInit(best, run map[string]serveInitCost) {
	for pkg, c := range run {
		if prev, ok := best[pkg]; ok {
			c = serveInitCost{
				clockMS: min(prev.clockMS, c.clockMS),
				bytes:   min(prev.bytes, c.bytes),
				allocs:  min(prev.allocs, c.allocs),
			}
		}
		best[pkg] = c
	}
}

// isServePackage reports whether pkg is one of servePackageTrees or below one.
func isServePackage(pkg string) bool {
	for _, root := range servePackageTrees {
		if pkg == root || strings.HasPrefix(pkg, root+"/") {
			return true
		}
	}
	return false
}

// serveInitOverrun is one budget one package exceeds.
type serveInitOverrun struct {
	pkg  string
	kind string // "allocs" or "clock"
	cost serveInitCost
}

func (o serveInitOverrun) String() string {
	if o.kind == "allocs" {
		return fmt.Sprintf("package %s makes %d allocations (%d bytes) in init (fewest of %d starts), over the %d-allocation budget; every bd command pays its init before main runs",
			o.pkg, o.cost.allocs, o.cost.bytes, servePackageInitTraceRuns, servePackageInitAllocBudget)
	}
	return fmt.Sprintf("package %s spends %.2f ms in init (fastest of %d starts), over the %.0f ms budget; every bd command pays it before main runs",
		o.pkg, o.cost.clockMS, servePackageInitTraceRuns, servePackageInitClockBudgetMS)
}

// servePackageInitOverruns returns the budgets the packages under
// servePackageTrees exceed, sorted by package then kind, and those packages,
// sorted, so that the caller can tell a clean measurement from an empty one.
func servePackageInitOverruns(best map[string]serveInitCost) (overruns []serveInitOverrun, traced []string) {
	for pkg, c := range best {
		if !isServePackage(pkg) {
			continue
		}
		traced = append(traced, pkg)
		if c.allocs > servePackageInitAllocBudget {
			overruns = append(overruns, serveInitOverrun{pkg: pkg, kind: "allocs", cost: c})
		}
		if c.clockMS > servePackageInitClockBudgetMS {
			overruns = append(overruns, serveInitOverrun{pkg: pkg, kind: "clock", cost: c})
		}
	}
	slices.Sort(traced)
	slices.SortFunc(overruns, func(a, b serveInitOverrun) int {
		return strings.Compare(a.pkg+" "+a.kind, b.pkg+" "+b.kind)
	})
	return overruns, traced
}

// appendServeInitSetting adds setting to an environment value that holds a
// list of settings joined by sep (GODEBUG uses commas, GORACE spaces),
// keeping the settings already there.
func appendServeInitSetting(value, sep, setting string) string {
	if value == "" {
		return setting
	}
	return value + sep + setting
}

// TestServePackagesInitStaysUnderBudget pins the startup cost of bd's HTTP
// stack (servePackageTrees). Every bd command pays the init of every package
// bd links before main runs, whatever the command does, and bd links bd
// serve's server and bd connect's client even when it uses neither. A change
// that does table-sized work in one of their inits (say, parsing the embedded
// OpenAPI document there instead of on first use) fails here, naming the
// package.
//
// It starts the bd under test (`bd version`) rather than re-executing this
// test binary as internal/ui's TestTerminalStackPackageInitStaysUnderBudget
// does: this package's TestMain claims a suite temp root, guards the checkout
// and, in the Dolt-server lane, starts a shared dolt sql-server, none of which
// a measurement start should do. It also measures the binary every bd
// command starts, not a test binary that links more.
func TestServePackagesInitStaysUnderBudget(t *testing.T) {
	bd := buildBDForInitTests(t)
	dir := t.TempDir()
	best := map[string]serveInitCost{}
	for range servePackageInitTraceRuns {
		cmd := exec.Command(bd, "version")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GODEBUG="+appendServeInitSetting(os.Getenv("GODEBUG"), ",", "inittrace=1"),
			// A collection that starts inside one package's init would be
			// billed to that package's clock.
			"GOGC=off",
			// A race-instrumented bd would otherwise sleep 1 s at exit.
			"GORACE="+appendServeInitSetting(os.Getenv("GORACE"), " ", "atexit_sleep_ms=0"))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s version with GODEBUG inittrace: %v\n%s", bd, err, out)
		}
		run, err := parseServeInitTrace(string(out))
		if err != nil {
			t.Fatal(err)
		}
		if len(run) == 0 {
			t.Fatalf("%s version printed no inittrace lines, so nothing was measured:\n%s", bd, out)
		}
		keepCheapestServeInit(best, run)
	}
	overruns, traced := servePackageInitOverruns(best)
	if len(traced) == 0 {
		t.Fatalf("no package under %v printed an inittrace line, so nothing was bounded; did the module path or bd's imports change?", servePackageTrees)
	}
	for _, pkg := range traced {
		c := best[pkg]
		t.Logf("%s: %d allocs, %d bytes, %.3f ms", pkg, c.allocs, c.bytes, c.clockMS)
	}
	for _, o := range overruns {
		t.Error(o)
	}
}

// TestServePackageInitBudgetRuleFires is the budget's own falsification:
// TestServePackagesInitStaysUnderBudget is permanently green against a bd
// that respects it, so a synthetic transcript proves the check still names a
// package over either bound, leaves packages outside servePackageTrees alone,
// treats an untraced package as within budget, and does not bill one start's
// stall to a package.
func TestServePackageInitBudgetRuleFires(t *testing.T) {
	mod := serveInitModulePath
	httpapi, httpclient, wire := mod+"/internal/httpapi", mod+"/internal/httpclient", mod+"/internal/httpclient/wire"
	trace := func(lines ...string) map[string]serveInitCost {
		t.Helper()
		costs, err := parseServeInitTrace(strings.Join(lines, "\n"))
		if err != nil {
			t.Fatal(err)
		}
		return costs
	}
	kinds := func(overruns []serveInitOverrun) []string {
		var got []string
		for _, o := range overruns {
			got = append(got, o.pkg+" "+o.kind)
		}
		return got
	}

	best := map[string]serveInitCost{}
	keepCheapestServeInit(best, trace(
		"bd version 1.3.4 (dev: 473aa09fa11e)",
		"init github.com/alecthomas/chroma/v2/styles @12 ms, 18 ms clock, 1675680 bytes, 35495 allocs",
		"init "+mod+"/backend/http @70 ms, 0 ms clock, 0 bytes, 0 allocs",
		"init "+wire+" @70 ms, 0.010 ms clock, 3888 bytes, 12 allocs",
		"init "+httpclient+" @70 ms, 0.020 ms clock, 2960 bytes, 512 allocs",
		"init "+httpapi+" @71 ms, 3.1 ms clock, 1048576 bytes, 513 allocs",
		"init "+httpapi+"x @71 ms, 50 ms clock, 64 bytes, 100000 allocs",
	))
	overruns, traced := servePackageInitOverruns(best)
	if want := []string{httpapi + " allocs"}; !slices.Equal(kinds(overruns), want) {
		t.Fatalf("overruns = %v, want %v (513 allocations is over the budget, 512 is not; chroma and %sx are outside the trees)", kinds(overruns), want, httpapi)
	}
	if want := []string{mod + "/backend/http", httpapi, httpclient, wire}; !slices.Equal(traced, want) {
		t.Fatalf("traced = %v, want %v (internal/httpapi/spec, untraced, is within budget)", traced, want)
	}
	if msg := overruns[0].String(); !strings.Contains(msg, httpapi+" makes 513 allocations") {
		t.Fatalf("overrun message %q does not name the package and its count", msg)
	}

	// A 40 ms stall in a later start is not billed: the fastest start counts.
	keepCheapestServeInit(best, trace("init "+wire+" @70 ms, 40 ms clock, 3888 bytes, 12 allocs"))
	if overruns, _ := servePackageInitOverruns(best); !slices.Equal(kinds(overruns), []string{httpapi + " allocs"}) {
		t.Fatalf("a stall in one start was billed: overruns = %v", kinds(overruns))
	}

	// Over the clock bound in every start fails on clock alone.
	slow := map[string]serveInitCost{}
	keepCheapestServeInit(slow, trace("init "+httpclient+" @70 ms, 12 ms clock, 2960 bytes, 18 allocs"))
	if overruns, _ := servePackageInitOverruns(slow); !slices.Equal(kinds(overruns), []string{httpclient + " clock"}) {
		t.Fatalf("overruns = %v, want [%s clock]", kinds(overruns), httpclient)
	}

	// A transcript naming no package under the trees bounds nothing, which
	// TestServePackagesInitStaysUnderBudget fails rather than passes.
	if _, traced := servePackageInitOverruns(trace("init github.com/alecthomas/chroma/v2/styles @12 ms, 8.3 ms clock, 1675680 bytes, 35495 allocs")); len(traced) != 0 {
		t.Fatalf("traced = %v, want none", traced)
	}

	// A malformed number is an error, not a silently skipped package.
	if _, err := parseServeInitTrace("init " + httpapi + " @1 ms, 1.2.3 ms clock, 1 bytes, 1 allocs"); err == nil {
		t.Fatal("a malformed clock parsed without error")
	}
}

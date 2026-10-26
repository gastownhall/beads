package ui

import (
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/testutil/bazeltest"
)

// initClockBudgetMS bounds the init of any one package this test binary links,
// taken as the fastest of initTraceRuns process starts so that a scheduling
// stall in one start cannot fail the test. The starts run with GOGC=off: a
// garbage collection that happens to start inside one package's init (on a
// host with many Ps its stop-the-world alone can take several ms) would
// otherwise be billed to that package. Every package here is in bd's terminal
// rendering stack, and every bd command pays its init before main runs,
// whatever the command does. Today each one takes well under 1 ms, about 1 ms
// under the race detector.
const (
	initClockBudgetMS = 10.0
	initTraceRuns     = 3
)

// initTraceLine matches one GODEBUG=inittrace=1 line, as documented by the
// runtime package: "init <pkg> @<start> ms, <clock> ms clock, <n> bytes, <n> allocs".
var initTraceLine = regexp.MustCompile(`^init (\S+) @[0-9.]+ ms, ([0-9.]+) ms clock`)

// TestTerminalStackPackageInitStaysUnderBudget pins the startup cost of the
// packages behind bd's terminal rendering (lipgloss, charmbracelet/x/ansi,
// go-runewidth and what they import). go-runewidth v0.0.26 and v0.0.27
// filled a 2 MB width table eagerly in init, which added about 35 ms to every
// bd command (mattn/go-runewidth#104, fixed in v0.0.28). A dependency bump that
// brings init work like that back fails here, naming the package.
func TestTerminalStackPackageInitStaysUnderBudget(t *testing.T) {
	best := map[string]float64{}
	for range initTraceRuns {
		cmd := exec.Command(os.Args[0], "-test.run=^$") //nolint:gosec // test binary re-exec
		cmd.Env = append(bazeltest.ShardFreeEnv(os.Environ()),
			"GODEBUG="+appendSetting(os.Getenv("GODEBUG"), ",", "inittrace=1"),
			"GOGC=off",
			// A -race build otherwise sleeps 1 s at exit; the child runs no test.
			"GORACE="+appendSetting(os.Getenv("GORACE"), " ", "atexit_sleep_ms=0"))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("re-exec %s with GODEBUG inittrace: %v\n%s", os.Args[0], err, out)
		}
		traced := 0
		for line := range strings.SplitSeq(string(out), "\n") {
			m := initTraceLine.FindStringSubmatch(strings.TrimSpace(line))
			if m == nil {
				continue
			}
			traced++
			ms, err := strconv.ParseFloat(m[2], 64)
			if err != nil {
				t.Fatalf("parse init clock in %q: %v", line, err)
			}
			if prev, ok := best[m[1]]; !ok || ms < prev {
				best[m[1]] = ms
			}
		}
		if traced == 0 {
			t.Fatalf("re-exec printed no inittrace lines, so nothing was measured:\n%s", out)
		}
	}
	for pkg, ms := range best {
		if ms > initClockBudgetMS {
			t.Errorf("package %s spends %.2f ms in init (fastest of %d starts), over the %.0f ms budget; every bd command pays it before main runs",
				pkg, ms, initTraceRuns, initClockBudgetMS)
		}
	}
}

// appendSetting adds setting to an environment value that holds a list of
// settings joined by sep (GODEBUG uses commas, GORACE spaces), keeping the
// settings already there.
func appendSetting(value, sep, setting string) string {
	if value == "" {
		return setting
	}
	return value + sep + setting
}

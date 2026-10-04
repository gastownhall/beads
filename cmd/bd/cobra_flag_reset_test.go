package main

import (
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// In-process runners (runBDInProcess, runBDInProcessAllowError) execute the
// package-level rootCmd tree again and again in one process. cobra never
// resets a flag between Execute calls: a value parsed by one invocation
// (create's --title, --labels, list's --status, ...) and its Changed bit
// stay on the FlagSet, so the next invocation that does not pass the flag
// still sees it, and a slice flag that was set appends to the stale value
// instead of replacing it. resetCommandFlags puts every flag of the tree
// back to the state it had before any test ran.
//
// The baseline is captured in TestMain (snapshotCommandFlags), after every
// init() registered its flags and before any test executed a command, so it
// is the registered default even for custom flag.Value types whose DefValue
// string cannot be parsed back (closeReasonFlagValue, onceStringFlag, ...).

type flagBaseline struct {
	// value is a shallow copy of *flag.Value's pointee. For pflag's scalar
	// types (type boolValue bool, ...) the pointee IS the bound variable, so
	// copying it back restores the variable; for the slice types it restores
	// the unexported "changed" bit that makes the next Set replace instead
	// of append; for this package's custom values it restores their fields.
	value reflect.Value
	// slice holds a pflag.SliceValue's elements: the struct copy above only
	// restores the pointer to the bound []string, not its contents.
	slice   []string
	isSlice bool
}

var (
	flagBaselineMu sync.Mutex
	flagBaselines  map[*pflag.Flag]flagBaseline
)

// visitCommandFlags calls fn once for every flag defined anywhere in cmd's
// tree (local and persistent flag sets; a flag shared by both is visited
// once).
func visitCommandFlags(cmd *cobra.Command, fn func(*pflag.Flag)) {
	seen := map[*pflag.Flag]bool{}
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		visit := func(f *pflag.Flag) {
			if !seen[f] {
				seen[f] = true
				fn(f)
			}
		}
		c.PersistentFlags().VisitAll(visit)
		c.Flags().VisitAll(visit)
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(cmd)
}

// snapshotCommandFlags records the current state of every flag in cmd's
// tree as the baseline resetCommandFlags restores. Called once from
// TestMain; a second call replaces the baseline.
func snapshotCommandFlags(cmd *cobra.Command) {
	baselines := map[*pflag.Flag]flagBaseline{}
	visitCommandFlags(cmd, func(f *pflag.Flag) {
		b := flagBaseline{}
		if rv := reflect.ValueOf(f.Value); rv.Kind() == reflect.Pointer && !rv.IsNil() {
			b.value = reflect.New(rv.Elem().Type()).Elem()
			b.value.Set(rv.Elem())
		}
		if sv, ok := f.Value.(pflag.SliceValue); ok {
			b.isSlice = true
			b.slice = append([]string(nil), sv.GetSlice()...)
		}
		baselines[f] = b
	})
	flagBaselineMu.Lock()
	flagBaselines = baselines
	flagBaselineMu.Unlock()
}

// resetCommandFlags returns every flag in cmd's tree to its baseline value
// with Changed=false. Flags bound to package globals (dbPath, actor,
// jsonOutput, readonlyMode, ...) are reset with them, so the runners
// overwrite any value a test assigned to such a global directly, before and
// after each run. Flags registered after the snapshot (cobra adds
// --help lazily) go back to their DefValue.
func resetCommandFlags(cmd *cobra.Command) {
	flagBaselineMu.Lock()
	baselines := flagBaselines
	flagBaselineMu.Unlock()
	visitCommandFlags(cmd, func(f *pflag.Flag) {
		defer func() { f.Changed = false }()
		b, ok := baselines[f]
		if !ok {
			if _, isSlice := f.Value.(pflag.SliceValue); !isSlice {
				_ = f.Value.Set(f.DefValue)
			}
			return
		}
		if b.value.IsValid() {
			reflect.ValueOf(f.Value).Elem().Set(b.value)
		}
		if b.isSlice {
			_ = f.Value.(pflag.SliceValue).Replace(append([]string(nil), b.slice...))
		}
	})
}

// Every in-process execution of the bd command tree (the runners above, and
// the many tests that call rootCmd.Execute directly) mutates process state
// that nothing restores: PersistentPreRun exports the selected workspace as
// BEADS_DIR and loads its .beads/.env, and records the workspace's storage
// mode in serverMode/proxiedServerMode. Left behind, a since-deleted
// fixture's BEADS_DIR sends a later test's workspace discovery to the wrong
// place ("no active beads workspace found", "migration sidecar is
// missing"), and a leaked proxiedServerMode routes later RunE-level tests
// down the proxied path ("proxied-server UOW provider not initialized", a
// nil-UOW panic in reopenProxiedResolve). installExecuteIsolation (called
// once from TestMain) registers a cobra initializer/finalizer pair that
// snapshots that state when an execution starts and puts it back when it
// ends, whatever the outcome. Executions can overlap (parallel tests that
// build their own commands): the outermost snapshot wins and is restored
// when the last one finishes.
var (
	executeIsolationMu    sync.Mutex
	executeIsolationDepth int
	executeIsolationEnv   []string
	executeIsolationModes [2]bool
	executeIsolationOnce  sync.Once
)

func installExecuteIsolation() {
	executeIsolationOnce.Do(func() {
		cobra.OnInitialize(func() {
			executeIsolationMu.Lock()
			defer executeIsolationMu.Unlock()
			if executeIsolationDepth == 0 {
				executeIsolationEnv = os.Environ()
				executeIsolationModes = [2]bool{serverMode, proxiedServerMode}
			}
			executeIsolationDepth++
		})
		cobra.OnFinalize(func() {
			executeIsolationMu.Lock()
			defer executeIsolationMu.Unlock()
			if executeIsolationDepth == 0 {
				return
			}
			executeIsolationDepth--
			if executeIsolationDepth == 0 {
				restoreProcessEnv(executeIsolationEnv)
				serverMode, proxiedServerMode = executeIsolationModes[0], executeIsolationModes[1]
			}
		})
	})
}

// restoreProcessEnv puts the process environment back to before, a prior
// os.Environ(), touching only the keys that differ.
func restoreProcessEnv(before []string) {
	want := make(map[string]string, len(before))
	for _, kv := range before {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			want[k] = v
		}
	}
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			continue
		}
		if old, had := want[k]; !had {
			_ = os.Unsetenv(k)
		} else if old != v {
			_ = os.Setenv(k, old)
		}
	}
	for k, v := range want {
		if _, ok := os.LookupEnv(k); !ok {
			_ = os.Setenv(k, v)
		}
	}
}

// TestResetCommandFlagsRestoresEveryFlag sets every flag of the real bd
// command tree to a non-default value, resets, and checks each flag is back
// to its baseline: same String(), Changed=false, and (for slice flags) the
// next Set replaces rather than appends to the old value.
func TestResetCommandFlagsRestoresEveryFlag(t *testing.T) {
	// Not parallel: no other top-level test runs while this one mutates the
	// shared command tree.
	t.Cleanup(func() { resetCommandFlags(rootCmd) })

	type state struct {
		value   string
		changed bool
	}
	before := map[*pflag.Flag]state{}
	visitCommandFlags(rootCmd, func(f *pflag.Flag) {
		before[f] = state{f.Value.String(), false}
	})
	if len(before) < 100 {
		t.Fatalf("visited only %d flags; the walk does not reach the command tree", len(before))
	}

	sample := map[string]string{
		"bool": "true", "int": "7", "int64": "7", "uint": "7", "float64": "1.5",
		"duration": "3s", "string": "carried-over", "stringSlice": "carried-over",
		"stringArray": "carried-over", "intSlice": "7",
	}
	visitCommandFlags(rootCmd, func(f *pflag.Flag) {
		// resetCommandFlags restores a Value through its pointee; a
		// non-pointer Value (or a map type, whose Set re-points the bound
		// map) would need its own handling.
		if reflect.ValueOf(f.Value).Kind() != reflect.Pointer {
			t.Errorf("flag --%s: Value %T is not a pointer; resetCommandFlags cannot restore it", f.Name, f.Value)
		}
		v, ok := sample[f.Value.Type()]
		if !ok {
			t.Errorf("flag --%s: no sample value for type %q; add one so its reset is tested", f.Name, f.Value.Type())
			return
		}
		if err := f.Value.Set(v); err != nil {
			t.Errorf("flag --%s (%s): Set(%q): %v; pick a sample it accepts so its reset is tested", f.Name, f.Value.Type(), v, err)
			return
		}
		f.Changed = true
	})

	resetCommandFlags(rootCmd)

	visitCommandFlags(rootCmd, func(f *pflag.Flag) {
		want, ok := before[f]
		if !ok {
			return
		}
		if got := f.Value.String(); got != want.value {
			t.Errorf("flag --%s (%s): value after reset = %q, want %q", f.Name, f.Value.Type(), got, want.value)
		}
		if f.Changed {
			t.Errorf("flag --%s: Changed still true after reset", f.Name)
		}
	})

	// A reset slice flag must behave like a fresh one: the first Set
	// replaces the default instead of appending to it.
	labels := createCmd.Flags().Lookup("labels")
	if labels == nil {
		t.Fatal("create --labels not registered")
	}
	if err := labels.Value.Set("a"); err != nil {
		t.Fatal(err)
	}
	resetCommandFlags(rootCmd)
	if err := labels.Value.Set("b"); err != nil {
		t.Fatal(err)
	}
	if got := labels.Value.String(); got != "[b]" {
		t.Errorf("create --labels after reset+Set(b) = %q, want [b]", got)
	}
}

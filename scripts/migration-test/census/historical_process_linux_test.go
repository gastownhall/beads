//go:build linux

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHistoricalCommandContainsDoubleForkedSetsidDescendant(t *testing.T) {
	binary := writeHistoricalTestBinary(t, `#!/bin/sh
(
  setsid sh -c 'printf "%s" "$$" > "$2"; : > "$3"; sleep 0.4; printf escaped > "$1"' escaped-child "$1" "$2" "$3" &
) &
while [ ! -f "$3" ]; do sleep 0.01; done
exit 17
`)
	target := filepath.Join(t.TempDir(), "escaped-mutation")
	pidFile := target + ".pid"
	readyFile := target + ".ready"
	ctx := withHistoricalBinaryBinding(context.Background(), bindHistoricalTestBinary(t, binary))

	_, err := runHistoricalCommand(ctx, binary, target, pidFile, readyFile)
	if err == nil {
		t.Fatal("double-forking historical command succeeded, want containment failure")
	}
	if isHistoricalProcessExit(err) {
		t.Fatalf("containment failure unwraps as historical process exit: %v", err)
	}
	var containment *historicalProcessContainmentError
	if !errors.As(err, &containment) {
		t.Fatalf("error = %T %v, want historical process containment error", err, err)
	}
	if !containment.unexpectedDescendant {
		t.Fatalf("containment = %#v, want an unexpected descendant", containment)
	}
	pidRaw, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatalf("read escaped child PID: %v", readErr)
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(pidRaw)))
	if parseErr != nil {
		t.Fatalf("parse escaped child PID %q: %v", pidRaw, parseErr)
	}
	if _, statErr := os.Stat(filepath.Join("/proc", strconv.Itoa(pid))); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("escaped child PID %d still exists after containment: %v", pid, statErr)
	}

	// The fixture waits long enough that an escaped child would mutate this path
	// after its direct parent had exited if containment only took one snapshot.
	time.Sleep(700 * time.Millisecond)
	if _, statErr := os.Lstat(target); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("double-forked descendant mutated %q after containment: %v", target, statErr)
	}
}

func TestHistoricalCommandRehashesItsBoundExecutableBeforeEveryInvocation(t *testing.T) {
	binary := writeHistoricalTestBinary(t, "#!/bin/sh\nprintf first > \"$1\"\n")
	target := filepath.Join(t.TempDir(), "marker")
	ctx := withHistoricalBinaryBinding(context.Background(), bindHistoricalTestBinary(t, binary))

	if _, err := runHistoricalCommand(ctx, binary, target); err != nil {
		t.Fatalf("first historical command: %v", err)
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf replaced > \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := runHistoricalCommand(ctx, binary, target); err == nil {
		t.Fatal("replaced historical executable was accepted on its second invocation")
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "first" {
		t.Fatalf("marker after rejected replacement = %q, error = %v; want first", got, err)
	}
}

func writeHistoricalTestBinary(t *testing.T, contents string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "historical-bd")
	if err := os.WriteFile(binary, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	return binary
}

func bindHistoricalTestBinary(t *testing.T, binary string) freshBinary {
	t.Helper()
	binding, err := bindFreshBinary(binary, sourceBuildAcquisitionForTest(testCatalog().Versions[0]))
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

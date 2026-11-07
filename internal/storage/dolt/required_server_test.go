package dolt

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// A real testing.T in a child process distinguishes Fatal from Skip without
// turning the parent suite red. The helper needs no server or database.
func TestSharedServerGate(t *testing.T) {
	for _, tc := range []struct {
		name, optIn string
		available   bool
		wantFailure bool
	}{
		{name: "optional unavailable"},
		{name: "non-opt-in unavailable", optIn: "true"},
		{name: "required unavailable", optIn: "1", wantFailure: true},
		{name: "optional available", available: true},
		{name: "required available", optIn: "1", available: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperSharedServerGate$", "-test.v")
			cmd.Env = append(os.Environ(), "BEADS_REQUIRED_SERVER_HELPER=1",
				"BEADS_TEST_ENV_RUN_DOLT="+tc.optIn,
				fmt.Sprintf("BEADS_GATE_SERVER_AVAILABLE=%t", tc.available))
			output, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("server gate blocked (possibly before refusing a missing server): %v\n%s", ctx.Err(), output)
			}
			if (err != nil) != tc.wantFailure {
				t.Fatalf("helper error = %v, want failure %t\n%s", err, tc.wantFailure, output)
			}
			text := string(output)
			if tc.wantFailure && !strings.Contains(text, "BEADS_TEST_ENV_RUN_DOLT=1 requires a shared Dolt test server") {
				t.Fatalf("missing actionable required-server diagnostic:\n%s", output)
			}
			if skipped := strings.Contains(text, "--- SKIP: TestHelperSharedServerGate/gate"); skipped != (!tc.available && !tc.wantFailure) {
				t.Fatalf("unexpected skip disposition:\n%s", output)
			}
			if ran := strings.Contains(text, "server gate admitted test"); ran != tc.available {
				t.Fatalf("unexpected test admission:\n%s", output)
			}
			if !strings.Contains(text, "server gate slots restored") {
				t.Fatalf("helper did not verify slot ownership after the test:\n%s", output)
			}
		})
	}
}

func TestHelperSharedServerGate(t *testing.T) {
	if os.Getenv("BEADS_REQUIRED_SERVER_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	testServerPort = 0
	testSem = make(chan struct{}, 1)
	available := os.Getenv("BEADS_GATE_SERVER_AVAILABLE") == "true"
	if available {
		// Only admission is under test; the gate does not connect to this port.
		testServerPort = 1
	} else {
		// Missing infrastructure must be refused before waiting for a slot.
		testSem <- struct{}{}
	}
	t.Run("gate", func(t *testing.T) {
		skipIfNoServer(t)
		if len(testSem) != 1 {
			t.Fatal("admitted test does not own a slot")
		}
		fmt.Println("server gate admitted test")
	})
	wantSlots := 1
	if available {
		wantSlots = 0
	}
	if len(testSem) != wantSlots {
		t.Fatalf("slot count after test = %d, want %d", len(testSem), wantSlots)
	}
	fmt.Println("server gate slots restored")
}

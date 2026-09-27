//go:build cgo

package main

// Cross-mode refusal-class table for the `--if-updated-at` fence — the three
// cases the T20 parity oracle (TestProxiedServerIfUpdatedAtParity in
// if_updated_at_conformance_test.go) does not carry: REPEAT APPLICATION,
// EMPTY GUARD and INVALID STAMP, run against BOTH verbs on BOTH transports
// (classic embedded vs proxied-server), asserting the same exit class and
// the same write-visibility everywhere.
//
// Rationale per the conformance lane (agent-forge-y30h.3.30.2): a transport
// that silently drops the fence parameter turns every refusal into a match,
// so the refusal-class cases must hold per transport, per verb — not only on
// the embedded path where the storage-level WHERE clause is unit-tested.
// Like the rest of the family, everything execs the built binary by argv:
// the file compiles before the flag exists and fails on
// "unknown flag: --if-updated-at" until both transports accept it.

import (
	"reflect"
	"testing"
	"time"
)

// ifupTableNextSecond sleeps across the next whole-second boundary. bd
// truncates updated_at to whole seconds, so the repeat-application case is
// only deterministic when the authorized write landed in a LATER second than
// the stamp it was authorized with — otherwise the "spent" stamp still
// matches and the fence legitimately fires again.
func ifupTableNextSecond(t *testing.T) {
	t.Helper()
	until := time.Now().Truncate(time.Second).Add(1100 * time.Millisecond)
	time.Sleep(time.Until(until))
}

// ifupTableOutcome records one refusal-class run: the exit code and whether
// the full row survived the command untouched.
type ifupTableOutcome struct {
	code int
	same bool
}

// TestProxiedServerIfUpdatedAtRefusalClassParity — the cross-mode half of the
// refusal-class table:
//
//	update:   match→repeat (exit 13, one-shot per generation / T3),
//	          empty stamp (exit 1 usage error / D3), invalid stamp (1 / D2)
//	unclaim:  fenced release→repeat (exit 13 / D1+A1: the new stamp guard
//	          adopts ExitGuardMismatch on unclaim too, unlike the legacy
//	          --if-assignee exit-1 wording), empty stamp (1 / D3),
//	          invalid stamp (1 / D2)
//
// Exit classes and write-visibility must be identical on the classic and
// proxied transports; every refusing step is also checked absolutely, since
// parity with a shared bug is still a bug.
func TestProxiedServerIfUpdatedAtRefusalClassParity(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	envs := newCrossModeEnvs(t, bd, "yzt", "yzu")
	outcomes := make(map[string][6]ifupTableOutcome, len(envs))

	for _, env := range envs {
		var table [6]ifupTableOutcome

		// ---- update: match, then repeat the same fenced write ----
		id := env.create(t, "Refusal class update")
		stamp := ifupEnvStamp(t, env, id)
		ifupTableNextSecond(t)
		env.mustRun(t, "update", id, "--priority", "1", "--if-updated-at", stamp)
		afterMatch := env.show(t, id)

		_, _, code := env.run(t, "update", id, "--priority", "2", "--if-updated-at", stamp)
		table[0] = ifupTableOutcome{code: code, same: reflect.DeepEqual(afterMatch, env.show(t, id))}

		// update: empty stamp must never downgrade to an unconditional write.
		_, _, code = env.run(t, "update", id, "--priority", "2", "--if-updated-at", "")
		table[1] = ifupTableOutcome{code: code, same: reflect.DeepEqual(afterMatch, env.show(t, id))}

		// update: unparseable stamp must fail validation before the store.
		_, _, code = env.run(t, "update", id, "--priority", "2", "--if-updated-at", "not-a-timestamp")
		table[2] = ifupTableOutcome{code: code, same: reflect.DeepEqual(afterMatch, env.show(t, id))}

		// ---- unclaim: supervisor release, then repeat the same fence ----
		held := env.create(t, "Refusal class release")
		env.mustRun(t, "update", held, "--assignee", "worker-a", "--status", "in_progress")
		releaseStamp := ifupEnvStamp(t, env, held)
		ifupTableNextSecond(t)
		env.mustRun(t, "unclaim", held, "--actor", "supervisor-x",
			"--if-assignee", "worker-a", "--if-updated-at", releaseStamp)
		afterRelease := env.show(t, held)

		_, _, code = env.run(t, "unclaim", held, "--actor", "supervisor-x",
			"--if-assignee", "worker-a", "--if-updated-at", releaseStamp)
		table[3] = ifupTableOutcome{code: code, same: reflect.DeepEqual(afterRelease, env.show(t, held))}

		// unclaim: empty stamp, claim state untouched.
		_, _, code = env.run(t, "unclaim", held, "--if-updated-at", "")
		table[4] = ifupTableOutcome{code: code, same: reflect.DeepEqual(afterRelease, env.show(t, held))}

		// unclaim: unparseable stamp, claim state untouched.
		_, _, code = env.run(t, "unclaim", held, "--if-updated-at", "not-a-timestamp")
		table[5] = ifupTableOutcome{code: code, same: reflect.DeepEqual(afterRelease, env.show(t, held))}

		outcomes[env.mode] = table

		// Per-mode absolute expectations.
		names := []string{
			"update repeat application",
			"update empty stamp",
			"update invalid stamp",
			"unclaim repeat release",
			"unclaim empty stamp",
			"unclaim invalid stamp",
		}
		for i, name := range names {
			got := table[i]
			want := 1
			if i%3 == 0 {
				want = ExitGuardMismatch
			}
			if got.code != want {
				t.Errorf("[%s] %s exit = %d, want %d", env.mode, name, got.code, want)
			}
			if !got.same {
				t.Errorf("[%s] %s mutated the row", env.mode, name)
			}
		}
	}

	// Cross-mode parity, field by field.
	classic, proxied := outcomes["classic"], outcomes["proxied"]
	names := []string{
		"update repeat exit", "update empty exit", "update invalid exit",
		"unclaim repeat exit", "unclaim empty exit", "unclaim invalid exit",
		"update repeat row", "update empty row", "update invalid row",
		"unclaim repeat row", "unclaim empty row", "unclaim invalid row",
	}
	for i, name := range names {
		var c, p any
		if i < 6 {
			c, p = classic[i].code, proxied[i].code
		} else {
			c, p = classic[i-6].same, proxied[i-6].same
		}
		if c != p {
			t.Errorf("cross-mode divergence on %s: classic=%v proxied=%v", name, c, p)
		}
	}
}

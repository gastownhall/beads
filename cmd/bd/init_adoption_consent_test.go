package main

// Tests for the #6099 inbound adoption-consent machinery: the interactive
// prompt's answer table and disclosure, and the consent arm of
// handleRemoteSafetyDecision — including the prompt-once latch (an early
// consent is reused at the late checkpoint, never re-asked) and the
// fail-closed shape of every decline.

import (
	"bufio"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// captureStderrForConsentTest runs fn with os.Stderr replaced by a pipe and
// returns what it wrote. Serialized under stdioMutex like every other
// os.Stderr reassignment in this package.
func captureStderrForConsentTest(t *testing.T, fn func()) string {
	t.Helper()
	stdioMutex.Lock()
	defer stdioMutex.Unlock()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	fn()
	os.Stderr = old
	_ = w.Close()
	buf := make([]byte, 1<<16)
	n, _ := r.Read(buf)
	_ = r.Close()
	return string(buf[:n])
}

// TestPromptInitRemoteAdoptionAnswerTable pins the prompt's consent
// semantics: only an explicit affirmative grants consent. Everything else —
// empty input, a bare newline, "n", an unrelated word — is a decline, and
// EOF/cancellation come back as errors so the caller can refuse rather than
// guess (the handler's read-error arm covers EOF).
func TestPromptInitRemoteAdoptionAnswerTable(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		wantGrant bool
		wantErr   bool
	}{
		{"y grants", "y\n", true, false},
		{"yes grants", "yes\n", true, false},
		{"uppercase YES grants", "YES\n", true, false},
		{"padded y grants", "  y  \n", true, false},
		{"n declines", "n\n", false, false},
		{"no declines", "no\n", false, false},
		{"empty input declines", "\n", false, false},
		{"unrelated word declines", "maybe\n", false, false},
		{"EOF declines with an error", "", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			granted, err := promptInitRemoteAdoption(context.Background(), "file:///tmp/upstream.git", bufio.NewReader(strings.NewReader(tc.input)))
			if granted != tc.wantGrant {
				t.Errorf("granted = %v, want %v", granted, tc.wantGrant)
			}
			if tc.wantErr && err == nil {
				t.Errorf("err = nil, want an error (EOF must not be read as consent)")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("err = %v, want nil", err)
			}
		})
	}
}

// TestPromptInitRemoteAdoptionDisclosesURL pins the disclosure half of the
// prompt: it names the URL it would adopt (the reporter's complaint was not
// knowing WHAT was being adopted), says what adoption does, and defaults to
// NO.
func TestPromptInitRemoteAdoptionDisclosesURL(t *testing.T) {
	const remoteURL = "file:///tmp/upstream.git"
	var out string
	granted, err := func() (bool, error) {
		var g bool
		var e error
		out = captureStderrForConsentTest(t, func() {
			g, e = promptInitRemoteAdoption(context.Background(), remoteURL, bufio.NewReader(strings.NewReader("n\n")))
		})
		return g, e
	}()
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if granted {
		t.Error("granted = true, want false for a decline")
	}
	for _, want := range []string{remoteURL, "Adopt this origin's issue history?", "[y/N]", "issue history"} {
		if !strings.Contains(out, want) {
			t.Errorf("prompt missing %q:\n%s", want, out)
		}
	}
}

// TestHandleRemoteSafetyDecision_AdoptionConsentArm pins the caller half of
// the #6099 consent decision: every arm of the consent gate at the
// handleRemoteSafetyDecision seam, via the promptInitRemoteAdoptionFn seam.
// The matrix in init_safety_test.go proves CheckRemoteSafety's decision;
// this proves the handler honors it — fail closed on decline and on any
// read error, exit 130 on cancellation, reuse an existing consent without
// re-asking (the prompt-once latch), and never prompt at the late
// checkpoint or from a non-interactive run.
func TestHandleRemoteSafetyDecision_AdoptionConsentArm(t *testing.T) {
	const syncURL = "file:///tmp/upstream.git"
	decision := RemoteSafetyDecision{Action: ActionRequireAdoptionConsent, Reason: "adoption-consent-required", ExitCode: 1}

	cases := []struct {
		name        string
		consent     *adoptionConsentState
		allowPrompt bool
		promptGrant bool
		promptErr   error
		wantPrompt  int  // how many times the prompt seam must fire
		wantBoot    bool // whether the handler says "bootstrap"
		wantExit    int  // 0 = expect nil error
	}{
		{
			// The early checkpoint, interactive, no consent yet: ask once.
			// Grant → adopt; the consent latches to the URL for the later
			// checkpoint.
			"interactive early prompt grants",
			&adoptionConsentState{interactive: true}, true, true, nil,
			1, true, 0,
		},
		{
			// Decline at the prompt → the generic refusal, exit 1.
			"interactive early prompt declines",
			&adoptionConsentState{interactive: true}, true, false, nil,
			1, false, 1,
		},
		{
			// A prompt read error is not consent: refuse, never guess.
			"prompt read error refuses",
			&adoptionConsentState{interactive: true}, true, false, errors.New("read: resource temporarily unavailable"),
			1, false, 1,
		},
		{
			// Ctrl-C at the consent prompt keeps the standard 130.
			"prompt cancellation exits 130",
			&adoptionConsentState{interactive: true}, true, false, context.Canceled,
			1, false, 130,
		},
		{
			// --yes: consent ahead of time, no prompt needed anywhere.
			"assume-yes skips the prompt",
			&adoptionConsentState{assumeYes: true, interactive: true}, true, false, nil,
			0, true, 0,
		},
		{
			// The prompt-once latch: consent already granted for THIS URL
			// (early checkpoint), the same checkpoint's decision fires again
			// (late bootstrap probe) → reuse, never re-ask.
			"consented URL is reused without re-asking",
			&adoptionConsentState{interactive: true, consentedURL: syncURL}, true, false, nil,
			0, true, 0,
		},
		{
			// The late checkpoint never prompts — a mid-run prompt after
			// workspace writes would be a new silent-adoption vector.
			"late checkpoint with no consent refuses without prompting",
			&adoptionConsentState{interactive: true}, false, true, nil,
			0, false, 1,
		},
		{
			// Late checkpoint, consented for a DIFFERENT URL: consent binds
			// to the URL it named; a changed origin is re-refused (#6099
			// item 6).
			"consent for another URL is not reused",
			&adoptionConsentState{interactive: true, consentedURL: "file:///tmp/other.git"}, false, true, nil,
			0, false, 1,
		},
		{
			// --non-interactive is not consent: no prompt, straight refusal.
			"non-interactive run never prompts",
			&adoptionConsentState{interactive: false}, true, true, nil,
			0, false, 1,
		},
		{
			// The late checkpoint accepts a consent granted for its own URL
			// earlier in the run (the early prompt's grant arriving at the
			// bootstrap leg).
			"late checkpoint reuses matching consent",
			&adoptionConsentState{interactive: true, consentedURL: syncURL}, false, true, nil,
			0, true, 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			promptCalls := 0
			orig := promptInitRemoteAdoptionFn
			promptInitRemoteAdoptionFn = func(ctx context.Context, remoteURL string, reader *bufio.Reader) (bool, error) {
				promptCalls++
				if tc.promptErr != nil {
					return false, tc.promptErr
				}
				return tc.promptGrant, nil
			}
			defer func() { promptInitRemoteAdoptionFn = orig }()

			confirmed := false
			var stderrOut string
			var bootstrap bool
			var err error
			stderrOut = captureStderrForConsentTest(t, func() {
				bootstrap, err = handleRemoteSafetyDecision(decision, "bd", syncURL, "", nil, true, &confirmed, tc.consent, tc.allowPrompt)
			})

			if promptCalls != tc.wantPrompt {
				t.Errorf("prompt fired %d times, want %d", promptCalls, tc.wantPrompt)
			}
			if bootstrap != tc.wantBoot {
				t.Errorf("bootstrap = %v, want %v", bootstrap, tc.wantBoot)
			}
			if tc.wantExit == 0 {
				if err != nil {
					t.Errorf("err = %v, want nil", err)
				}
			} else {
				code, ok := exitCodeFromError(err)
				if !ok {
					t.Fatalf("err = %v, want an *exitError with code %d", err, tc.wantExit)
				}
				if code != tc.wantExit {
					t.Errorf("exit code = %d, want %d", code, tc.wantExit)
				}
			}
			if confirmed {
				t.Error("confirmed latch was set; the consent arm must not touch the divergence latch")
			}
			// Every refusal must name the URL and the remedies; the late
			// variant must not claim nothing was written.
			if tc.wantExit == 1 {
				for _, want := range []string{syncURL, "bd init --yes", "--no-adopt", "BD_NO_REMOTE_ADOPT"} {
					if !strings.Contains(stderrOut, want) {
						t.Errorf("refusal missing %q:\n%s", want, stderrOut)
					}
				}
				late := !tc.allowPrompt
				if strings.Contains(stderrOut, "after workspace scaffolding") != late {
					t.Errorf("late-variant note present=%v, want %v:\n%s", strings.Contains(stderrOut, "after workspace scaffolding"), late, stderrOut)
				}
			}
			// A granted consent must latch to the URL for later checkpoints.
			if tc.wantBoot && tc.wantPrompt == 1 && tc.promptGrant && tc.promptErr == nil {
				if tc.consent.consentedURL != syncURL {
					t.Errorf("consent latched to %q, want %q", tc.consent.consentedURL, syncURL)
				}
			}
		})
	}
}

// TestInitAdoptionConsentSatisfied pins the consent predicate itself: --yes
// authorizes any derived origin; a prompted consent authorizes exactly the
// URL it named and nothing else.
func TestInitAdoptionConsentSatisfied(t *testing.T) {
	const url = "file:///tmp/upstream.git"
	if !initAdoptionConsentSatisfied(true, "", url) {
		t.Error("--yes should authorize adoption with no prompted consent")
	}
	if !initAdoptionConsentSatisfied(false, url, url) {
		t.Error("a consent granted for this URL should authorize it")
	}
	if initAdoptionConsentSatisfied(false, "file:///tmp/other.git", url) {
		t.Error("consent granted for another URL must not transfer")
	}
	if initAdoptionConsentSatisfied(false, "", url) {
		t.Error("no consent anywhere must not authorize adoption")
	}
}

// TestInitAdoptionConsentAcrossBothCheckpoints pins the #6099 consent gate as
// the two checkpoints of a single `bd init` actually compose it: the early
// checkpoint may prompt, the late one may not, and the only things that carry
// from one to the other are the consent state's assumeYes flag and the
// URL-bound consent granted at the early prompt. Simulates both checkpoints
// exactly as init.go invokes CheckRemoteSafety and handleRemoteSafetyDecision
// in sequence. Every refusal here is the LATE shape: it must not prompt and
// must carry the after-scaffolding note, and a bootstrap answer means no
// clone was attempted (the handler returns bootstrap=false on refusal).
func TestInitAdoptionConsentAcrossBothCheckpoints(t *testing.T) {
	const (
		earlyURL = "file:///tmp/upstream.git"
		lateURL  = "file:///tmp/upstream.git" // same origin, same URL
		otherURL = "file:///tmp/unrelated.git"
	)

	runCheckpoints := func(t *testing.T, consent *adoptionConsentState, earlyData, lateData, lateUnknown bool, lateSyncURL string, promptGrant bool) (earlyBoot, lateBoot bool, lateErr error, promptCalls int) {
		t.Helper()
		// Early checkpoint: derived remote, data found, no override flags —
		// the consent gate's own decision.
		earlyDecision := CheckRemoteSafety(RemoteSafetyInput{
			RemoteHasDoltData:          earlyData,
			RemoteDerivedFromGitOrigin: true,
			AssumeYes:                  initAdoptionConsentSatisfied(consent.assumeYes, consent.consentedURL, earlyURL),
			IsInteractive:              consent.interactive,
		})
		promptCalls = 0
		orig := promptInitRemoteAdoptionFn
		promptInitRemoteAdoptionFn = func(ctx context.Context, remoteURL string, reader *bufio.Reader) (bool, error) {
			promptCalls++
			return promptGrant, nil
		}
		defer func() { promptInitRemoteAdoptionFn = orig }()
		earlyBoot, err := handleRemoteSafetyDecision(earlyDecision, "bd", earlyURL, "", nil, true, new(bool), consent, true)
		if err != nil {
			t.Fatalf("early checkpoint errored: %v", err)
		}

		// Late checkpoint: the same run, after workspace scaffolding. It may
		// never prompt (allowConsentPrompt=false), and it recomputes consent
		// from the shared state — an early grant binds to the URL it named.
		lateDecision := CheckRemoteSafety(RemoteSafetyInput{
			RemoteHasDoltData:          lateData,
			RemoteDerivedFromGitOrigin: true,
			RemoteProbeUnknown:         lateUnknown,
			AssumeYes:                  initAdoptionConsentSatisfied(consent.assumeYes, consent.consentedURL, lateSyncURL),
			IsInteractive:              consent.interactive,
		})
		lateBoot, lateErr = handleRemoteSafetyDecision(lateDecision, "bd", lateSyncURL, "", nil, true, new(bool), consent, false)
		return earlyBoot, lateBoot, lateErr, promptCalls
	}

	t.Run("early grant covers the late checkpoint for the same URL", func(t *testing.T) {
		consent := &adoptionConsentState{interactive: true}
		earlyBoot, lateBoot, lateErr, promptCalls := runCheckpoints(t, consent, true, true, false, lateURL, true)
		if !earlyBoot || !lateBoot {
			t.Errorf("bootstrap early=%v late=%v, want both true (one prompt covers the whole run)", earlyBoot, lateBoot)
		}
		if lateErr != nil {
			t.Errorf("late checkpoint errored: %v", lateErr)
		}
		if promptCalls != 1 {
			t.Errorf("prompt fired %d times, want 1 (the early prompt must be the only one)", promptCalls)
		}
		if consent.consentedURL != earlyURL {
			t.Errorf("consent latched to %q, want %q", consent.consentedURL, earlyURL)
		}
	})

	t.Run("late probe newly finding data with no consent refuses before cloning", func(t *testing.T) {
		// The early checkpoint found nothing (no data → ActionNoRemoteData,
		// no prompt, no consent); the late probe now finds derived history.
		// Refusing here — rather than cloning — is the whole point of the
		// second checkpoint.
		consent := &adoptionConsentState{interactive: true}
		earlyBoot, lateBoot, lateErr, promptCalls := runCheckpoints(t, consent, false, true, false, lateURL, true)
		_ = earlyBoot
		if lateBoot {
			t.Error("late checkpoint bootstrapped without consent")
		}
		code, ok := exitCodeFromError(lateErr)
		if !ok || code != 1 {
			t.Fatalf("late refusal err = %v, want exit 1", lateErr)
		}
		if promptCalls != 0 {
			t.Errorf("prompt fired %d times, want 0 (early checkpoint had no data to ask about; the late one may not ask)", promptCalls)
		}
	})

	t.Run("unrelated early consent is not reused for a different URL", func(t *testing.T) {
		// Consent granted at the early prompt for one URL must not adopt a
		// DIFFERENT URL the late checkpoint derives (#6099 item 6): consent
		// binds to what was asked, not to "a remote".
		consent := &adoptionConsentState{interactive: true, consentedURL: otherURL}
		_, lateBoot, lateErr, promptCalls := runCheckpoints(t, consent, false, true, false, lateURL, true)
		if lateBoot {
			t.Error("late checkpoint bootstrapped on consent granted for a different URL")
		}
		code, ok := exitCodeFromError(lateErr)
		if !ok || code != 1 {
			t.Fatalf("late refusal err = %v, want exit 1", lateErr)
		}
		if promptCalls != 0 {
			t.Errorf("prompt fired %d times, want 0 (the late checkpoint must not re-ask)", promptCalls)
		}
	})

	t.Run("--yes permits the late checkpoint for any derived URL", func(t *testing.T) {
		// --yes consents ahead of time to adopting whatever derived origin
		// the run finds, at either checkpoint, without a prompt.
		consent := &adoptionConsentState{assumeYes: true, interactive: true}
		earlyBoot, lateBoot, lateErr, promptCalls := runCheckpoints(t, consent, true, true, false, otherURL, true)
		if !earlyBoot || !lateBoot {
			t.Errorf("bootstrap early=%v late=%v, want both true under --yes", earlyBoot, lateBoot)
		}
		if promptCalls != 0 {
			t.Errorf("prompt fired %d times, want 0 under --yes", promptCalls)
		}
		if lateErr != nil {
			t.Errorf("late checkpoint errored under --yes: %v", lateErr)
		}
	})
}

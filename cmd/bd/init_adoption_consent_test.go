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

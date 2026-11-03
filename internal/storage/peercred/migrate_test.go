package peercred

import (
	"strings"
	"testing"
)

func TestPlanRejectsBlobSeenUnderAnotherBinding(t *testing.T) {
	victim := Binding{Name: "victim", URL: "https://victim.example/db", Username: "victim-user"}
	attacker := Binding{Name: "attacker", URL: "https://attacker.example/db", Username: "victim-user"}
	moved := Candidate{Row: Row{Binding: attacker, Blob: []byte("blob-v")}, Plaintext: "victim-secret"}
	own := Candidate{Row: Row{Binding: team, Blob: []byte("blob-t")}, Plaintext: "peerpass"}

	eligible, skipped := Plan([]Candidate{moved, own}, []Occurrence{
		{Binding: attacker, Blob: []byte("blob-v")},
		{Binding: victim, Blob: []byte("blob-v")}, // the original, now deleted
		{Binding: team, Blob: []byte("blob-t")},
		{Binding: team, Blob: []byte("blob-t")}, // same binding in many commits
	})
	if len(eligible) != 1 || eligible[0].Row.Binding != team {
		t.Errorf("eligible = %+v, want only the team row", eligible)
	}
	if len(skipped) != 1 || skipped[0].Row.Binding != attacker {
		t.Errorf("skipped = %+v, want only the attacker row", skipped)
	}
}

func TestSkipWarningNeverPrintsSecretsOrURL(t *testing.T) {
	out := SkipWarning("linux", Row{
		Binding:     Binding{Name: "team", URL: "https://user:pw@peer.example/db", Username: "peeruser"},
		Sovereignty: "T2",
		Blob:        []byte("ciphertext-bytes"),
	})
	if !strings.Contains(out, "bd federation add-peer team <url> --user peeruser --sovereignty T2") {
		t.Errorf("warning %q lacks the re-add command", out)
	}
	for _, leak := range []string{"peer.example", "user:pw", "ciphertext-bytes"} {
		if strings.Contains(out, leak) {
			t.Errorf("warning %q leaks %q", out, leak)
		}
	}
	if !strings.HasSuffix(out, "\n") || strings.Count(out, "\n") != 1 {
		t.Errorf("warning %q is not one line", out)
	}

	hostile := SkipWarning("linux", Row{Binding: Binding{Name: "x; rm -rf ~\n", Username: "$(id)"}})
	if strings.Count(hostile, "\n") != 1 {
		t.Errorf("hostile name broke the line: %q", hostile)
	}
	if strings.Contains(hostile, "add-peer x; rm") || strings.Contains(hostile, "--user $(id)") {
		t.Errorf("hostile values pasted into the command: %q", hostile)
	}
}

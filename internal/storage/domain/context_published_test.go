package domain

import (
	"reflect"
	"strings"
	"testing"
)

// The sync remote is written the way every token-authenticated git remote is
// written: the credential lives in the URL's userinfo.
const (
	publishedFakeToken  = "ghs_NOTAREALTOKENabcdefghijklmnop"
	publishedFakeRemote = "https://x-access-token:" + publishedFakeToken + "@github.com/example/private.git"
)

// populatedSnapshot fills EVERY member with its own sentinel, so a leak shows
// up as the name of the field that leaked rather than as a failed count.
func populatedSnapshot() ContextInfo {
	return ContextInfo{
		BeadsDir:     "/host/workspace/.beads",
		RepoRoot:     "/host/workspace",
		CWDRepoRoot:  "/host/elsewhere",
		IsRedirected: true,
		IsWorktree:   true,
		Backend:      "dolt",
		DoltMode:     "proxied-server",
		ServerHost:   "10.4.2.9",
		ServerPort:   3307,
		ProxiedDir:   "/host/workspace/.beads/dolt",
		Database:     "beads",
		DataDir:      "/host/workspace/.beads/embeddeddolt",
		ProjectID:    "proj-1",
		SyncRemote:   publishedFakeRemote,
		Role:         "maintainer",
		BdVersion:    "9.9.9",
	}
}

// TestPublishedContextCarriesNothingExcluded is the enforcement half of the
// context exclusions, and it sits HERE — one level below both surfaces —
// because that is what makes them structural. The HTTP allowlist test pins
// what the handler writes; this pins that the values it is written from never
// contained the excluded members in the first place, so neither surface can
// republish one by adding a field to its own response struct.
func TestPublishedContextCarriesNothingExcluded(t *testing.T) {
	snapshot := populatedSnapshot()
	published := PublishedContext(snapshot)

	excluded := map[string]string{
		"SyncRemote":  snapshot.SyncRemote,
		"CWDRepoRoot": snapshot.CWDRepoRoot,
		"ServerHost":  snapshot.ServerHost,
		"ProxiedDir":  snapshot.ProxiedDir,
		"DataDir":     snapshot.DataDir,
		"Role":        snapshot.Role,
	}

	v := reflect.ValueOf(published)
	for i := range v.NumField() {
		field := v.Type().Field(i)
		got, ok := v.Field(i).Interface().(string)
		if !ok {
			continue
		}
		for name, sentinel := range excluded {
			if sentinel != "" && strings.Contains(got, sentinel) {
				t.Errorf("published field %s carries the excluded %s: %q", field.Name, name, got)
			}
		}
	}

	// Named separately from the value sweep above: a credential could also
	// arrive embedded in some other field, and the sweep alone would not see a
	// member that grew the wrong name.
	if _, found := reflect.TypeOf(published).FieldByName("SyncRemote"); found {
		t.Error("PublishedContextFields grew a SyncRemote member; remote URLs routinely embed credentials " +
			"and this type is the reason no surface has to remember that")
	}
	if _, found := reflect.TypeOf(published).FieldByName("ServerPort"); found {
		t.Error("PublishedContextFields grew a ServerPort member; the database bind endpoint is excluded " +
			"so that no consumer is invited to dial the database directly")
	}
}

// TestPublishedContextCarriesTheIdentity is the other direction: every member
// it does have has to be the snapshot's own value, because two surfaces read
// their workspace identity from exactly this and a transposed pair would name
// the same workspace two different ways on each of them.
func TestPublishedContextCarriesTheIdentity(t *testing.T) {
	snapshot := populatedSnapshot()
	published := PublishedContext(snapshot)

	for _, tc := range []struct{ name, got, want string }{
		{"BdVersion", published.BdVersion, snapshot.BdVersion},
		{"Backend", published.Backend, snapshot.Backend},
		{"DoltMode", published.DoltMode, snapshot.DoltMode},
		{"Database", published.Database, snapshot.Database},
		{"BeadsDir", published.BeadsDir, snapshot.BeadsDir},
		{"RepoRoot", published.RepoRoot, snapshot.RepoRoot},
		{"ProjectID", published.ProjectID, snapshot.ProjectID},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}

	// The count is part of the claim: a new member is a new disclosure on
	// every surface that consumes this at once, which is a review, not a
	// refactor.
	if n := reflect.TypeOf(published).NumField(); n != 7 {
		t.Errorf("PublishedContextFields has %d members, want 7 — adding one publishes it on every context surface", n)
	}
}

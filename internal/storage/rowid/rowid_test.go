package rowid

import (
	"database/sql"
	"testing"
)

func v(s string) sql.NullString               { return sql.NullString{String: s, Valid: true} }
func null() sql.NullString                    { return sql.NullString{} }
func fs(f ...sql.NullString) []sql.NullString { return f }

// Golden vectors. These were computed independently from the spec primitives
// (SHA-256 over the "n"/"v<len>:<bytes>" field encoding, then RFC-4122 v5 over
// "table\x1fordinal\x1fdigest") without importing this package. If Digest or
// New ever changes its namespace, separator, or encoding these will break —
// and that break is intentional, because such a change re-derives every
// backfilled row id and must be a conscious, migration-backed decision.
func TestGolden(t *testing.T) {
	eventsDigest := Digest(fs(v("bd-1"), v("created"), v("steve"), null(), v("open"), null(), v("2026-06-09 12:00:00")))
	if want := "f686b5a4b239e1e8c0f5fcc170fc37e3d2c820847e930423b81492401bcc88d7"; eventsDigest != want {
		t.Errorf("events Digest = %q, want %q", eventsDigest, want)
	}
	if got, want := New("events", 0, eventsDigest), "248ba505-db16-50cf-9edb-68850b1fb2d2"; got != want {
		t.Errorf("New(events, 0) = %q, want %q", got, want)
	}
	if got, want := New("events", 1, eventsDigest), "a08156c6-4bb7-56a7-9b35-d2b4f991aeac"; got != want {
		t.Errorf("New(events, 1) = %q, want %q", got, want)
	}

	commentsDigest := Digest(fs(v("bd-1"), v("steve"), v("hello"), v("2026-06-09 12:00:00")))
	if want := "e2adcdfb59141e491ee69da49ad318a7943dfeda0fd3e43a0a2c5b93aafe05a3"; commentsDigest != want {
		t.Errorf("comments Digest = %q, want %q", commentsDigest, want)
	}
	if got, want := New("comments", 0, commentsDigest), "07fd35e3-caad-56d1-93df-9b3478ed78de"; got != want {
		t.Errorf("New(comments, 0) = %q, want %q", got, want)
	}
}

// NULL and the empty string are distinct row contents and must digest apart.
func TestDigestNullVsEmpty(t *testing.T) {
	if Digest(fs(null())) == Digest(fs(v(""))) {
		t.Fatal("Digest(NULL) == Digest(\"\"); NULL must be distinguishable from empty")
	}
	if got, want := Digest(fs(null())), "1b16b1df538ba12dc3f97edbb85caa7050d46c148134290feba80f8236c83db9"; got != want {
		t.Errorf("Digest(NULL) = %q, want %q", got, want)
	}
	if got, want := Digest(fs(v(""))), "e016774934e7ab6018337b5335e8de6c36af03654d72513a7d2dd94e4e1cbef5"; got != want {
		t.Errorf("Digest(\"\") = %q, want %q", got, want)
	}
}

// The field encoding must be a prefix code: value bytes must not bleed across
// field boundaries ("ab","c" vs "a","bc"), and values containing the encoding's
// own marker characters must not alias.
func TestDigestNoAliasing(t *testing.T) {
	pairs := [][2][]sql.NullString{
		{fs(v("ab"), v("c")), fs(v("a"), v("bc"))},
		{fs(v("v1:x")), fs(v("v1:"), v("x"))},
		{fs(v("n")), fs(null())},
		{fs(v("a"), null()), fs(v("a"))},
	}
	for _, p := range pairs {
		if Digest(p[0]) == Digest(p[1]) {
			t.Errorf("Digest aliased %v and %v", p[0], p[1])
		}
	}
}

// Same content, different table or ordinal -> different id; everything equal
// -> same id, every time.
func TestNewDistinctness(t *testing.T) {
	d := Digest(fs(v("x")))
	if New("events", 0, d) != New("events", 0, d) {
		t.Fatal("New not deterministic")
	}
	if New("events", 0, d) == New("comments", 0, d) {
		t.Error("New collides across tables")
	}
	if New("events", 0, d) == New("events", 1, d) {
		t.Error("New collides across ordinals")
	}
}

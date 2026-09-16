package main

import (
	"reflect"
	"testing"
)

// kvPairsWithPrefix is the shared post-filter on both `bd kv list` paths: a
// no-op re-check after a SQL-side prefix read, and the whole filter on the
// GetAllConfig fallback — so its semantics define the flag's output either way.
func TestKVPairsWithPrefix(t *testing.T) {
	allConfig := map[string]string{
		"kv.mail.dog.m1":      "a",
		"kv.mail.dog.m2":      "b",
		"kv.mail.doggerel.m3": "c",
		"kv.mail.hare.m1":     "d",
		"jira.url":            "not-kv",
	}

	t.Run("empty prefix lists every kv pair", func(t *testing.T) {
		got := kvPairsWithPrefix(allConfig, "")
		want := map[string]string{
			"mail.dog.m1":      "a",
			"mail.dog.m2":      "b",
			"mail.doggerel.m3": "c",
			"mail.hare.m1":     "d",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("prefix narrows to one box and strips kv.", func(t *testing.T) {
		got := kvPairsWithPrefix(allConfig, "mail.dog.")
		want := map[string]string{
			"mail.dog.m1": "a",
			"mail.dog.m2": "b",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("prefix without trailing dot matches sibling boxes literally", func(t *testing.T) {
		got := kvPairsWithPrefix(allConfig, "mail.dog")
		if len(got) != 3 {
			t.Fatalf("got %v, want mail.dog.* plus mail.doggerel.*", got)
		}
	})

	t.Run("no matches returns empty map", func(t *testing.T) {
		got := kvPairsWithPrefix(allConfig, "mail.owl.")
		if len(got) != 0 {
			t.Fatalf("got %v, want empty", got)
		}
	})
}

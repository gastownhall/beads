package flatfile

import (
	"path/filepath"
	"sync"
	"testing"
)

func newTestStore(t *testing.T) *FlatFileStore {
	t.Helper()
	s, err := NewFlatFileStore(filepath.Join(t.TempDir(), ".beads"))
	if err != nil {
		t.Fatalf("NewFlatFileStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestNextSequentialID(t *testing.T) {
	s := newTestStore(t)

	id1, err := s.nextSequentialID("proj")
	if err != nil {
		t.Fatalf("nextSequentialID: %v", err)
	}
	if id1 != "proj-1" {
		t.Errorf("first ID = %q, want %q", id1, "proj-1")
	}

	id2, err := s.nextSequentialID("proj")
	if err != nil {
		t.Fatalf("nextSequentialID: %v", err)
	}
	if id2 != "proj-2" {
		t.Errorf("second ID = %q, want %q", id2, "proj-2")
	}
}

func TestNextSequentialIDMultiplePrefix(t *testing.T) {
	s := newTestStore(t)

	id1, _ := s.nextSequentialID("alpha")
	id2, _ := s.nextSequentialID("beta")
	id3, _ := s.nextSequentialID("alpha")

	if id1 != "alpha-1" {
		t.Errorf("id1 = %q, want %q", id1, "alpha-1")
	}
	if id2 != "beta-1" {
		t.Errorf("id2 = %q, want %q", id2, "beta-1")
	}
	if id3 != "alpha-2" {
		t.Errorf("id3 = %q, want %q", id3, "alpha-2")
	}
}

func TestPeekSequentialID(t *testing.T) {
	s := newTestStore(t)

	last, err := s.peekSequentialID("proj")
	if err != nil {
		t.Fatalf("peekSequentialID: %v", err)
	}
	if last != 0 {
		t.Errorf("peek before any allocation = %d, want 0", last)
	}

	s.nextSequentialID("proj")
	s.nextSequentialID("proj")

	last, err = s.peekSequentialID("proj")
	if err != nil {
		t.Fatalf("peekSequentialID: %v", err)
	}
	if last != 2 {
		t.Errorf("peek after 2 allocations = %d, want 2", last)
	}
}

func TestCounterPersistence(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".beads")

	s1, err := NewFlatFileStore(dir)
	if err != nil {
		t.Fatalf("NewFlatFileStore: %v", err)
	}
	s1.nextSequentialID("proj")
	s1.nextSequentialID("proj")
	s1.nextSequentialID("proj")
	s1.Close()

	s2, err := NewFlatFileStore(dir)
	if err != nil {
		t.Fatalf("NewFlatFileStore reopen: %v", err)
	}
	defer s2.Close()

	id, err := s2.nextSequentialID("proj")
	if err != nil {
		t.Fatalf("nextSequentialID after reopen: %v", err)
	}
	if id != "proj-4" {
		t.Errorf("ID after reopen = %q, want %q", id, "proj-4")
	}
}

func TestCounterConcurrency(t *testing.T) {
	s := newTestStore(t)
	const goroutines = 20

	var wg sync.WaitGroup
	ids := make(chan string, goroutines)

	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := s.nextSequentialID("proj")
			if err != nil {
				t.Errorf("nextSequentialID: %v", err)
				return
			}
			ids <- id
		}()
	}
	wg.Wait()
	close(ids)

	seen := make(map[string]bool)
	for id := range ids {
		if seen[id] {
			t.Errorf("duplicate ID: %s", id)
		}
		seen[id] = true
	}
	if len(seen) != goroutines {
		t.Errorf("got %d unique IDs, want %d", len(seen), goroutines)
	}
}

//go:build cgo

package corpus_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// TestCorpusPartsDealEveryShapeOnce runs without the embedded engine. It
// holds the deal TestServedCorpusPart1..N run to what makes splitting the
// corpus safe: every shape lands in exactly one part, each part keeps corpus
// order (so a part's reads still run before its writes, on its own fresh
// seed), no part is empty, and there is one TestServedCorpusPartN per part.
func TestCorpusPartsDealEveryShapeOnce(t *testing.T) {
	if len(servedCorpusParts) != corpusParts {
		t.Fatalf("servedCorpusParts has %d tests, want corpusParts = %d", len(servedCorpusParts), corpusParts)
	}
	c, _ := loadCorpus(t)
	index := map[string]int{}
	for i, s := range c.Shapes {
		index[s.ID] = i
	}
	seen := map[string]int{}
	for part := 1; part <= corpusParts; part++ {
		shapes := corpusPart(c.Shapes, part)
		if len(shapes) == 0 {
			t.Errorf("part %d runs no shapes", part)
		}
		last := -1
		for _, s := range shapes {
			if prev, dup := seen[s.ID]; dup {
				t.Errorf("shape %s is in part %d and part %d", s.ID, prev, part)
			}
			seen[s.ID] = part
			i, ok := index[s.ID]
			if !ok {
				t.Errorf("part %d runs %s, which is not a corpus shape", part, s.ID)
				continue
			}
			if i <= last {
				t.Errorf("part %d runs %s out of corpus order", part, s.ID)
			}
			last = i
		}
	}
	for _, s := range c.Shapes {
		if _, ok := seen[s.ID]; !ok {
			t.Errorf("shape %s is in no part", s.ID)
		}
	}
}

// The deal itself, on a corpus of 10 against 4 parts: round robin in corpus
// order.
func TestCorpusPartIsRoundRobinInOrder(t *testing.T) {
	var shapes []shape
	for i := 0; i < 10; i++ {
		shapes = append(shapes, shape{ID: fmt.Sprint(i)})
	}
	want := map[int]string{1: "[0 4 8]", 2: "[1 5 9]", 3: "[2 6]", 4: "[3 7]"}
	for part, w := range want {
		var ids []string
		for _, s := range corpusPartOf(shapes, part, 4) {
			ids = append(ids, s.ID)
		}
		if got := fmt.Sprint(ids); got != w {
			t.Errorf("part %d of 4 = %s, want %s", part, got, w)
		}
	}
}

// fakeBD writes a stand-in for bd that appends its arguments, one call per
// line, to calls.log in its working directory, and exits 3 when an argument
// is "boom".
func fakeBD(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "bd")
	script := "#!/bin/sh\necho \"$*\" >> calls.log\nfor a in \"$@\"; do [ \"$a\" = boom ] && { echo boom >&2; exit 3; }; done\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin
}

func calls(t *testing.T, sd *side) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(sd.dir, "calls.log"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

// prepareSide is what lets testServedCorpus seed the served and embedded
// sides at once: each side attaches, writes its assets and runs the seed in
// its own directories, in seed order, and a failure comes back as an error
// rather than a t.Fatal from a goroutine that is not the test's.
func TestPrepareSideSeedsBothSidesConcurrently(t *testing.T) {
	bin := fakeBD(t)
	c := &corpusFile{
		Files: []corpusAsset{{Name: "plan.json", Content: "{}"}},
		Seed:  [][]string{{"create", "--graph", "{file:plan.json}"}, {"close", "e2e-x"}},
	}
	a := &side{name: "served", dir: t.TempDir(), files: t.TempDir()}
	b := &side{name: "embedded", dir: t.TempDir(), files: t.TempDir()}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, sd := range []*side{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = prepareSide(context.Background(), t, bin, c, sd, []string{"init", sd.name})
		}()
	}
	wg.Wait()
	for i, sd := range []*side{a, b} {
		if errs[i] != nil {
			t.Fatalf("%s: %v", sd.name, errs[i])
		}
		want := []string{"init " + sd.name, "create --graph " + filepath.Join(sd.files, "plan.json"), "close e2e-x"}
		if got := calls(t, sd); !reflect.DeepEqual(got, want) {
			t.Errorf("%s ran %q, want %q", sd.name, got, want)
		}
		if _, err := os.Stat(filepath.Join(sd.files, "plan.json")); err != nil {
			t.Errorf("%s: asset not written: %v", sd.name, err)
		}
	}
}

func TestPrepareSideReportsAFailedStepAsAnError(t *testing.T) {
	bin := fakeBD(t)
	c := &corpusFile{Seed: [][]string{{"create", "boom"}, {"close", "e2e-x"}}}
	sd := &side{name: "embedded", dir: t.TempDir(), files: t.TempDir()}
	err := prepareSide(context.Background(), t, bin, c, sd, []string{"init"})
	if err == nil || !strings.Contains(err.Error(), "seed [create boom] on embedded failed") || !strings.Contains(err.Error(), "exit=3") {
		t.Fatalf("err = %v, want the failed seed step with its exit", err)
	}
	if got := calls(t, sd); !reflect.DeepEqual(got, []string{"init", "create boom"}) {
		t.Errorf("ran %q; the seed must stop at the failed step", got)
	}

	sd = &side{name: "served", dir: t.TempDir(), files: t.TempDir()}
	if err := prepareSide(context.Background(), t, bin, c, sd, []string{"connect", "boom"}); err == nil || !strings.Contains(err.Error(), "bd connect boom on served") {
		t.Fatalf("err = %v, want the failed attach", err)
	}
}

// Once the other side has failed, the test cancels: a side stops before its
// next step instead of seeding on for nothing.
func TestPrepareSideStopsWhenCancelled(t *testing.T) {
	bin := fakeBD(t)
	c := &corpusFile{Seed: [][]string{{"create", "e2e-x"}}}
	sd := &side{name: "embedded", dir: t.TempDir(), files: t.TempDir()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := prepareSide(ctx, t, bin, c, sd, []string{"init"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(filepath.Join(sd.dir, "calls.log")); !os.IsNotExist(err) {
		t.Errorf("bd ran after the cancel (calls.log: %v)", err)
	}
}

// A part test wired to another part's number would run that part's shapes
// twice and drop its own without a word; testServedCorpus refuses to run
// unless the test's name is its part's (corpusPartNameErr).
func TestCorpusPartNameMustMatchItsPart(t *testing.T) {
	if err := corpusPartNameErr("TestServedCorpusPart3", 3); err != nil {
		t.Errorf("matching name: %v", err)
	}
	for _, c := range []struct {
		name string
		part int
	}{
		{"TestServedCorpusPart3", 2},
		{"TestServedCorpusPart1", 10},
		{"TestServedCorpusPart10", 1},
		{"TestServedCorpusPart3/sub", 3},
		{"TestServedCorpus", 1},
		// A stray part past corpusParts would run no shapes at all.
		{fmt.Sprintf("TestServedCorpusPart%d", corpusParts+1), corpusParts + 1},
		{"TestServedCorpusPart0", 0},
	} {
		if err := corpusPartNameErr(c.name, c.part); err == nil {
			t.Errorf("corpusPartNameErr(%q, %d) = nil, want a mismatch", c.name, c.part)
		}
	}
	// Every wired part test passes the guard: run each with the engine
	// off, so the guard (which comes first) is all that can fail it.
	for k, fn := range servedCorpusParts {
		want := fmt.Sprintf("TestServedCorpusPart%d", k+1)
		if got := testFuncName(fn); got != want {
			t.Errorf("servedCorpusParts[%d] is %s, want %s", k, got, want)
		}
	}
}

// testFuncName is fn's unqualified function name.
func testFuncName(fn func(*testing.T)) string {
	full := runtime.FuncForPC(reflect.ValueOf(fn).Pointer()).Name()
	return full[strings.LastIndex(full, ".")+1:]
}

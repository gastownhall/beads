package linear

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteAndReadLastPullTimestamp(t *testing.T) {
	dir := t.TempDir()

	if err := WriteLastPullTimestamp(dir); err != nil {
		t.Fatalf("WriteLastPullTimestamp: %v", err)
	}

	got, err := ReadLastPullTimestamp(dir)
	if err != nil {
		t.Fatalf("ReadLastPullTimestamp: %v", err)
	}
	if got.IsZero() {
		t.Fatal("expected non-zero timestamp after write")
	}
	if time.Since(got) > 5*time.Second {
		t.Fatalf("timestamp too old: %v (expected within 5s of now)", got)
	}
}

func TestReadLastPullTimestamp_MissingFile(t *testing.T) {
	dir := t.TempDir()

	got, err := ReadLastPullTimestamp(dir)
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if !got.IsZero() {
		t.Fatalf("expected zero time for missing file, got: %v", got)
	}
}

func TestReadLastPullTimestamp_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, lastPullFileName), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadLastPullTimestamp(dir)
	if err != nil {
		t.Fatalf("expected nil error for empty file, got: %v", err)
	}
	if !got.IsZero() {
		t.Fatalf("expected zero time for empty file, got: %v", got)
	}
}

func TestReadLastPullTimestamp_MalformedFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, lastPullFileName), []byte("not-a-timestamp\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := ReadLastPullTimestamp(dir)
	if err == nil {
		t.Fatal("expected error for malformed timestamp")
	}
}

func TestIsPullStale(t *testing.T) {
	t.Run("missing file is always stale", func(t *testing.T) {
		dir := t.TempDir()
		if !IsPullStale(dir, 20*time.Minute) {
			t.Fatal("expected stale when last_pull file is missing")
		}
	})

	t.Run("just written is fresh", func(t *testing.T) {
		dir := t.TempDir()
		if err := WriteLastPullTimestamp(dir); err != nil {
			t.Fatal(err)
		}
		if IsPullStale(dir, 20*time.Minute) {
			t.Fatal("expected fresh immediately after write")
		}
	})

	t.Run("old timestamp is stale", func(t *testing.T) {
		dir := t.TempDir()
		oldTime := time.Now().UTC().Add(-30 * time.Minute).Format(time.RFC3339)
		if err := os.WriteFile(filepath.Join(dir, lastPullFileName), []byte(oldTime+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if !IsPullStale(dir, 20*time.Minute) {
			t.Fatal("expected stale for 30m-old timestamp with 20m threshold")
		}
	})
}

func TestIsWithinDebounce(t *testing.T) {
	t.Run("no file returns false", func(t *testing.T) {
		dir := t.TempDir()
		if IsWithinDebounce(dir) {
			t.Fatal("expected false when file doesn't exist")
		}
	})

	t.Run("just written returns true", func(t *testing.T) {
		dir := t.TempDir()
		if err := WriteLastPullTimestamp(dir); err != nil {
			t.Fatal(err)
		}
		if !IsWithinDebounce(dir) {
			t.Fatal("expected within debounce immediately after write")
		}
	})

	t.Run("old timestamp returns false", func(t *testing.T) {
		dir := t.TempDir()
		oldTime := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339)
		if err := os.WriteFile(filepath.Join(dir, lastPullFileName), []byte(oldTime+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if IsWithinDebounce(dir) {
			t.Fatal("expected not within debounce for 10m-old timestamp")
		}
	})
}

func TestGetStalenessInfo(t *testing.T) {
	t.Run("never pulled", func(t *testing.T) {
		dir := t.TempDir()
		info := GetStalenessInfo(dir, 20*time.Minute)
		if !info.NeverPulled {
			t.Fatal("expected NeverPulled=true")
		}
		if !info.IsStale {
			t.Fatal("expected IsStale=true when never pulled")
		}
	})

	t.Run("fresh pull", func(t *testing.T) {
		dir := t.TempDir()
		if err := WriteLastPullTimestamp(dir); err != nil {
			t.Fatal(err)
		}
		info := GetStalenessInfo(dir, 20*time.Minute)
		if info.NeverPulled {
			t.Fatal("expected NeverPulled=false")
		}
		if !info.IsFresh {
			t.Fatal("expected IsFresh=true")
		}
		if info.IsStale {
			t.Fatal("expected IsStale=false")
		}
	})
}

func TestFormatAge(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "30s"},
		{5 * time.Minute, "5m"},
		{45 * time.Minute, "45m"},
		{1 * time.Hour, "1h"},
		{90 * time.Minute, "1h30m"},
		{2*time.Hour + 15*time.Minute, "2h15m"},
	}
	for _, tt := range tests {
		got := FormatAge(tt.d)
		if got != tt.want {
			t.Errorf("FormatAge(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestPullIfStaleSkipsWhenFresh(t *testing.T) {
	dir := t.TempDir()

	// Write a recent timestamp — data is fresh
	if err := WriteLastPullTimestamp(dir); err != nil {
		t.Fatal(err)
	}

	// Verify that IsPullStale returns false (would skip pull)
	if IsPullStale(dir, DefaultStaleThreshold) {
		t.Fatal("expected IsPullStale=false immediately after writing timestamp")
	}

	// Verify StalenessInfo agrees
	info := GetStalenessInfo(dir, DefaultStaleThreshold)
	if !info.IsFresh {
		t.Fatal("expected IsFresh=true")
	}
	if info.IsStale {
		t.Fatal("expected IsStale=false for fresh data")
	}
}

func TestPullIfStaleDebounce(t *testing.T) {
	dir := t.TempDir()

	// Write a timestamp that's past the threshold (stale) but within debounce
	// e.g., 3 minutes ago: past a 1-minute threshold but within the 5-minute debounce
	recentTime := time.Now().UTC().Add(-3 * time.Minute).Format(time.RFC3339)
	if err := os.WriteFile(filepath.Join(dir, lastPullFileName), []byte(recentTime+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// With a 1-minute threshold, data is "stale" by threshold
	if !IsPullStale(dir, 1*time.Minute) {
		t.Fatal("expected stale with 1m threshold and 3m-old timestamp")
	}

	// But the debounce window (5 min) should prevent a pull
	if !IsWithinDebounce(dir) {
		t.Fatal("expected within debounce for 3m-old timestamp (debounce is 5m)")
	}

	// Write a timestamp that's outside the debounce window
	oldTime := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339)
	if err := os.WriteFile(filepath.Join(dir, lastPullFileName), []byte(oldTime+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Both stale and outside debounce
	if !IsPullStale(dir, 1*time.Minute) {
		t.Fatal("expected stale for 10m-old timestamp")
	}
	if IsWithinDebounce(dir) {
		t.Fatal("expected not within debounce for 10m-old timestamp")
	}
}

func TestWriteLastPullTimestamp_EmptyDir(t *testing.T) {
	err := WriteLastPullTimestamp("")
	if err == nil {
		t.Fatal("expected error for empty beadsDir")
	}
}

func TestReadLastPullTimestamp_EmptyDir(t *testing.T) {
	_, err := ReadLastPullTimestamp("")
	if err == nil {
		t.Fatal("expected error for empty beadsDir")
	}
}

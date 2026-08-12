package execenv

import (
	"runtime"
	"slices"
	"testing"
)

func TestKeyIdentityForWindows(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		windows bool
		want    string
	}{
		{name: "Unix stays exact", key: "BeAdS_DiR", want: "BeAdS_DiR"},
		{name: "Windows lowercases", key: "BeAdS_DiR", windows: true, want: "beads_dir"},
		{name: "Windows keeps long s distinct", key: "BEADſ_DIR", windows: true, want: "beadſ_dir"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := keyIdentityForWindows(tt.key, tt.windows); got != tt.want {
				t.Errorf("keyIdentityForWindows(%q, %v) = %q, want %q", tt.key, tt.windows, got, tt.want)
			}
		})
	}
}

func TestKeyEqualUsesHostSemantics(t *testing.T) {
	if got, want := KeyEqual("BeAdS_DiR", "BEADS_DIR"), runtime.GOOS == "windows"; got != want {
		t.Errorf("KeyEqual mixed-case = %v, want %v on %s", got, want, runtime.GOOS)
	}
	if KeyEqual("BEADſ_DIR", "BEADS_DIR") {
		t.Error("KeyEqual merged Unicode long s with ASCII s")
	}
}

func TestWithoutUsesHostSemanticsAndPreservesOtherEntries(t *testing.T) {
	in := []string{
		"FIRST=keep-first",
		"beads_dir=drop-on-windows",
		"BEADS_DIR=drop-canonical",
		"ALLOWED=keep-duplicate-first",
		"MALFORMED",
		"BEADſ_DIR=keep-unicode-near-collision",
		"ALLOWED=keep-duplicate-second",
		`=C:=C:\work`,
		"LAST=keep-last",
	}
	original := slices.Clone(in)

	got := Without(in, "BEADS_DIR")
	want := []string{
		"FIRST=keep-first",
		"beads_dir=drop-on-windows",
		"ALLOWED=keep-duplicate-first",
		"MALFORMED",
		"BEADſ_DIR=keep-unicode-near-collision",
		"ALLOWED=keep-duplicate-second",
		`=C:=C:\work`,
		"LAST=keep-last",
	}
	if runtime.GOOS == "windows" {
		want = slices.Delete(want, 1, 2)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Without() = %q, want %q on %s", got, want, runtime.GOOS)
	}
	if !slices.Equal(in, original) {
		t.Fatalf("Without modified input: got %q, want %q", in, original)
	}
}

func TestLookupUsesLastEffectiveValue(t *testing.T) {
	env := []string{
		"TARGET=first",
		"target=mixed-case",
		"TARGET=last",
		"MALFORMED",
		`=C:=C:\work`,
	}
	value, ok := Lookup(env, "TARGET")
	if !ok || value != "last" {
		t.Fatalf("Lookup(TARGET) = %q, %v; want last, true", value, ok)
	}
	if value, ok := Lookup(env, "target"); runtime.GOOS == "windows" {
		if !ok || value != "last" {
			t.Fatalf("Lookup(target) = %q, %v; want last, true on Windows", value, ok)
		}
	} else if !ok || value != "mixed-case" {
		t.Fatalf("Lookup(target) = %q, %v; want mixed-case, true on %s", value, ok, runtime.GOOS)
	}
	if value, ok := Lookup(env, "MISSING"); ok || value != "" {
		t.Fatalf("Lookup(MISSING) = %q, %v; want empty, false", value, ok)
	}
	if value, ok := Lookup(env, "=C:"); !ok || value != `C:\work` {
		t.Fatalf("Lookup(=C:) = %q, %v; want %q, true", value, ok, `C:\work`)
	}
}

func TestContainsKeyWithPrefixUsesKeyIdentity(t *testing.T) {
	env := []string{
		"MALFORMED",
		`=C:=C:\work`,
		"dolt_remote_password=secret",
		"DOLT_REMOTEſ_PASSWORD=near-collision",
	}
	if got, want := ContainsKeyWithPrefix(env, "DOLT_REMOTE_"), runtime.GOOS == "windows"; got != want {
		t.Errorf("ContainsKeyWithPrefix mixed-case = %v, want %v on %s", got, want, runtime.GOOS)
	}
	if ContainsKeyWithPrefix([]string{"DOLT_REMOTEſ_PASSWORD=secret"}, "DOLT_REMOTES_") {
		t.Error("ContainsKeyWithPrefix merged Unicode long s with ASCII s")
	}
	if ContainsKeyWithPrefix([]string{"DOLT_REMOTE_PASSWORD", `=C:=C:\work`}, "DOLT_REMOTE_") {
		t.Error("ContainsKeyWithPrefix treated a malformed or drive entry as a matching key-value entry")
	}
}

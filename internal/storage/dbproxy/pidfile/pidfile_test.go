package pidfile

import (
	"testing"
)

func TestPidFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	in := PidFile{Pid: 42, Port: 1234, UpstreamID: "abc123"}
	if err := Write(dir, "proxy.pid", in); err != nil {
		t.Fatalf("Write: %v", err)
	}
	out, err := Read(dir, "proxy.pid")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if out == nil {
		t.Fatal("Read returned nil")
	}
	if *out != in {
		t.Errorf("round-trip: got %+v, want %+v", *out, in)
	}
}

func TestPidFileLegacyWithoutUpstreamID(t *testing.T) {
	dir := t.TempDir()
	in := PidFile{Pid: 7, Port: 8}
	if err := Write(dir, "proxy.pid", in); err != nil {
		t.Fatalf("Write: %v", err)
	}
	out, err := Read(dir, "proxy.pid")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if out == nil || out.UpstreamID != "" {
		t.Errorf("expected empty UpstreamID for legacy pidfile, got %+v", out)
	}
}

package pidfile

import (
	"errors"
	"os"
	"path/filepath"
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

func TestPidFileV1JSONRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "proxy.pid"), []byte(`{"pid":7,"port":8}`), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	pf, err := Read(dir, "proxy.pid")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if pf == nil || pf.Schema != 0 || pf.Kind != "" || pf.Birth != "" || pf.RootID != "" || pf.ControlPort != 0 {
		t.Fatalf("v1 pidfile = %+v, want zero v2 fields", pf)
	}
}

func TestPidFileV2RoundTrip(t *testing.T) {
	dir := t.TempDir()
	in := PidFile{
		Pid:         42,
		Port:        1234,
		UpstreamID:  "abc123",
		Schema:      SchemaV2,
		Kind:        KindProxy,
		Birth:       "linux-v1:boot:123",
		RootID:      "root",
		ControlPort: 4321,
	}
	if err := Write(dir, "proxy.pid", in); err != nil {
		t.Fatalf("Write: %v", err)
	}
	out, err := Read(dir, "proxy.pid")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if out == nil || *out != in {
		t.Errorf("round-trip: got %+v, want %+v", out, in)
	}
}

func TestValidateV2(t *testing.T) {
	valid := PidFile{Pid: 1, Port: 1234, Schema: SchemaV2, Kind: KindProxy, Birth: "birth"}
	tests := []struct {
		name string
		pf   PidFile
		want error
	}{
		{name: "valid", pf: valid},
		{name: "legacy schema", pf: PidFile{Pid: 1, Port: 1234, Kind: KindProxy, Birth: "birth"}, want: ErrLegacySchema},
		{name: "bad pid", pf: PidFile{Port: 1234, Schema: SchemaV2, Kind: KindProxy, Birth: "birth"}, want: ErrBadPid},
		{name: "bad port", pf: PidFile{Pid: 1, Schema: SchemaV2, Kind: KindProxy, Birth: "birth"}, want: ErrBadPort},
		{name: "bad control port", pf: PidFile{Pid: 1, Port: 1234, ControlPort: 65536, Schema: SchemaV2, Kind: KindProxy, Birth: "birth"}, want: ErrBadPort},
		{name: "wrong kind", pf: PidFile{Pid: 1, Port: 1234, Schema: SchemaV2, Kind: KindDoltBackend, Birth: "birth"}, want: ErrKindMismatch},
		{name: "missing birth", pf: PidFile{Pid: 1, Port: 1234, Schema: SchemaV2, Kind: KindProxy}, want: ErrMissingBirth},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.pf.ValidateV2(KindProxy)
			if !errors.Is(err, tt.want) {
				t.Errorf("ValidateV2() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestReadMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "proxy.pid"), []byte(`{"pid":`), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, err := Read(dir, "proxy.pid"); err == nil {
		t.Fatal("Read(malformed JSON) succeeded, want error")
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

package configfile

import (
	"testing"
)

// doltHostSpellings is every spelling the classifier must agree on, by kind.
// One address written two ways gets one answer.
var doltHostSpellings = []struct {
	host string
	kind DoltHostKind
}{
	{"", DoltHostBdServer},
	{"  ", DoltHostBdServer},
	{"127.0.0.1", DoltHostBdServer},
	{" 127.0.0.1 ", DoltHostBdServer},
	{"[127.0.0.1]", DoltHostBdServer},
	{"::ffff:127.0.0.1", DoltHostBdServer},
	{"[::ffff:127.0.0.1]", DoltHostBdServer},
	{"localhost", DoltHostBdServer},
	{"LOCALHOST", DoltHostBdServer},
	{" LocalHost ", DoltHostBdServer},
	{"0.0.0.0", DoltHostBdServer},

	{"::1", DoltHostIPv6Loopback},
	{"[::1]", DoltHostIPv6Loopback},
	{" [::1] ", DoltHostIPv6Loopback},
	{"0:0:0:0:0:0:0:1", DoltHostIPv6Loopback},
	{"[0:0:0:0:0:0:0:1]", DoltHostIPv6Loopback},
	{"0000:0000:0000:0000:0000:0000:0000:0001", DoltHostIPv6Loopback},
	{"::", DoltHostIPv6Loopback},
	{"[::]", DoltHostIPv6Loopback},

	{"127.0.0.2", DoltHostOtherLoopback},
	{"127.1.2.3", DoltHostOtherLoopback},
	{"::ffff:127.0.0.2", DoltHostOtherLoopback},

	{"db.example.com", DoltHostRemote},
	{"10.0.0.5", DoltHostRemote},
	{"192.168.1.10", DoltHostRemote},
	{"2001:db8::1", DoltHostRemote},
	{"[2001:db8::1]", DoltHostRemote},
	{"fe80::1%eth0", DoltHostRemote},
	{"::1%lo", DoltHostRemote},
	{"localhost.example.com", DoltHostRemote},
	{"[db]", DoltHostRemote},
}

func TestClassifyDoltHost(t *testing.T) {
	for _, tc := range doltHostSpellings {
		if got := ClassifyDoltHost(tc.host); got != tc.kind {
			t.Errorf("ClassifyDoltHost(%q) = %v, want %v", tc.host, got, tc.kind)
		}
	}
}

// TestBdRunsServerAt pins the ownership invariant: bd treats a server as its
// own only at 127.0.0.1, the one address its own server binds, in any
// spelling. The IPv6 loopback, another loopback address and another machine
// are never bd's.
func TestBdRunsServerAt(t *testing.T) {
	for _, tc := range doltHostSpellings {
		want := tc.kind == DoltHostBdServer
		if got := BdRunsServerAt(tc.host); got != want {
			t.Errorf("BdRunsServerAt(%q) = %v, want %v", tc.host, got, want)
		}
	}
}

// TestIsLocalHostString pins the machine-locality question: bd's address and
// the IPv6 loopback are this machine, in every spelling; another loopback
// address keeps implying a server bd does not run (GH#3545 inference), and
// every remote host is remote.
func TestIsLocalHostString(t *testing.T) {
	for _, tc := range doltHostSpellings {
		want := tc.kind == DoltHostBdServer || tc.kind == DoltHostIPv6Loopback
		if got := IsLocalHostString(tc.host); got != want {
			t.Errorf("IsLocalHostString(%q) = %v, want %v", tc.host, got, want)
		}
	}
}

// TestHostImpliesServerMode_CanonicalSpellings pins mode inference to the
// classifier: one address infers one mode however it is spelled.
func TestHostImpliesServerMode_CanonicalSpellings(t *testing.T) {
	for _, tc := range []struct {
		host string
		want bool
	}{
		{"::1", false},
		{"0:0:0:0:0:0:0:1", false},
		{"[::1]", false},
		{"LOCALHOST", false},
		{"::ffff:127.0.0.1", false},
		{"127.0.0.2", true},
		{"db.example.com", true},
	} {
		t.Run(tc.host, func(t *testing.T) {
			t.Setenv("BEADS_DOLT_SERVER_HOST", tc.host)
			if got := (&Config{}).HostImpliesServerMode(); got != tc.want {
				t.Errorf("HostImpliesServerMode() with BEADS_DOLT_SERVER_HOST=%q = %v, want %v", tc.host, got, tc.want)
			}
		})
	}
}

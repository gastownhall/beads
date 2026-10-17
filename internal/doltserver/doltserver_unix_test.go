//go:build !windows

package doltserver

import (
	"reflect"
	"testing"
)

func TestParseDoltProcessPIDs(t *testing.T) {
	tests := []struct {
		name     string
		snapshot string
		want     []int
	}{
		{
			name:     "ordinary command",
			snapshot: "  101 S /usr/local/bin/dolt sql-server --port 3306\n",
			want:     []int{101},
		},
		{
			name:     "profiled command",
			snapshot: "102 Sl dolt --prof cpu --prof-path /tmp/dolt-pprof sql-server\n",
			want:     []int{102},
		},
		{
			name:     "loose substring false positive is rejected",
			snapshot: "103 S some-tool says dolt then sql-server\n",
		},
		{
			name:     "executable basename must be exactly dolt",
			snapshot: "116 S /opt/dolt-tools/bin/dolt-helper sql-server\n",
		},
		{
			name:     "dolt config path is not a sql-server argument",
			snapshot: "117 S /usr/bin/cat /etc/dolt/sql-server.yaml\n",
		},
		{
			name: "empty state column keeps the command",
			snapshot: "   1466      /Users/u/.nix-profile/bin/dolt sql-server --config /Users/u/.beads/shared-server/dolt-server-config.yaml\n" +
				"1467 dolt sql-server\n" +
				"1468 /usr/bin/python3 -c dolt sql-server\n",
			want: []int{1466, 1467},
		},
		{
			name:     "empty state with profiled command",
			snapshot: "1469 /Users/u/.nix-profile/bin/dolt --prof cpu sql-server\n",
			want:     []int{1469},
		},
		{
			name:     "linux tracing-stop state t keeps a live server",
			snapshot: "120 t dolt sql-server\n121 tl dolt sql-server\n",
			want:     []int{120, 121},
		},
		{
			name:     "historical and freebsd primary states L and W keep a live server",
			snapshot: "122 L dolt sql-server\n123 W dolt sql-server\n124 Ls+ dolt sql-server\n",
			want:     []int{122, 123, 124},
		},
		{
			name:     "freebsd capability and jail modifiers keep a live server",
			snapshot: "125 SJ dolt sql-server\n126 SC+ dolt sql-server\n127 Ss+CJ dolt sql-server\n",
			want:     []int{125, 126, 127},
		},
		{
			name:     "sql server before dolt is rejected",
			snapshot: "104 S sql-server then dolt\n",
		},
		{
			name:     "case sensitive command matching",
			snapshot: "105 S Dolt sql-server\n106 S dolt SQL-SERVER\n",
		},
		{
			name:     "zombie and dead states including modifiers are rejected",
			snapshot: "107 Z dolt sql-server\n108 Z+ dolt sql-server\n109 X dolt sql-server\n110 X< dolt sql-server\n",
		},
		{
			name:     "valid state prefix with modifier is accepted",
			snapshot: "111 S+ dolt sql-server\n",
			want:     []int{111},
		},
		{
			name: "malformed and invalid rows are rejected",
			snapshot: "\n" +
				"not-a-pid S dolt sql-server\n" +
				"0 S dolt sql-server\n" +
				"-1 S dolt sql-server\n" +
				"112\n" +
				"113 S\n" +
				"115 S \n",
		},
		{
			name:     "preserves source order",
			snapshot: "202 S dolt sql-server\n201 S dolt sql-server\n203 R dolt sql-server\n",
			want:     []int{202, 201, 203},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseDoltProcessPIDs([]byte(tt.snapshot)); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseDoltProcessPIDs() = %v, want %v", got, tt.want)
			}
		})
	}
}

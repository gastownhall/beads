package wire

import "testing"

func TestExtractPageParentageFromText(t *testing.T) {
	info := ExtractPageParentageFromText(`<parent-data-source url="collection://6e35ad8d-edea-443d-ad83-890f14a253a2" name="Beads Issues"></parent-data-source><ancestor-2-database url="https://www.notion.so/7bc8b700c54445d6ae49c1174d5347e4" title="Beads Issues"></ancestor-2-database>`)
	if got := info.DataSourceID; got != "6e35ad8d-edea-443d-ad83-890f14a253a2" {
		t.Fatalf("data_source_id = %q", got)
	}
	if got := info.DatabaseID; got != "7bc8b700-c544-45d6-ae49-c1174d5347e4" {
		t.Fatalf("database_id = %q", got)
	}
}

func TestMatchesTargetDatabase(t *testing.T) {
	tests := []struct {
		name   string
		actual DatabaseInfo
		target DatabaseInfo
		want   bool
	}{
		{
			name:   "matching data source and database",
			actual: DatabaseInfo{DatabaseID: "db-1", DataSourceID: "ds-1"},
			target: DatabaseInfo{DatabaseID: "db-1", DataSourceID: "ds-1"},
			want:   true,
		},
		{
			name:   "mismatched data source",
			actual: DatabaseInfo{DatabaseID: "db-1", DataSourceID: "ds-2"},
			target: DatabaseInfo{DatabaseID: "db-1", DataSourceID: "ds-1"},
			want:   false,
		},
		{
			name:   "matching database without data source",
			actual: DatabaseInfo{DatabaseID: "db-1"},
			target: DatabaseInfo{DatabaseID: "db-1", DataSourceID: "ds-1"},
			want:   true,
		},
		{
			name:   "no comparable ids",
			actual: DatabaseInfo{},
			target: DatabaseInfo{DatabaseID: "db-1", DataSourceID: "ds-1"},
			want:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MatchesTargetDatabase(tt.actual, tt.target); got != tt.want {
				t.Fatalf("MatchesTargetDatabase() = %v, want %v", got, tt.want)
			}
		})
	}
}

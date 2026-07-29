package schema_test

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/schema"
)

func TestLatestVersionIsPositive(t *testing.T) {
	if v := schema.LatestVersion(); v <= 0 {
		t.Fatalf("LatestVersion() = %d, want > 0", v)
	}
}

func TestIsSchemaSkewErrorDetectsWrappedError(t *testing.T) {
	skew := &schema.SchemaSkewError{DBVersion: 9, BinaryVersion: 3}
	wrapped := fmt.Errorf("connect: %w", skew)
	if !schema.IsSchemaSkewError(wrapped) {
		t.Fatal("IsSchemaSkewError(wrapped SchemaSkewError) = false, want true")
	}
	if schema.IsSchemaSkewError(fmt.Errorf("plain error")) {
		t.Fatal("IsSchemaSkewError(plain error) = true, want false")
	}
}

func TestDBConnSatisfiedByStdlibTypes(t *testing.T) {
	var (
		_ schema.DBConn = (*sql.DB)(nil)
		_ schema.DBConn = (*sql.Tx)(nil)
		_ schema.DBConn = (*sql.Conn)(nil)
	)
}

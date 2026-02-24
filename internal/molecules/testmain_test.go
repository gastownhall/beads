package molecules

import (
	"fmt"
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/testutil"
)

func TestMain(m *testing.M) {
	os.Exit(testMainInner(m))
}

func testMainInner(m *testing.M) int {
	srv, cleanup := testutil.StartTestDoltServer("molecules-pkg-test-*")
	defer cleanup()

	if srv != nil {
		os.Setenv("BEADS_DOLT_PORT", fmt.Sprintf("%d", srv.Port))
		os.Setenv("BEADS_TEST_MODE", "1")
	}

	code := m.Run()

	os.Unsetenv("BEADS_DOLT_PORT")
	os.Unsetenv("BEADS_TEST_MODE")
	return code
}

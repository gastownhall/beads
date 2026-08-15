package db

import (
	"context"
	"database/sql"
	"math/rand"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/suite"

	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/testutil"
)

type testSuite struct {
	suite.Suite
	db             *sql.DB
	dbName         string
	baselineCommit string
	eventsDDL      string
	journalEnabled bool
}

func (s *testSuite) SetupSuite() {
	testutil.RequireDoltContainer(s.T())

	port := testutil.DoltContainerPortInt()
	s.Require().NotZero(port, "test container port must be set")

	ctx := context.Background()

	rootDSN := doltutil.ServerDSN{Host: "127.0.0.1", Port: port, User: "root"}.String()
	root, err := sql.Open("mysql", rootDSN)
	s.Require().NoError(err)
	defer root.Close()

	s.dbName = "beads_domain_db_test_" + randomSuffix(8)
	_, err = root.ExecContext(ctx, "CREATE DATABASE `"+s.dbName+"`")
	s.Require().NoError(err)

	dsn := doltutil.ServerDSN{Host: "127.0.0.1", Port: port, User: "root", Database: s.dbName}.String()
	db, err := sql.Open("mysql", dsn)
	s.Require().NoError(err)
	s.Require().NoError(db.PingContext(ctx))
	s.db = db

	_, err = schema.MigrateUp(ctx, db)
	s.Require().NoError(err, "applying beads schema")

	_, err = db.ExecContext(ctx, "CALL DOLT_ADD('-A')")
	s.Require().NoError(err, "dolt add baseline")
	_, err = db.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?, '--allow-empty')", "beads domain/db test baseline")
	s.Require().NoError(err, "dolt commit baseline")
	s.Require().NoError(
		db.QueryRowContext(ctx, "SELECT HASHOF('HEAD')").Scan(&s.baselineCommit),
		"capture baseline commit hash",
	)

	// events is dolt_ignored since 0062 (bd-red8u): it lives only in the
	// working set, so the baseline commit above does not contain it and the
	// per-test DOLT_RESET below swaps out the issues table it references —
	// after which the surviving untracked table's fk_events_issue silently
	// stops enforcing (verified on dolt-sql-server 2.2.0). Capture the DDL so
	// SetupTest can recreate the table fresh against the reset root, which
	// re-links the FK and clears any audit rows orphaned by the reset.
	var tbl string
	s.Require().NoError(
		db.QueryRowContext(ctx, "SHOW CREATE TABLE events").Scan(&tbl, &s.eventsDDL),
		"capture events DDL",
	)
}

func (s *testSuite) TearDownSuite() {
	if s.db != nil {
		_ = s.db.Close()
		s.db = nil
	}
	if s.dbName == "" {
		return
	}
	port := testutil.DoltContainerPortInt()
	if port == 0 {
		return
	}
	rootDSN := doltutil.ServerDSN{Host: "127.0.0.1", Port: port, User: "root"}.String()
	root, err := sql.Open("mysql", rootDSN)
	if err != nil {
		return
	}
	defer root.Close()
	_, _ = root.ExecContext(context.Background(), "DROP DATABASE IF EXISTS `"+s.dbName+"`")
}

func (s *testSuite) SetupTest() {
	s.journalEnabled = false
	ctx := context.Background()
	_, err := s.db.ExecContext(ctx, "CALL DOLT_RESET('--hard', ?)", s.baselineCommit)
	s.Require().NoError(err, "reset to baseline %s", s.baselineCommit)
	// Recreate the working-set-only events table after the reset — see the
	// eventsDDL capture in SetupSuite for why.
	_, err = s.db.ExecContext(ctx, "DROP TABLE IF EXISTS events")
	s.Require().NoError(err, "drop events after reset")
	_, err = s.db.ExecContext(ctx, s.eventsDDL)
	s.Require().NoError(err, "recreate events after reset")
}

func (s *testSuite) Runner() Runner {
	return s.db
}

func (s *testSuite) Ctx() context.Context {
	return issueops.WithEventsJournal(context.Background(), s.journalEnabled)
}

var suffixLetters = []rune("abcdefghijklmnopqrstuvwxyz")

func randomSuffix(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = suffixLetters[rand.Intn(len(suffixLetters))]
	}
	return string(b)
}

func TestDomainDB(t *testing.T) {
	suite.Run(t, &testSuite{})
}

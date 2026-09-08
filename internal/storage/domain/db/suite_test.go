package db

import (
	"context"
	"database/sql"
	"math/rand"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/suite"

	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/testutil"
)

type testSuite struct {
	suite.Suite
	db             *sql.DB
	dbName         string
	baselineCommit string
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
	_, err := s.db.ExecContext(context.Background(), "CALL DOLT_RESET('--hard', ?)", s.baselineCommit)
	s.Require().NoError(err, "reset to baseline %s", s.baselineCommit)
}

func (s *testSuite) Runner() Runner {
	return s.db
}

func (s *testSuite) Ctx() context.Context {
	return context.Background()
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

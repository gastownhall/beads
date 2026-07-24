package fs

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage/domain"
)

type testSuite struct {
	suite.Suite
	origDir  string
	suiteDir string
}

func (s *testSuite) SetupSuite() {
	origDir, err := os.Getwd()
	s.Require().NoError(err, "SetupSuite: get working directory")
	s.origDir = origDir

	suiteDir, err := os.MkdirTemp("", "beads-fs-suite-*")
	s.Require().NoError(err, "SetupSuite: create temp dir")
	s.suiteDir = suiteDir

	s.Require().NoError(os.Chdir(suiteDir), "SetupSuite: chdir to non-git dir")
	git.ResetCaches()
}

func (s *testSuite) TearDownSuite() {
	if s.origDir != "" {
		_ = os.Chdir(s.origDir)
		git.ResetCaches()
	}
	if s.suiteDir != "" {
		_ = os.RemoveAll(s.suiteDir)
	}
}

func (s *testSuite) Ctx() context.Context {
	return context.Background()
}

func (s *testSuite) newRepo() (workDir, beadsDir string, repo domain.BeadsDirFSRepository) {
	s.T().Helper()
	s.T().Setenv("BEADS_DIR", "")
	workDir = s.T().TempDir()
	beadsDir = filepath.Join(workDir, ".beads")
	repo = NewBeadsDirFSRepository(workDir, testTemplates())
	return
}

func testTemplates() domain.BeadsDirTemplates {
	return domain.BeadsDirTemplates{
		BeadsGitignore:           "# test beads gitignore\ndolt/\n",
		ProjectGitignoreHeader:   "# Beads test header",
		ProjectGitignorePatterns: []string{".dolt/", "*.db"},
		Readme:                   "# test readme\n",
	}
}

func TestDomainFS(t *testing.T) {
	suite.Run(t, &testSuite{})
}

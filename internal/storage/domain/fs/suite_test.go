package fs

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/steveyegge/beads/internal/storage/domain"
)

type testSuite struct {
	suite.Suite
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

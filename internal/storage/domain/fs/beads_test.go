package fs

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/utils"
)

func (s *testSuite) TestBeadsDirFSRepository() {
	s.Run("CreateBeadsDir", func() {
		s.Run("CreatesDirectoryWithPerms", s.createBeadsDirCreates)
		s.Run("IdempotentOnExisting", s.createBeadsDirIdempotent)
	})
	s.Run("BeadsDirExists", func() {
		s.Run("MissingReturnsFalse", s.beadsDirExistsMissing)
		s.Run("PresentReturnsTrue", s.beadsDirExistsPresent)
		s.Run("FileNotDirReturnsFalse", s.beadsDirExistsIsFile)
	})
	s.Run("WriteBeadsGitignore", func() {
		s.Run("WritesTemplate", s.writeBeadsGitignoreWrites)
		s.Run("IdempotentOnMatchingContent", s.writeBeadsGitignoreIdempotent)
		s.Run("OverwritesDifferingContent", s.writeBeadsGitignoreOverwrites)
	})
	s.Run("BeadsGitignoreExists", func() {
		s.Run("MissingReturnsFalse", s.beadsGitignoreExistsMissing)
		s.Run("PresentReturnsTrue", s.beadsGitignoreExistsPresent)
	})
	s.Run("WriteProjectGitignore", func() {
		s.Run("CreatesWithHeaderAndPatterns", s.writeProjectGitignoreCreates)
		s.Run("AppendsToExisting", s.writeProjectGitignoreAppends)
		s.Run("SkipsAlreadyPresentPatterns", s.writeProjectGitignoreNoDuplicates)
		s.Run("NoChangeWhenAllPresent", s.writeProjectGitignoreNoOpWhenComplete)
		s.Run("AddsLeadingNewlineWhenMissing", s.writeProjectGitignoreFixesTrailingNewline)
	})
	s.Run("ProjectGitignoreExists", func() {
		s.Run("MissingReturnsFalse", s.projectGitignoreExistsMissing)
		s.Run("PresentReturnsTrue", s.projectGitignoreExistsPresent)
	})
	s.Run("WriteInteractionsLog", func() {
		s.Run("CreatesEmptyFile", s.writeInteractionsLogCreates)
		s.Run("PreservesExisting", s.writeInteractionsLogPreserves)
	})
	s.Run("WriteReadme", func() {
		s.Run("CreatesFromTemplate", s.writeReadmeCreates)
		s.Run("PreservesExisting", s.writeReadmePreserves)
	})
	s.Run("MetadataJSON", func() {
		s.Run("ReadMissingReturnsNil", s.readMetadataJSONMissing)
		s.Run("WriteThenReadRoundTrip", s.metadataJSONRoundTrip)
		s.Run("WriteOverwrites", s.metadataJSONOverwrite)
	})
	s.Run("ConfigYAML", func() {
		s.Run("ReadMissingReturnsNil", s.readConfigYAMLMissing)
		s.Run("WriteThenReadRoundTrip", s.configYAMLRoundTrip)
		s.Run("WriteOverwrites", s.configYAMLOverwrite)
	})
	s.Run("ReadBeadsConfig", func() {
		s.Run("MissingFileReturnsNil", s.readBeadsConfigMissing)
		s.Run("ParsesMetadataJSON", s.readBeadsConfigParses)
	})
	s.Run("ResolveBeadsDirPath", func() {
		s.Run("HonorsBeadsDirEnv", s.resolveBeadsDirPathHonorsEnv)
		s.Run("FallsBackToLocalDotBeads", s.resolveBeadsDirPathLocalFallback)
	})
	s.Run("BeadsDirIsLocal", func() {
		s.Run("TrueForWorkDirChild", s.beadsDirIsLocalTrue)
		s.Run("FalseForExplicitEnv", s.beadsDirIsLocalFalseEnv)
	})
}

func (s *testSuite) createBeadsDirCreates() {
	_, beadsDir, repo := s.newRepo()
	s.Require().NoError(repo.CreateBeadsDir(s.Ctx()))

	info, err := os.Stat(beadsDir)
	s.Require().NoError(err)
	s.True(info.IsDir())

	if runtime.GOOS != "windows" {
		s.Equal(config.BeadsDirPerm, info.Mode().Perm())
	}
}

func (s *testSuite) createBeadsDirIdempotent() {
	_, _, repo := s.newRepo()
	s.Require().NoError(repo.CreateBeadsDir(s.Ctx()))
	s.Require().NoError(repo.CreateBeadsDir(s.Ctx()))
}

func (s *testSuite) beadsDirExistsMissing() {
	_, _, repo := s.newRepo()
	exists, err := repo.BeadsDirExists(s.Ctx())
	s.Require().NoError(err)
	s.False(exists)
}

func (s *testSuite) beadsDirExistsPresent() {
	_, beadsDir, repo := s.newRepo()
	s.Require().NoError(os.MkdirAll(beadsDir, 0700))
	exists, err := repo.BeadsDirExists(s.Ctx())
	s.Require().NoError(err)
	s.True(exists)
}

func (s *testSuite) beadsDirExistsIsFile() {
	_, beadsDir, repo := s.newRepo()
	s.Require().NoError(os.WriteFile(beadsDir, []byte("not a dir"), 0600))
	exists, err := repo.BeadsDirExists(s.Ctx())
	s.Require().NoError(err)
	s.False(exists)
}

func (s *testSuite) writeBeadsGitignoreWrites() {
	_, beadsDir, repo := s.newRepo()
	s.Require().NoError(os.MkdirAll(beadsDir, 0700))
	s.Require().NoError(repo.WriteBeadsGitignore(s.Ctx()))

	data, err := os.ReadFile(filepath.Join(beadsDir, ".gitignore"))
	s.Require().NoError(err)
	s.Equal(testTemplates().BeadsGitignore, string(data))
}

func (s *testSuite) writeBeadsGitignoreIdempotent() {
	_, beadsDir, repo := s.newRepo()
	s.Require().NoError(os.MkdirAll(beadsDir, 0700))
	path := filepath.Join(beadsDir, ".gitignore")
	s.Require().NoError(os.WriteFile(path, []byte(testTemplates().BeadsGitignore), 0600))

	before, err := os.Stat(path)
	s.Require().NoError(err)

	s.Require().NoError(repo.WriteBeadsGitignore(s.Ctx()))

	after, err := os.Stat(path)
	s.Require().NoError(err)
	s.Equal(before.ModTime(), after.ModTime(), "file should not be rewritten when content already matches")
}

func (s *testSuite) writeBeadsGitignoreOverwrites() {
	_, beadsDir, repo := s.newRepo()
	s.Require().NoError(os.MkdirAll(beadsDir, 0700))
	path := filepath.Join(beadsDir, ".gitignore")
	s.Require().NoError(os.WriteFile(path, []byte("stale\n"), 0600))

	s.Require().NoError(repo.WriteBeadsGitignore(s.Ctx()))

	data, err := os.ReadFile(path)
	s.Require().NoError(err)
	s.Equal(testTemplates().BeadsGitignore, string(data))
}

func (s *testSuite) beadsGitignoreExistsMissing() {
	_, beadsDir, repo := s.newRepo()
	s.Require().NoError(os.MkdirAll(beadsDir, 0700))
	exists, err := repo.BeadsGitignoreExists(s.Ctx())
	s.Require().NoError(err)
	s.False(exists)
}

func (s *testSuite) beadsGitignoreExistsPresent() {
	_, beadsDir, repo := s.newRepo()
	s.Require().NoError(os.MkdirAll(beadsDir, 0700))
	s.Require().NoError(os.WriteFile(filepath.Join(beadsDir, ".gitignore"), []byte("x"), 0600))
	exists, err := repo.BeadsGitignoreExists(s.Ctx())
	s.Require().NoError(err)
	s.True(exists)
}

func (s *testSuite) writeProjectGitignoreCreates() {
	workDir, _, repo := s.newRepo()
	s.Require().NoError(repo.WriteProjectGitignore(s.Ctx()))

	data, err := os.ReadFile(filepath.Join(workDir, ".gitignore"))
	s.Require().NoError(err)
	body := string(data)

	tpl := testTemplates()
	s.Contains(body, tpl.ProjectGitignoreHeader)
	for _, pattern := range tpl.ProjectGitignorePatterns {
		s.Contains(body, pattern)
	}
}

func (s *testSuite) writeProjectGitignoreAppends() {
	workDir, _, repo := s.newRepo()
	path := filepath.Join(workDir, ".gitignore")
	preexisting := "node_modules/\n*.log\n"
	s.Require().NoError(os.WriteFile(path, []byte(preexisting), 0644))

	s.Require().NoError(repo.WriteProjectGitignore(s.Ctx()))

	data, err := os.ReadFile(path)
	s.Require().NoError(err)
	body := string(data)

	tpl := testTemplates()
	s.True(strings.HasPrefix(body, preexisting), "preexisting content must be preserved at the top")
	s.Contains(body, tpl.ProjectGitignoreHeader)
	for _, pattern := range tpl.ProjectGitignorePatterns {
		s.Contains(body, pattern)
	}
}

func (s *testSuite) writeProjectGitignoreNoDuplicates() {
	workDir, _, repo := s.newRepo()
	path := filepath.Join(workDir, ".gitignore")
	preexisting := ".dolt/\nnode_modules/\n"
	s.Require().NoError(os.WriteFile(path, []byte(preexisting), 0644))

	s.Require().NoError(repo.WriteProjectGitignore(s.Ctx()))

	data, err := os.ReadFile(path)
	s.Require().NoError(err)

	s.Equal(1, strings.Count(string(data), ".dolt/\n"), "must not duplicate existing patterns")
}

func (s *testSuite) writeProjectGitignoreNoOpWhenComplete() {
	workDir, _, repo := s.newRepo()
	path := filepath.Join(workDir, ".gitignore")
	tpl := testTemplates()
	var buf bytes.Buffer
	buf.WriteString("existing\n")
	buf.WriteString(tpl.ProjectGitignoreHeader + "\n")
	for _, pattern := range tpl.ProjectGitignorePatterns {
		buf.WriteString(pattern + "\n")
	}
	s.Require().NoError(os.WriteFile(path, buf.Bytes(), 0644))

	before, err := os.Stat(path)
	s.Require().NoError(err)

	s.Require().NoError(repo.WriteProjectGitignore(s.Ctx()))

	after, err := os.Stat(path)
	s.Require().NoError(err)
	s.Equal(before.ModTime(), after.ModTime(), "must not rewrite when all patterns already present")
}

func (s *testSuite) writeProjectGitignoreFixesTrailingNewline() {
	workDir, _, repo := s.newRepo()
	path := filepath.Join(workDir, ".gitignore")
	s.Require().NoError(os.WriteFile(path, []byte("no-trailing-newline"), 0644))

	s.Require().NoError(repo.WriteProjectGitignore(s.Ctx()))

	data, err := os.ReadFile(path)
	s.Require().NoError(err)
	body := string(data)

	s.True(strings.HasPrefix(body, "no-trailing-newline\n"), "trailing newline must be inserted before appended content")
	s.Contains(body, testTemplates().ProjectGitignoreHeader)
}

func (s *testSuite) projectGitignoreExistsMissing() {
	_, _, repo := s.newRepo()
	exists, err := repo.ProjectGitignoreExists(s.Ctx())
	s.Require().NoError(err)
	s.False(exists)
}

func (s *testSuite) projectGitignoreExistsPresent() {
	workDir, _, repo := s.newRepo()
	s.Require().NoError(os.WriteFile(filepath.Join(workDir, ".gitignore"), []byte("x"), 0644))
	exists, err := repo.ProjectGitignoreExists(s.Ctx())
	s.Require().NoError(err)
	s.True(exists)
}

func (s *testSuite) writeInteractionsLogCreates() {
	_, beadsDir, repo := s.newRepo()
	s.Require().NoError(os.MkdirAll(beadsDir, 0700))
	s.Require().NoError(repo.WriteInteractionsLog(s.Ctx()))

	data, err := os.ReadFile(filepath.Join(beadsDir, "interactions.jsonl"))
	s.Require().NoError(err)
	s.Empty(data)
}

func (s *testSuite) writeInteractionsLogPreserves() {
	_, beadsDir, repo := s.newRepo()
	s.Require().NoError(os.MkdirAll(beadsDir, 0700))
	path := filepath.Join(beadsDir, "interactions.jsonl")
	existing := []byte(`{"event":"x"}` + "\n")
	s.Require().NoError(os.WriteFile(path, existing, 0644))

	s.Require().NoError(repo.WriteInteractionsLog(s.Ctx()))

	data, err := os.ReadFile(path)
	s.Require().NoError(err)
	s.Equal(existing, data)
}

func (s *testSuite) writeReadmeCreates() {
	_, beadsDir, repo := s.newRepo()
	s.Require().NoError(os.MkdirAll(beadsDir, 0700))
	s.Require().NoError(repo.WriteReadme(s.Ctx()))

	data, err := os.ReadFile(filepath.Join(beadsDir, "README.md"))
	s.Require().NoError(err)
	s.Equal(testTemplates().Readme, string(data))
}

func (s *testSuite) writeReadmePreserves() {
	_, beadsDir, repo := s.newRepo()
	s.Require().NoError(os.MkdirAll(beadsDir, 0700))
	path := filepath.Join(beadsDir, "README.md")
	custom := []byte("# My custom readme\n")
	s.Require().NoError(os.WriteFile(path, custom, 0644))

	s.Require().NoError(repo.WriteReadme(s.Ctx()))

	data, err := os.ReadFile(path)
	s.Require().NoError(err)
	s.Equal(custom, data)
}

func (s *testSuite) readMetadataJSONMissing() {
	_, beadsDir, repo := s.newRepo()
	s.Require().NoError(os.MkdirAll(beadsDir, 0700))
	data, err := repo.ReadMetadataJSON(s.Ctx())
	s.Require().NoError(err)
	s.Nil(data)
}

func (s *testSuite) metadataJSONRoundTrip() {
	_, beadsDir, repo := s.newRepo()
	s.Require().NoError(os.MkdirAll(beadsDir, 0700))
	body := []byte(`{"_project_id":"abc-123"}`)

	s.Require().NoError(repo.WriteMetadataJSON(s.Ctx(), body))

	got, err := repo.ReadMetadataJSON(s.Ctx())
	s.Require().NoError(err)
	s.Equal(body, got)
}

func (s *testSuite) metadataJSONOverwrite() {
	_, beadsDir, repo := s.newRepo()
	s.Require().NoError(os.MkdirAll(beadsDir, 0700))
	s.Require().NoError(repo.WriteMetadataJSON(s.Ctx(), []byte(`{"v":1}`)))
	s.Require().NoError(repo.WriteMetadataJSON(s.Ctx(), []byte(`{"v":2}`)))

	got, err := repo.ReadMetadataJSON(s.Ctx())
	s.Require().NoError(err)
	s.Equal([]byte(`{"v":2}`), got)
}

func (s *testSuite) readConfigYAMLMissing() {
	_, beadsDir, repo := s.newRepo()
	s.Require().NoError(os.MkdirAll(beadsDir, 0700))
	data, err := repo.ReadConfigYAML(s.Ctx())
	s.Require().NoError(err)
	s.Nil(data)
}

func (s *testSuite) configYAMLRoundTrip() {
	_, beadsDir, repo := s.newRepo()
	s.Require().NoError(os.MkdirAll(beadsDir, 0700))
	body := []byte("issue_prefix: bd\n")

	s.Require().NoError(repo.WriteConfigYAML(s.Ctx(), body))

	got, err := repo.ReadConfigYAML(s.Ctx())
	s.Require().NoError(err)
	s.Equal(body, got)
}

func (s *testSuite) configYAMLOverwrite() {
	_, beadsDir, repo := s.newRepo()
	s.Require().NoError(os.MkdirAll(beadsDir, 0700))
	s.Require().NoError(repo.WriteConfigYAML(s.Ctx(), []byte("v: 1\n")))
	s.Require().NoError(repo.WriteConfigYAML(s.Ctx(), []byte("v: 2\n")))

	got, err := repo.ReadConfigYAML(s.Ctx())
	s.Require().NoError(err)
	s.Equal([]byte("v: 2\n"), got)
}

func (s *testSuite) readBeadsConfigMissing() {
	_, beadsDir, repo := s.newRepo()
	s.Require().NoError(os.MkdirAll(beadsDir, 0700))
	cfg, err := repo.ReadBeadsConfig(s.Ctx())
	s.Require().NoError(err)
	s.Nil(cfg)
}

func (s *testSuite) readBeadsConfigParses() {
	_, beadsDir, repo := s.newRepo()
	s.Require().NoError(os.MkdirAll(beadsDir, 0700))
	body := []byte(`{"dolt_database":"my_db","project_id":"pid-123"}`)
	s.Require().NoError(repo.WriteMetadataJSON(s.Ctx(), body))

	cfg, err := repo.ReadBeadsConfig(s.Ctx())
	s.Require().NoError(err)
	s.Require().NotNil(cfg)
	s.Equal("my_db", cfg.DoltDatabase)
	s.Equal("pid-123", cfg.ProjectID)
}

func (s *testSuite) resolveBeadsDirPathHonorsEnv() {
	envDir := s.T().TempDir()
	s.T().Setenv("BEADS_DIR", envDir)

	workDir := s.T().TempDir()
	repo := NewBeadsDirFSRepository(workDir, testTemplates())

	res := repo.ResolveBeadsDirPath(s.Ctx())
	s.True(res.HasExplicit)
	s.NotEmpty(res.BeadsDir)
}

func (s *testSuite) resolveBeadsDirPathLocalFallback() {
	workDir, _, repo := s.newRepo()

	res := repo.ResolveBeadsDirPath(s.Ctx())
	s.False(res.HasExplicit)
	// When BEADS_DIR is unset and we're outside a git worktree, the fallback is
	// workDir/.beads (FollowRedirect leaves the path unchanged when no redirect).
	s.Equal(filepath.Join(utils.CanonicalizePath(workDir), ".beads"), res.BeadsDir)
}

func (s *testSuite) beadsDirIsLocalTrue() {
	_, _, repo := s.newRepo()
	s.True(repo.BeadsDirIsLocal(s.Ctx()))
}

func (s *testSuite) beadsDirIsLocalFalseEnv() {
	envDir := s.T().TempDir()
	s.T().Setenv("BEADS_DIR", envDir)

	workDir := s.T().TempDir()
	repo := NewBeadsDirFSRepository(workDir, testTemplates())

	s.False(repo.BeadsDirIsLocal(s.Ctx()), "explicit BEADS_DIR outside workDir is not local")
}

// statically assert the BeadsDirTemplates type is reachable for downstream callers.
var _ = domain.BeadsDirTemplates{}

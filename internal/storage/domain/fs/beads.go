package fs

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/utils"
)

func NewBeadsDirFSRepository(workDir string, templates domain.BeadsDirTemplates) domain.BeadsDirFSRepository {
	beadsDir, hasExplicit := resolveBeadsDir(workDir)
	return &beadsDirFSRepositoryImpl{
		workDir:     workDir,
		beadsDir:    beadsDir,
		hasExplicit: hasExplicit,
		templates:   templates,
	}
}

type beadsDirFSRepositoryImpl struct {
	workDir     string
	beadsDir    string
	hasExplicit bool
	templates   domain.BeadsDirTemplates
}

var _ domain.BeadsDirFSRepository = (*beadsDirFSRepositoryImpl)(nil)

func resolveBeadsDir(workDir string) (string, bool) {
	if envBeadsDir := os.Getenv("BEADS_DIR"); envBeadsDir != "" {
		return utils.CanonicalizePath(envBeadsDir), true
	}
	return beads.ResolveBeadsDirForRepo(workDir), false
}

func (r *beadsDirFSRepositoryImpl) ResolveBeadsDirPath(ctx context.Context) domain.BeadsDirResolution {
	return domain.BeadsDirResolution{BeadsDir: r.beadsDir, HasExplicit: r.hasExplicit}
}

func (r *beadsDirFSRepositoryImpl) BeadsDirIsLocal(ctx context.Context) bool {
	workDir := filepath.Clean(utils.CanonicalizePath(r.workDir))
	beadsDir := filepath.Clean(utils.CanonicalizePath(r.beadsDir))
	if beadsDir == workDir {
		return true
	}
	rel, err := filepath.Rel(workDir, beadsDir)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (r *beadsDirFSRepositoryImpl) CreateBeadsDir(ctx context.Context) error {
	if r.beadsDir == "" {
		return fmt.Errorf("fs: CreateBeadsDir: beadsDir not resolved")
	}
	if err := os.MkdirAll(r.beadsDir, config.BeadsDirPerm); err != nil {
		return fmt.Errorf("fs: CreateBeadsDir: mkdir %s: %w", r.beadsDir, err)
	}
	if _, err := config.FixBeadsDirPermissions(r.beadsDir); err != nil {
		return fmt.Errorf("fs: CreateBeadsDir: fix perms %s: %w", r.beadsDir, err)
	}
	return nil
}

func (r *beadsDirFSRepositoryImpl) BeadsDirExists(ctx context.Context) (bool, error) {
	info, err := os.Stat(r.beadsDir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("fs: BeadsDirExists: stat %s: %w", r.beadsDir, err)
	}
	return info.IsDir(), nil
}

func (r *beadsDirFSRepositoryImpl) WriteBeadsGitignore(ctx context.Context) error {
	if r.templates.BeadsGitignore == "" {
		return fmt.Errorf("fs: WriteBeadsGitignore: template not configured")
	}
	path := filepath.Join(r.beadsDir, ".gitignore")
	body := []byte(r.templates.BeadsGitignore)
	// #nosec G304 -- path joined under bound beadsDir
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, body) {
		return nil
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		return fmt.Errorf("fs: WriteBeadsGitignore: %w", err)
	}
	return nil
}

func (r *beadsDirFSRepositoryImpl) BeadsGitignoreExists(ctx context.Context) (bool, error) {
	return fileExists(filepath.Join(r.beadsDir, ".gitignore"), "fs: BeadsGitignoreExists")
}

func (r *beadsDirFSRepositoryImpl) WriteProjectGitignore(ctx context.Context) error {
	if r.workDir == "" {
		return fmt.Errorf("fs: WriteProjectGitignore: workDir not set")
	}
	if len(r.templates.ProjectGitignorePatterns) == 0 {
		return fmt.Errorf("fs: WriteProjectGitignore: patterns not configured")
	}
	path := filepath.Join(r.workDir, ".gitignore")
	// #nosec G304 -- path joined under bound workDir
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("fs: WriteProjectGitignore: read: %w", err)
	}

	var toAdd []string
	for _, pattern := range r.templates.ProjectGitignorePatterns {
		if !containsLine(existing, pattern) {
			toAdd = append(toAdd, pattern)
		}
	}
	if len(toAdd) == 0 {
		return nil
	}

	var buf bytes.Buffer
	buf.Write(existing)
	if len(existing) > 0 && !bytes.HasSuffix(existing, []byte("\n")) {
		buf.WriteByte('\n')
	}
	if header := r.templates.ProjectGitignoreHeader; header != "" && !containsLine(existing, header) {
		if len(existing) > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString(header + "\n")
	}
	for _, pattern := range toAdd {
		buf.WriteString(pattern + "\n")
	}

	// #nosec G306 -- .gitignore must be world-readable so users can read/edit it
	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("fs: WriteProjectGitignore: write: %w", err)
	}
	return nil
}

func (r *beadsDirFSRepositoryImpl) ProjectGitignoreExists(ctx context.Context) (bool, error) {
	return fileExists(filepath.Join(r.workDir, ".gitignore"), "fs: ProjectGitignoreExists")
}

func (r *beadsDirFSRepositoryImpl) WriteInteractionsLog(ctx context.Context) error {
	path := filepath.Join(r.beadsDir, "interactions.jsonl")
	switch _, err := os.Stat(path); {
	case err == nil:
		return nil
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("fs: WriteInteractionsLog: stat: %w", err)
	}
	// #nosec G306 -- interactions log is consumed by user tooling
	if err := os.WriteFile(path, []byte{}, 0644); err != nil {
		return fmt.Errorf("fs: WriteInteractionsLog: write: %w", err)
	}
	return nil
}

func (r *beadsDirFSRepositoryImpl) WriteReadme(ctx context.Context) error {
	if r.templates.Readme == "" {
		return fmt.Errorf("fs: WriteReadme: template not configured")
	}
	path := filepath.Join(r.beadsDir, "README.md")
	if _, err := os.Stat(path); err == nil {
		return nil // preserve any user edits
	}
	// #nosec G306 -- README should be world-readable
	if err := os.WriteFile(path, []byte(r.templates.Readme), 0644); err != nil {
		return fmt.Errorf("fs: WriteReadme: %w", err)
	}
	return nil
}

func (r *beadsDirFSRepositoryImpl) WriteMetadataJSON(ctx context.Context, content []byte) error {
	path := filepath.Join(r.beadsDir, "metadata.json")
	if err := os.WriteFile(path, content, 0600); err != nil {
		return fmt.Errorf("fs: WriteMetadataJSON: %w", err)
	}
	return nil
}

func (r *beadsDirFSRepositoryImpl) ReadMetadataJSON(ctx context.Context) ([]byte, error) {
	path := filepath.Join(r.beadsDir, "metadata.json")
	// #nosec G304 -- path joined under bound beadsDir
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("fs: ReadMetadataJSON: %w", err)
	}
	return data, nil
}

func (r *beadsDirFSRepositoryImpl) WriteConfigYAML(ctx context.Context, content []byte) error {
	path := filepath.Join(r.beadsDir, "config.yaml")
	if err := os.WriteFile(path, content, 0600); err != nil {
		return fmt.Errorf("fs: WriteConfigYAML: %w", err)
	}
	return nil
}

func (r *beadsDirFSRepositoryImpl) ReadConfigYAML(ctx context.Context) ([]byte, error) {
	path := filepath.Join(r.beadsDir, "config.yaml")
	// #nosec G304 -- path joined under bound beadsDir
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("fs: ReadConfigYAML: %w", err)
	}
	return data, nil
}

func (r *beadsDirFSRepositoryImpl) ReadBeadsConfig(ctx context.Context) (*configfile.Config, error) {
	if r.beadsDir == "" {
		return nil, fmt.Errorf("fs: ReadBeadsConfig: beadsDir not resolved")
	}
	cfg, err := configfile.Load(r.beadsDir)
	if err != nil {
		return nil, fmt.Errorf("fs: ReadBeadsConfig: %w", err)
	}
	return cfg, nil
}

func (r *beadsDirFSRepositoryImpl) WriteProxiedServerClientInfo(ctx context.Context, info *configfile.ProxiedServerClientInfo) error {
	if r.beadsDir == "" {
		return fmt.Errorf("fs: WriteProxiedServerClientInfo: beadsDir not resolved")
	}
	if err := configfile.SaveProxiedServerClientInfo(r.beadsDir, info); err != nil {
		return fmt.Errorf("fs: WriteProxiedServerClientInfo: %w", err)
	}
	return nil
}

func (r *beadsDirFSRepositoryImpl) ReadProxiedServerClientInfo(ctx context.Context) (*configfile.ProxiedServerClientInfo, error) {
	if r.beadsDir == "" {
		return nil, fmt.Errorf("fs: ReadProxiedServerClientInfo: beadsDir not resolved")
	}
	info, err := configfile.LoadProxiedServerClientInfo(r.beadsDir)
	if err != nil {
		return nil, fmt.Errorf("fs: ReadProxiedServerClientInfo: %w", err)
	}
	return info, nil
}

func fileExists(path, opLabel string) (bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("%s: stat %s: %w", opLabel, path, err)
	}
	return !info.IsDir(), nil
}

func containsLine(content []byte, line string) bool {
	s := bufio.NewScanner(bytes.NewReader(content))
	for s.Scan() {
		if strings.TrimSpace(s.Text()) == line {
			return true
		}
	}
	return false
}

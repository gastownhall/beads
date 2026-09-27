// Package externaldeps resolves explicit cross-project capability dependencies.
package externaldeps

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

const externalPrefix = "external:"

func defaultProjectWarning(project ProjectName) {
	fmt.Fprintf(os.Stderr, "Warning: external project %q is unavailable; its capability dependencies remain blocking\n", project)
}

// ProjectName is the configured name in external_projects.
type ProjectName string

// CapabilityName is the suffix matched by a provides:<capability> label.
type CapabilityName string

// ProjectLocator resolves a configured project name to its workspace root.
// The bool is false when the project is not configured or unavailable.
type ProjectLocator func(ProjectName) (string, bool)

// StoreOpener opens a project workspace as a read-only store.
type StoreOpener func(context.Context, string) (storage.DoltStorage, error)

type reference struct {
	raw        string
	project    ProjectName
	capability CapabilityName
	valid      bool
}

func parseReference(raw string) reference {
	ref := reference{raw: raw}
	if !strings.HasPrefix(raw, externalPrefix) {
		return ref
	}
	parts := strings.SplitN(raw, ":", 3)
	if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
		return ref
	}
	ref.project = ProjectName(parts[1])
	ref.capability = CapabilityName(parts[2])
	ref.valid = true
	return ref
}

func isExternalReference(raw string) bool {
	return strings.HasPrefix(raw, externalPrefix)
}

// resolveReferences returns ref -> satisfied. Every ref starts unsatisfied;
// malformed refs and foreign-store failures therefore fail closed.
func (s *Store) resolveReferences(ctx context.Context, refs []reference) (map[string]bool, error) {
	result := make(map[string]bool, len(refs))
	byProject := make(map[ProjectName]map[CapabilityName][]string)
	for _, ref := range refs {
		result[ref.raw] = false
		if !ref.valid {
			continue
		}
		if byProject[ref.project] == nil {
			byProject[ref.project] = make(map[CapabilityName][]string)
		}
		byProject[ref.project][ref.capability] = append(byProject[ref.project][ref.capability], ref.raw)
	}

	if s.locateProject == nil || s.openProject == nil {
		return result, nil
	}

	for project, capabilities := range byProject {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path, ok := s.locateProject(project)
		if !ok {
			s.warnUnresolvedProject(project)
			continue
		}
		foreign, err := s.openProject(ctx, path)
		if err != nil || foreign == nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			s.warnUnresolvedProject(project)
			continue
		}

		for capability, rawRefs := range capabilities {
			issues, queryErr := foreign.GetIssuesByLabel(ctx, "provides:"+string(capability))
			if queryErr != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					_ = foreign.Close()
					return nil, ctxErr
				}
				s.warnUnresolvedProject(project)
				continue
			}
			satisfied := false
			for _, issue := range issues {
				if issue != nil && issue.Status == types.StatusClosed {
					satisfied = true
					break
				}
			}
			if satisfied {
				for _, raw := range rawRefs {
					result[raw] = true
				}
			}
		}
		_ = foreign.Close()
	}

	return result, nil
}

package pluginprocess

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	backendplugin "github.com/steveyegge/beads/backend/plugin"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

var _ storage.DoltStorage = (*Store)(nil)
var _ storage.PendingCommitter = (*Store)(nil)
var _ storage.RawSQLExecutor = (*Store)(nil)

type OpenOptions struct {
	Config
	BeadsDir string
	Database string
	Branch   string
	ReadOnly bool
}

type Store struct {
	client    *Client
	sessionID string
	closeOnce sync.Once
}

// Capabilities returns the behavior advertised by the plugin hello. The
// configured factory exposes a backend-neutral copy to public consumers.
func (s *Store) Capabilities() backendplugin.Capabilities {
	return s.client.Hello().Capabilities
}

func Open(ctx context.Context, opts OpenOptions) (*Store, error) {
	client, err := Start(ctx, opts.Config)
	if err != nil {
		return nil, err
	}
	var opened openResult
	if err := client.request(ctx, "open", openParams{
		BeadsDir: opts.BeadsDir,
		Database: opts.Database,
		Branch:   opts.Branch,
		ReadOnly: opts.ReadOnly,
	}, &opened); err != nil {
		_ = client.Close()
		return nil, err
	}
	return &Store{client: client, sessionID: opened.SessionID}, nil
}

func (s *Store) Close() error {
	var err error
	s.closeOnce.Do(func() {
		if s.sessionID != "" {
			err = errors.Join(err, s.client.request(context.Background(), "close", sessionParams{SessionID: s.sessionID}, nil))
		}
		err = errors.Join(err, s.client.Close())
	})
	return err
}

func (s *Store) Path() string {
	var out struct {
		Path string `json:"path"`
	}
	if err := s.client.request(context.Background(), "path", sessionParams{SessionID: s.sessionID}, &out); err != nil {
		return ""
	}
	return out.Path
}

func (s *Store) CLIDir() string {
	var out struct {
		Path string `json:"path"`
	}
	if err := s.client.request(context.Background(), "cli_dir", sessionParams{SessionID: s.sessionID}, &out); err != nil {
		return ""
	}
	return out.Path
}

func (s *Store) SetConfig(ctx context.Context, key, value string) error {
	return s.client.request(ctx, "set_config", configParams{SessionID: s.sessionID, Key: key, Value: value}, nil)
}

func (s *Store) GetConfig(ctx context.Context, key string) (string, error) {
	var out struct {
		Value string `json:"value"`
	}
	if err := s.client.request(ctx, "get_config", configParams{SessionID: s.sessionID, Key: key}, &out); err != nil {
		return "", err
	}
	return out.Value, nil
}

func (s *Store) GetAllConfig(ctx context.Context) (map[string]string, error) {
	var out map[string]string
	if err := s.client.request(ctx, "get_all_config", sessionParams{SessionID: s.sessionID}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) ExecuteRawSQL(ctx context.Context, query string) (storage.RawSQLResult, error) {
	var out rawSQLResult
	if err := s.client.request(ctx, "raw_sql", rawSQLParams{SessionID: s.sessionID, Query: query}, &out); err != nil {
		return storage.RawSQLResult{}, err
	}
	return storage.RawSQLResult{
		Columns:      out.Columns,
		Rows:         out.Rows,
		RowsAffected: out.RowsAffected,
		Read:         out.Read,
	}, nil
}

func (s *Store) DeleteConfig(ctx context.Context, key string) error {
	return s.client.request(ctx, "delete_config", configParams{SessionID: s.sessionID, Key: key}, nil)
}

func (s *Store) GetCustomStatuses(ctx context.Context) ([]string, error) {
	var out []string
	if err := s.client.request(ctx, "get_custom_statuses", sessionParams{SessionID: s.sessionID}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) GetCustomStatusesDetailed(ctx context.Context) ([]types.CustomStatus, error) {
	var out []types.CustomStatus
	if err := s.client.request(ctx, "get_custom_statuses_detailed", sessionParams{SessionID: s.sessionID}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) GetCustomTypes(ctx context.Context) ([]string, error) {
	var out []string
	if err := s.client.request(ctx, "get_custom_types", sessionParams{SessionID: s.sessionID}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) GetInfraTypes(ctx context.Context) map[string]bool {
	var out map[string]bool
	if err := s.client.request(ctx, "get_infra_types", sessionParams{SessionID: s.sessionID}, &out); err != nil {
		return nil
	}
	return out
}

func (s *Store) IsInfraTypeCtx(ctx context.Context, t types.IssueType) bool {
	var out struct {
		OK bool `json:"ok"`
	}
	if err := s.client.request(ctx, "is_infra_type", issueTypeParams{SessionID: s.sessionID, IssueType: t}, &out); err != nil {
		return false
	}
	return out.OK
}

func (s *Store) SetMetadata(ctx context.Context, key, value string) error {
	return s.client.request(ctx, "set_metadata", metadataParams{SessionID: s.sessionID, Key: key, Value: value}, nil)
}

func (s *Store) GetMetadata(ctx context.Context, key string) (string, error) {
	var out struct {
		Value string `json:"value"`
	}
	if err := s.client.request(ctx, "get_metadata", metadataParams{SessionID: s.sessionID, Key: key}, &out); err != nil {
		return "", err
	}
	return out.Value, nil
}

func (s *Store) SetLocalMetadata(ctx context.Context, key, value string) error {
	return s.client.request(ctx, "set_local_metadata", metadataParams{SessionID: s.sessionID, Key: key, Value: value}, nil)
}

func (s *Store) GetLocalMetadata(ctx context.Context, key string) (string, error) {
	var out struct {
		Value string `json:"value"`
	}
	if err := s.client.request(ctx, "get_local_metadata", metadataParams{SessionID: s.sessionID, Key: key}, &out); err != nil {
		return "", err
	}
	return out.Value, nil
}

func (s *Store) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	var out types.Issue
	if err := s.client.request(ctx, "create_issue", createIssueParams{SessionID: s.sessionID, Issue: issue, Actor: actor}, &out); err != nil {
		return err
	}
	if issue != nil {
		*issue = out
	}
	return nil
}

func (s *Store) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error {
	var out []*types.Issue
	if err := s.client.request(ctx, "create_issues", createIssuesParams{SessionID: s.sessionID, Issues: issues, Actor: actor}, &out); err != nil {
		return err
	}
	copyIssueResults(issues, out)
	return nil
}

func (s *Store) CreateIssuesWithFullOptions(ctx context.Context, issues []*types.Issue, actor string, opts storage.BatchCreateOptions) error {
	var out []*types.Issue
	if err := s.client.request(ctx, "create_issues_with_full_options", createIssuesParams{SessionID: s.sessionID, Issues: issues, Actor: actor, Options: backendplugin.BatchCreateOptionsFromStorage(opts)}, &out); err != nil {
		return err
	}
	copyIssueResults(issues, out)
	return nil
}

func copyIssueResults(dst, src []*types.Issue) {
	for i := range dst {
		if i >= len(src) || dst[i] == nil || src[i] == nil {
			continue
		}
		*dst[i] = *src[i]
	}
}

func (s *Store) RunInTransaction(ctx context.Context, commitMsg string, fn func(tx storage.Transaction) error) (err error) {
	var out struct {
		TxID string `json:"tx_id"`
	}
	if err := s.client.request(ctx, "begin_transaction", transactionParams{SessionID: s.sessionID, CommitMsg: commitMsg}, &out); err != nil {
		return err
	}
	if out.TxID == "" {
		return errors.New("backend plugin returned empty transaction id")
	}
	tx := &transactionStore{client: s.client, txID: out.TxID}
	defer func() {
		if r := recover(); r != nil {
			_ = s.client.request(context.Background(), "rollback_transaction", transactionParams{TxID: out.TxID}, nil)
			panic(r)
		}
		if err != nil {
			_ = s.client.request(context.Background(), "rollback_transaction", transactionParams{TxID: out.TxID}, nil)
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	return s.client.request(ctx, "commit_transaction", transactionParams{TxID: out.TxID}, nil)
}

type transactionStore struct {
	client *Client
	txID   string
}

var _ storage.Transaction = (*transactionStore)(nil)

func (t *transactionStore) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	var out types.Issue
	if err := t.client.request(ctx, "tx_create_issue", createIssueParams{SessionID: t.txID, Issue: issue, Actor: actor}, &out); err != nil {
		return err
	}
	if issue != nil {
		*issue = out
	}
	return nil
}

func (t *transactionStore) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error {
	var out []*types.Issue
	if err := t.client.request(ctx, "tx_create_issues", createIssuesParams{SessionID: t.txID, Issues: issues, Actor: actor}, &out); err != nil {
		return err
	}
	copyIssueResults(issues, out)
	return nil
}

func (t *transactionStore) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	return t.client.request(ctx, "tx_update_issue", updateIssueParams{SessionID: t.txID, ID: id, Updates: updates, Actor: actor}, nil)
}

func (t *transactionStore) CloseIssue(ctx context.Context, id string, reason string, actor string, session string) error {
	return t.client.request(ctx, "tx_close_issue", closeIssueParams{SessionID: t.txID, ID: id, Reason: reason, Actor: actor, Session: session}, nil)
}

func (t *transactionStore) DeleteIssue(ctx context.Context, id string) error {
	return t.client.request(ctx, "tx_delete_issue", issueIDParams{SessionID: t.txID, ID: id}, nil)
}

func (t *transactionStore) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	var out types.Issue
	if err := t.client.request(ctx, "tx_get_issue", issueIDParams{SessionID: t.txID, ID: id}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (t *transactionStore) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	var out []*types.Issue
	if err := t.client.request(ctx, "tx_search_issues", searchIssuesParams{SessionID: t.txID, Query: query, Filter: filter}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (t *transactionStore) SearchIssueIDs(ctx context.Context, query string, filter types.IssueFilter) ([]string, error) {
	issues, err := t.SearchIssues(ctx, query, filter)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(issues))
	for _, issue := range issues {
		if issue != nil {
			ids = append(ids, issue.ID)
		}
	}
	return ids, nil
}

func (t *transactionStore) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	return t.AddDependencyWithOptions(ctx, dep, actor, storage.DependencyAddOptions{})
}

func (t *transactionStore) AddDependencyWithOptions(ctx context.Context, dep *types.Dependency, actor string, opts storage.DependencyAddOptions) error {
	return t.client.request(ctx, "tx_add_dependency", dependencyParams{SessionID: t.txID, Dependency: dep, Actor: actor, Options: opts}, nil)
}

func (t *transactionStore) RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error {
	return t.client.request(ctx, "tx_remove_dependency", dependencyParams{SessionID: t.txID, IssueID: issueID, DependsOnID: dependsOnID, Actor: actor}, nil)
}

func (t *transactionStore) GetDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error) {
	var out []*types.Dependency
	if err := t.client.request(ctx, "tx_get_dependency_records", issueIDParams{SessionID: t.txID, ID: issueID}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (t *transactionStore) CycleThroughEdges(ctx context.Context, edges [][2]string) (string, error) {
	var out struct {
		Cycle string `json:"cycle"`
	}
	if err := t.client.request(ctx, "tx_cycle_through_edges", cycleEdgesParams{SessionID: t.txID, Edges: edges}, &out); err != nil {
		return "", err
	}
	return out.Cycle, nil
}

func (t *transactionStore) AddLabel(ctx context.Context, issueID, label, actor string) error {
	return t.client.request(ctx, "tx_add_label", addLabelParams{SessionID: t.txID, ID: issueID, Label: label, Actor: actor}, nil)
}

func (t *transactionStore) RemoveLabel(ctx context.Context, issueID, label, actor string) error {
	return t.client.request(ctx, "tx_remove_label", labelParams{SessionID: t.txID, ID: issueID, Label: label, Actor: actor}, nil)
}

func (t *transactionStore) GetLabels(ctx context.Context, issueID string) ([]string, error) {
	var out []string
	if err := t.client.request(ctx, "tx_get_labels", issueIDParams{SessionID: t.txID, ID: issueID}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (t *transactionStore) SetConfig(ctx context.Context, key, value string) error {
	return t.client.request(ctx, "tx_set_config", configParams{SessionID: t.txID, Key: key, Value: value}, nil)
}

func (t *transactionStore) GetConfig(ctx context.Context, key string) (string, error) {
	var out struct {
		Value string `json:"value"`
	}
	if err := t.client.request(ctx, "tx_get_config", configParams{SessionID: t.txID, Key: key}, &out); err != nil {
		return "", err
	}
	return out.Value, nil
}

func (t *transactionStore) SetMetadata(ctx context.Context, key, value string) error {
	return t.client.request(ctx, "tx_set_metadata", metadataParams{SessionID: t.txID, Key: key, Value: value}, nil)
}

func (t *transactionStore) GetMetadata(ctx context.Context, key string) (string, error) {
	var out struct {
		Value string `json:"value"`
	}
	if err := t.client.request(ctx, "tx_get_metadata", metadataParams{SessionID: t.txID, Key: key}, &out); err != nil {
		return "", err
	}
	return out.Value, nil
}

func (t *transactionStore) SetLocalMetadata(ctx context.Context, key, value string) error {
	return t.client.request(ctx, "tx_set_local_metadata", metadataParams{SessionID: t.txID, Key: key, Value: value}, nil)
}

func (t *transactionStore) GetLocalMetadata(ctx context.Context, key string) (string, error) {
	var out struct {
		Value string `json:"value"`
	}
	if err := t.client.request(ctx, "tx_get_local_metadata", metadataParams{SessionID: t.txID, Key: key}, &out); err != nil {
		return "", err
	}
	return out.Value, nil
}

func (t *transactionStore) AddComment(ctx context.Context, issueID, actor, comment string) error {
	return t.client.request(ctx, "tx_add_comment", commentParams{SessionID: t.txID, IssueID: issueID, Author: actor, Text: comment}, nil)
}

func (t *transactionStore) ImportIssueComment(ctx context.Context, issueID, author, text string, createdAt time.Time) (*types.Comment, error) {
	var out types.Comment
	if err := t.client.request(ctx, "tx_import_issue_comment", commentParams{SessionID: t.txID, IssueID: issueID, Author: author, Text: text, CreatedAt: createdAt}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (t *transactionStore) GetIssueComments(ctx context.Context, issueID string) ([]*types.Comment, error) {
	var out []*types.Comment
	if err := t.client.request(ctx, "tx_get_issue_comments", issueIDParams{SessionID: t.txID, ID: issueID}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	var out types.Issue
	if err := s.client.request(ctx, "get_issue", issueIDParams{SessionID: s.sessionID, ID: id}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *Store) GetIssueByExternalRef(ctx context.Context, externalRef string) (*types.Issue, error) {
	var out types.Issue
	if err := s.client.request(ctx, "get_issue_by_external_ref", externalRefParams{SessionID: s.sessionID, ExternalRef: externalRef}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *Store) GetIssuesByIDs(ctx context.Context, ids []string) ([]*types.Issue, error) {
	var out []*types.Issue
	if err := s.client.request(ctx, "get_issues_by_ids", issueIDsParams{SessionID: s.sessionID, IDs: ids}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	var out []*types.Issue
	if err := s.client.request(ctx, "search_issues", searchIssuesParams{SessionID: s.sessionID, Query: query, Filter: filter}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) SearchIssueIDs(ctx context.Context, query string, filter types.IssueFilter) ([]string, error) {
	issues, err := s.SearchIssues(ctx, query, filter)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(issues))
	for _, issue := range issues {
		if issue != nil {
			ids = append(ids, issue.ID)
		}
	}
	return ids, nil
}

func (s *Store) SearchIssuesWithCounts(ctx context.Context, query string, filter types.IssueFilter) ([]*types.IssueWithCounts, error) {
	var out []*types.IssueWithCounts
	if err := s.client.request(ctx, "search_issues_with_counts", searchIssuesParams{SessionID: s.sessionID, Query: query, Filter: filter}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	var out types.Issue
	return s.client.request(ctx, "update_issue", updateIssueParams{SessionID: s.sessionID, ID: id, Updates: updates, Actor: actor}, &out)
}

func (s *Store) ReopenIssue(ctx context.Context, id string, reason string, actor string) error {
	return s.client.request(ctx, "reopen_issue", reopenIssueParams{SessionID: s.sessionID, ID: id, Reason: reason, Actor: actor}, nil)
}

func (s *Store) UpdateIssueType(ctx context.Context, id string, issueType string, actor string) error {
	return s.client.request(ctx, "update_issue_type", updateIssueTypeParams{SessionID: s.sessionID, ID: id, IssueType: issueType, Actor: actor}, nil)
}

func (s *Store) CloseIssue(ctx context.Context, id string, reason string, actor string, session string) error {
	return s.client.request(ctx, "close_issue", closeIssueParams{SessionID: s.sessionID, ID: id, Reason: reason, Actor: actor, Session: session}, nil)
}

func (s *Store) DeleteIssue(ctx context.Context, id string) error {
	return s.client.request(ctx, "delete_issue", issueIDParams{SessionID: s.sessionID, ID: id}, nil)
}

func (s *Store) DeleteIssues(ctx context.Context, ids []string, cascade bool, force bool, dryRun bool) (*types.DeleteIssuesResult, error) {
	var out types.DeleteIssuesResult
	if err := s.client.request(ctx, "delete_issues", deleteIssuesParams{SessionID: s.sessionID, IDs: ids, Cascade: cascade, Force: force, DryRun: dryRun}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *Store) DeleteIssuesBySourceRepo(ctx context.Context, sourceRepo string) (int, error) {
	var out struct {
		Count int `json:"count"`
	}
	if err := s.client.request(ctx, "delete_issues_by_source_repo", sourceRepoParams{SessionID: s.sessionID, SourceRepo: sourceRepo}, &out); err != nil {
		return 0, err
	}
	return out.Count, nil
}

func (s *Store) UpdateIssueID(ctx context.Context, oldID, newID string, issue *types.Issue, actor string) error {
	return s.client.request(ctx, "update_issue_id", updateIssueIDParams{SessionID: s.sessionID, OldID: oldID, NewID: newID, Issue: issue, Actor: actor}, nil)
}

func (s *Store) ClaimIssue(ctx context.Context, id string, actor string) error {
	return s.client.request(ctx, "claim_issue", claimIssueParams{SessionID: s.sessionID, ID: id, Actor: actor}, nil)
}

func (s *Store) UnclaimIssue(ctx context.Context, id string, actor string) error {
	return s.client.request(ctx, "unclaim_issue", claimIssueParams{SessionID: s.sessionID, ID: id, Actor: actor}, nil)
}

func (s *Store) ClaimReadyIssue(ctx context.Context, filter types.WorkFilter, actor string) (*types.Issue, error) {
	var out types.Issue
	if err := s.client.request(ctx, "claim_ready_issue", claimIssueParams{SessionID: s.sessionID, Filter: filter, Actor: actor}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *Store) HeartbeatIssue(ctx context.Context, id, actor string) error {
	return s.client.request(ctx, "heartbeat_issue", claimIssueParams{SessionID: s.sessionID, ID: id, Actor: actor}, nil)
}

func (s *Store) ReclaimExpiredLeases(ctx context.Context, olderThan time.Duration, actor string) ([]types.ReclaimedLease, error) {
	var out []types.ReclaimedLease
	if err := s.client.request(ctx, "reclaim_expired_leases", backendplugin.ReclaimExpiredLeasesParams{SessionID: s.sessionID, OlderThan: olderThan, Actor: actor}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) PromoteFromEphemeral(ctx context.Context, id string, actor string) error {
	return s.client.request(ctx, "promote_from_ephemeral", claimIssueParams{SessionID: s.sessionID, ID: id, Actor: actor}, nil)
}

func (s *Store) GetNextChildID(ctx context.Context, parentID string) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	if err := s.client.request(ctx, "get_next_child_id", issueIDParams{SessionID: s.sessionID, ID: parentID}, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

func (s *Store) RenameCounterPrefix(ctx context.Context, oldPrefix, newPrefix string) error {
	return s.client.request(ctx, "rename_counter_prefix", prefixRenameParams{SessionID: s.sessionID, OldPrefix: oldPrefix, NewPrefix: newPrefix}, nil)
}

func (s *Store) RenameDependencyPrefix(ctx context.Context, oldPrefix, newPrefix string) error {
	return s.client.request(ctx, "rename_dependency_prefix", prefixRenameParams{SessionID: s.sessionID, OldPrefix: oldPrefix, NewPrefix: newPrefix}, nil)
}

func (s *Store) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	return s.client.request(ctx, "add_dependency", dependencyParams{SessionID: s.sessionID, Dependency: dep, Actor: actor}, nil)
}

func (s *Store) RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error {
	return s.client.request(ctx, "remove_dependency", dependencyParams{SessionID: s.sessionID, IssueID: issueID, DependsOnID: dependsOnID, Actor: actor}, nil)
}

func (s *Store) GetDependencies(ctx context.Context, issueID string) ([]*types.Issue, error) {
	var out []*types.Issue
	if err := s.client.request(ctx, "get_dependencies", issueIDParams{SessionID: s.sessionID, ID: issueID}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) GetDependents(ctx context.Context, issueID string) ([]*types.Issue, error) {
	var out []*types.Issue
	if err := s.client.request(ctx, "get_dependents", issueIDParams{SessionID: s.sessionID, ID: issueID}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) GetDependencyTree(ctx context.Context, issueID string, maxDepth int, showAllPaths bool, reverse bool) ([]*types.TreeNode, error) {
	var out []*types.TreeNode
	if err := s.client.request(ctx, "get_dependency_tree", dependencyTreeParams{
		SessionID:    s.sessionID,
		IssueID:      issueID,
		MaxDepth:     maxDepth,
		ShowAllPaths: showAllPaths,
		Reverse:      reverse,
	}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) AddLabel(ctx context.Context, issueID, label, actor string) error {
	var out []string
	return s.client.request(ctx, "add_label", addLabelParams{SessionID: s.sessionID, ID: issueID, Label: label, Actor: actor}, &out)
}

func (s *Store) RemoveLabel(ctx context.Context, issueID, label, actor string) error {
	return s.client.request(ctx, "remove_label", labelParams{SessionID: s.sessionID, ID: issueID, Label: label, Actor: actor}, nil)
}

func (s *Store) GetLabels(ctx context.Context, issueID string) ([]string, error) {
	var out []string
	if err := s.client.request(ctx, "get_labels", issueIDParams{SessionID: s.sessionID, ID: issueID}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) GetIssuesByLabel(ctx context.Context, label string) ([]*types.Issue, error) {
	var out []*types.Issue
	if err := s.client.request(ctx, "get_issues_by_label", labelParams{SessionID: s.sessionID, Label: label}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) GetDependenciesWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	var out []*types.IssueWithDependencyMetadata
	if err := s.client.request(ctx, "get_dependencies_with_metadata", issueIDParams{SessionID: s.sessionID, ID: issueID}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) GetDependentsWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	var out []*types.IssueWithDependencyMetadata
	if err := s.client.request(ctx, "get_dependents_with_metadata", issueIDParams{SessionID: s.sessionID, ID: issueID}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) GetDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error) {
	var out []*types.Dependency
	if err := s.client.request(ctx, "get_dependency_records", issueIDParams{SessionID: s.sessionID, ID: issueID}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) GetDependencyRecordsForIssues(ctx context.Context, issueIDs []string) (map[string][]*types.Dependency, error) {
	var out map[string][]*types.Dependency
	if err := s.client.request(ctx, "get_dependency_records_for_issues", issueIDsParams{SessionID: s.sessionID, IDs: issueIDs}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) GetAllDependencyRecords(ctx context.Context) (map[string][]*types.Dependency, error) {
	var out map[string][]*types.Dependency
	if err := s.client.request(ctx, "get_all_dependency_records", sessionParams{SessionID: s.sessionID}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) GetDependencyCounts(ctx context.Context, issueIDs []string) (map[string]*types.DependencyCounts, error) {
	var out map[string]*types.DependencyCounts
	if err := s.client.request(ctx, "get_dependency_counts", dependencyCountsParams{SessionID: s.sessionID, IssueIDs: issueIDs}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) GetBlockingInfoForIssues(ctx context.Context, issueIDs []string) (map[string][]string, map[string][]string, map[string]string, error) {
	var out struct {
		BlockedBy map[string][]string `json:"blocked_by"`
		Blocks    map[string][]string `json:"blocks"`
		Parents   map[string]string   `json:"parents"`
	}
	if err := s.client.request(ctx, "get_blocking_info_for_issues", issueIDsParams{SessionID: s.sessionID, IDs: issueIDs}, &out); err != nil {
		return nil, nil, nil, err
	}
	return out.BlockedBy, out.Blocks, out.Parents, nil
}

func (s *Store) IsBlocked(ctx context.Context, issueID string) (bool, []string, error) {
	var out struct {
		Blocked   bool     `json:"blocked"`
		BlockedBy []string `json:"blocked_by"`
	}
	if err := s.client.request(ctx, "is_blocked", issueIDParams{SessionID: s.sessionID, ID: issueID}, &out); err != nil {
		return false, nil, err
	}
	return out.Blocked, out.BlockedBy, nil
}

func (s *Store) GetNewlyUnblockedByClose(ctx context.Context, closedIssueID string) ([]*types.Issue, error) {
	var out []*types.Issue
	if err := s.client.request(ctx, "get_newly_unblocked_by_close", issueIDParams{SessionID: s.sessionID, ID: closedIssueID}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) DetectCycles(ctx context.Context) ([][]*types.Issue, error) {
	var out [][]*types.Issue
	if err := s.client.request(ctx, "detect_cycles", sessionParams{SessionID: s.sessionID}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) FindWispDependentsRecursive(ctx context.Context, ids []string) (map[string]bool, error) {
	var out map[string]bool
	if err := s.client.request(ctx, "find_wisp_dependents_recursive", issueIDsParams{SessionID: s.sessionID, IDs: ids}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) CountDependentsByStatus(ctx context.Context, issueID string, status types.Status) (int64, error) {
	var out struct {
		Count int64 `json:"count"`
	}
	if err := s.client.request(ctx, "count_dependents_by_status", statusCountParams{SessionID: s.sessionID, IssueID: issueID, Status: status}, &out); err != nil {
		return 0, err
	}
	return out.Count, nil
}

func (s *Store) GetIssueComments(ctx context.Context, issueID string) ([]*types.Comment, error) {
	var out []*types.Comment
	if err := s.client.request(ctx, "get_issue_comments", issueIDParams{SessionID: s.sessionID, ID: issueID}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) AddIssueComment(ctx context.Context, issueID, author, text string) (*types.Comment, error) {
	var out types.Comment
	if err := s.client.request(ctx, "add_issue_comment", commentParams{SessionID: s.sessionID, IssueID: issueID, Author: author, Text: text}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *Store) AddComment(ctx context.Context, issueID, actor, comment string) error {
	return s.client.request(ctx, "add_comment", commentParams{SessionID: s.sessionID, IssueID: issueID, Author: actor, Text: comment}, nil)
}

func (s *Store) ImportIssueComment(ctx context.Context, issueID, author, text string, createdAt time.Time) (*types.Comment, error) {
	var out types.Comment
	if err := s.client.request(ctx, "import_issue_comment", commentParams{SessionID: s.sessionID, IssueID: issueID, Author: author, Text: text, CreatedAt: createdAt}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *Store) GetEvents(ctx context.Context, issueID string, limit int) ([]*types.Event, error) {
	var out []*types.Event
	if err := s.client.request(ctx, "get_events", eventsParams{SessionID: s.sessionID, IssueID: issueID, Limit: limit}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) GetAllEventsSince(ctx context.Context, since time.Time) ([]*types.Event, error) {
	var out []*types.Event
	if err := s.client.request(ctx, "get_all_events_since", eventsParams{SessionID: s.sessionID, Since: since}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) GetCommentCounts(ctx context.Context, issueIDs []string) (map[string]int, error) {
	var out map[string]int
	if err := s.client.request(ctx, "get_comment_counts", issueIDsParams{SessionID: s.sessionID, IDs: issueIDs}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) GetCommentsForIssues(ctx context.Context, issueIDs []string) (map[string][]*types.Comment, error) {
	var out map[string][]*types.Comment
	if err := s.client.request(ctx, "get_comments_for_issues", issueIDsParams{SessionID: s.sessionID, IDs: issueIDs}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) GetLabelsForIssues(ctx context.Context, issueIDs []string) (map[string][]string, error) {
	var out map[string][]string
	if err := s.client.request(ctx, "get_labels_for_issues", issueIDsParams{SessionID: s.sessionID, IDs: issueIDs}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) GetReadyWork(ctx context.Context, filter types.WorkFilter) ([]*types.Issue, error) {
	var out []*types.Issue
	if err := s.client.request(ctx, "ready_work", readyWorkParams{SessionID: s.sessionID, Filter: filter}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) GetBlockedIssues(ctx context.Context, filter types.WorkFilter) ([]*types.BlockedIssue, error) {
	var out []*types.BlockedIssue
	if err := s.client.request(ctx, "blocked_issues", readyWorkParams{SessionID: s.sessionID, Filter: filter}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) GetEpicsEligibleForClosure(ctx context.Context) ([]*types.EpicStatus, error) {
	var out []*types.EpicStatus
	if err := s.client.request(ctx, "epics_eligible_for_closure", sessionParams{SessionID: s.sessionID}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) ListWisps(ctx context.Context, filter types.WispFilter) ([]*types.Issue, error) {
	var out []*types.Issue
	if err := s.client.request(ctx, "list_wisps", wispParams{SessionID: s.sessionID, Filter: filter}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) CountIssues(ctx context.Context, query string, filter types.IssueFilter) (int64, error) {
	var out struct {
		Count int64 `json:"count"`
	}
	if err := s.client.request(ctx, "count_issues", countIssuesParams{SessionID: s.sessionID, Query: query, Filter: filter}, &out); err != nil {
		return 0, err
	}
	return out.Count, nil
}

func (s *Store) CountIssuesByGroup(ctx context.Context, filter types.IssueFilter, groupBy string) (map[string]int, error) {
	var out map[string]int
	if err := s.client.request(ctx, "count_issues_by_group", countIssuesParams{SessionID: s.sessionID, Filter: filter, GroupBy: groupBy}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) CountDependents(ctx context.Context, issueID string) (int64, error) {
	var out struct {
		Count int64 `json:"count"`
	}
	if err := s.client.request(ctx, "count_dependents", countIssueParams{SessionID: s.sessionID, IssueID: issueID}, &out); err != nil {
		return 0, err
	}
	return out.Count, nil
}

func (s *Store) CountDependencies(ctx context.Context, issueID string) (int64, error) {
	var out struct {
		Count int64 `json:"count"`
	}
	if err := s.client.request(ctx, "count_dependencies", countIssueParams{SessionID: s.sessionID, IssueID: issueID}, &out); err != nil {
		return 0, err
	}
	return out.Count, nil
}

func (s *Store) CountIssueComments(ctx context.Context, issueID string) (int64, error) {
	var out struct {
		Count int64 `json:"count"`
	}
	if err := s.client.request(ctx, "count_issue_comments", countIssueParams{SessionID: s.sessionID, IssueID: issueID}, &out); err != nil {
		return 0, err
	}
	return out.Count, nil
}

func (s *Store) CountEvents(ctx context.Context, issueID string, limit int) (int64, error) {
	var out struct {
		Count int64 `json:"count"`
	}
	if err := s.client.request(ctx, "count_events", countIssueParams{SessionID: s.sessionID, IssueID: issueID, Limit: limit}, &out); err != nil {
		return 0, err
	}
	return out.Count, nil
}

func (s *Store) GetStatistics(ctx context.Context) (*types.Statistics, error) {
	var out types.Statistics
	if err := s.client.request(ctx, "statistics", sessionParams{SessionID: s.sessionID}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *Store) GetRepoMtime(ctx context.Context, repoPath string) (int64, error) {
	var out struct {
		MtimeNS int64 `json:"mtime_ns"`
	}
	if err := s.client.request(ctx, "get_repo_mtime", repoMtimeParams{SessionID: s.sessionID, RepoPath: repoPath}, &out); err != nil {
		return 0, err
	}
	return out.MtimeNS, nil
}

func (s *Store) SetRepoMtime(ctx context.Context, repoPath, jsonlPath string, mtimeNs int64) error {
	return s.client.request(ctx, "set_repo_mtime", repoMtimeParams{SessionID: s.sessionID, RepoPath: repoPath, JSONLPath: jsonlPath, MtimeNS: mtimeNs}, nil)
}

func (s *Store) ClearRepoMtime(ctx context.Context, repoPath string) error {
	return s.client.request(ctx, "clear_repo_mtime", repoMtimeParams{SessionID: s.sessionID, RepoPath: repoPath}, nil)
}

func (s *Store) GetMoleculeProgress(ctx context.Context, moleculeID string) (*types.MoleculeProgressStats, error) {
	var out types.MoleculeProgressStats
	if err := s.client.request(ctx, "get_molecule_progress", moleculeParams{SessionID: s.sessionID, MoleculeID: moleculeID}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *Store) GetMoleculeLastActivity(ctx context.Context, moleculeID string) (*types.MoleculeLastActivity, error) {
	var out types.MoleculeLastActivity
	if err := s.client.request(ctx, "get_molecule_last_activity", moleculeParams{SessionID: s.sessionID, MoleculeID: moleculeID}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *Store) GetStaleIssues(ctx context.Context, filter types.StaleFilter) ([]*types.Issue, error) {
	var out []*types.Issue
	if err := s.client.request(ctx, "get_stale_issues", staleIssuesParams{SessionID: s.sessionID, Filter: filter}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) DoltGC(ctx context.Context) error {
	return s.client.request(ctx, "dolt_gc", sessionParams{SessionID: s.sessionID}, nil)
}

func (s *Store) Flatten(ctx context.Context) error {
	return s.client.request(ctx, "flatten", sessionParams{SessionID: s.sessionID}, nil)
}

func (s *Store) Compact(ctx context.Context, initialHash, boundaryHash string, oldCommits int, recentHashes []string) error {
	return s.client.request(ctx, "compact", compactionParams{
		SessionID:    s.sessionID,
		InitialHash:  initialHash,
		BoundaryHash: boundaryHash,
		OldCommits:   oldCommits,
		RecentHashes: recentHashes,
	}, nil)
}

func (s *Store) CheckEligibility(ctx context.Context, issueID string, tier int) (bool, string, error) {
	var out struct {
		Eligible bool   `json:"eligible"`
		Reason   string `json:"reason"`
	}
	if err := s.client.request(ctx, "check_eligibility", compactionParams{SessionID: s.sessionID, IssueID: issueID, Tier: tier}, &out); err != nil {
		return false, "", err
	}
	return out.Eligible, out.Reason, nil
}

func (s *Store) ApplyCompaction(ctx context.Context, issueID string, tier int, originalSize int, compactedSize int, commitHash string) error {
	return s.client.request(ctx, "apply_compaction", compactionParams{
		SessionID:     s.sessionID,
		IssueID:       issueID,
		Tier:          tier,
		OriginalSize:  originalSize,
		CompactedSize: compactedSize,
		CommitHash:    commitHash,
	}, nil)
}

func (s *Store) SnapshotIssue(ctx context.Context, issueID string, tier int) error {
	return s.client.request(ctx, "snapshot_issue", compactionParams{SessionID: s.sessionID, IssueID: issueID, Tier: tier}, nil)
}

func (s *Store) GetCompactionSnapshot(ctx context.Context, issueID string) (*types.IssueSnapshot, error) {
	var out types.IssueSnapshot
	if err := s.client.request(ctx, "get_compaction_snapshot", compactionParams{SessionID: s.sessionID, IssueID: issueID}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *Store) RestoreFromSnapshot(ctx context.Context, issueID string) (*types.IssueSnapshot, error) {
	var out types.IssueSnapshot
	if err := s.client.request(ctx, "restore_from_snapshot", compactionParams{SessionID: s.sessionID, IssueID: issueID}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *Store) GetTier1Candidates(ctx context.Context) ([]*types.CompactionCandidate, error) {
	var out []*types.CompactionCandidate
	if err := s.client.request(ctx, "get_tier1_candidates", sessionParams{SessionID: s.sessionID}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) GetTier2Candidates(ctx context.Context) ([]*types.CompactionCandidate, error) {
	var out []*types.CompactionCandidate
	if err := s.client.request(ctx, "get_tier2_candidates", sessionParams{SessionID: s.sessionID}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) MergeSlotCreate(ctx context.Context, actor string) (*types.Issue, error) {
	var out types.Issue
	if err := s.client.request(ctx, "merge_slot_create", mergeSlotParams{SessionID: s.sessionID, Actor: actor}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *Store) MergeSlotCheck(ctx context.Context) (*storage.MergeSlotStatus, error) {
	var out storage.MergeSlotStatus
	if err := s.client.request(ctx, "merge_slot_check", sessionParams{SessionID: s.sessionID}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *Store) MergeSlotAcquire(ctx context.Context, holder, actor string, wait bool) (*storage.MergeSlotResult, error) {
	var out storage.MergeSlotResult
	if err := s.client.request(ctx, "merge_slot_acquire", mergeSlotParams{SessionID: s.sessionID, Holder: holder, Actor: actor, Wait: wait}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *Store) MergeSlotRelease(ctx context.Context, holder, actor string) error {
	return s.client.request(ctx, "merge_slot_release", mergeSlotParams{SessionID: s.sessionID, Holder: holder, Actor: actor}, nil)
}

func (s *Store) SlotSet(ctx context.Context, issueID, key, value, actor string) error {
	return s.client.request(ctx, "slot_set", slotParams{SessionID: s.sessionID, IssueID: issueID, Key: key, Value: value, Actor: actor}, nil)
}

func (s *Store) SlotGet(ctx context.Context, issueID, key string) (string, error) {
	var out struct {
		Value string `json:"value"`
	}
	if err := s.client.request(ctx, "slot_get", slotParams{SessionID: s.sessionID, IssueID: issueID, Key: key}, &out); err != nil {
		return "", err
	}
	return out.Value, nil
}

func (s *Store) SlotClear(ctx context.Context, issueID, key, actor string) error {
	return s.client.request(ctx, "slot_clear", slotParams{SessionID: s.sessionID, IssueID: issueID, Key: key, Actor: actor}, nil)
}

func (s *Store) GetReadyWorkWithCounts(ctx context.Context, filter types.WorkFilter) ([]*types.IssueWithCounts, error) {
	var out []*types.IssueWithCounts
	if err := s.client.request(ctx, "ready_work_with_counts", readyWorkParams{SessionID: s.sessionID, Filter: filter}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) IterIssues(ctx context.Context, query string, filter types.IssueFilter) (storage.Iter[types.Issue], error) {
	issues, err := s.SearchIssues(ctx, query, filter)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(issues), nil
}

func (s *Store) IterReadyWork(ctx context.Context, filter types.WorkFilter) (storage.Iter[types.Issue], error) {
	issues, err := s.GetReadyWork(ctx, filter)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(issues), nil
}

func (s *Store) IterDependenciesWithMetadata(ctx context.Context, issueID string) (storage.Iter[types.IssueWithDependencyMetadata], error) {
	issues, err := s.GetDependenciesWithMetadata(ctx, issueID)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(issues), nil
}

func (s *Store) IterDependentsWithMetadata(ctx context.Context, issueID string) (storage.Iter[types.IssueWithDependencyMetadata], error) {
	issues, err := s.GetDependentsWithMetadata(ctx, issueID)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(issues), nil
}

func (s *Store) IterIssueComments(ctx context.Context, issueID string) (storage.Iter[types.Comment], error) {
	comments, err := s.GetIssueComments(ctx, issueID)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(comments), nil
}

func (s *Store) IterEvents(ctx context.Context, issueID string, limit int) (storage.Iter[types.Event], error) {
	events, err := s.GetEvents(ctx, issueID, limit)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(events), nil
}

func (s *Store) IterAllEventsSince(ctx context.Context, since time.Time) (storage.Iter[types.Event], error) {
	events, err := s.GetAllEventsSince(ctx, since)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(events), nil
}

func (s *Store) IterBlockedIssues(ctx context.Context, filter types.WorkFilter) (storage.Iter[types.BlockedIssue], error) {
	issues, err := s.GetBlockedIssues(ctx, filter)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(issues), nil
}

func (s *Store) IterWisps(ctx context.Context, filter types.WispFilter) (storage.Iter[types.Issue], error) {
	issues, err := s.ListWisps(ctx, filter)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(issues), nil
}

func (s *Store) IterAllDependencyRecords(ctx context.Context) (storage.Iter[types.Dependency], error) {
	byIssue, err := s.GetAllDependencyRecords(ctx)
	if err != nil {
		return nil, err
	}
	var deps []*types.Dependency
	for _, issueDeps := range byIssue {
		deps = append(deps, issueDeps...)
	}
	return storage.NewSliceIter(deps), nil
}

func (s *Store) Commit(ctx context.Context, message string) error {
	return s.client.request(ctx, "commit", commitParams{SessionID: s.sessionID, Message: message}, nil)
}

func (s *Store) CommitWithConfig(ctx context.Context, message string) error {
	return s.Commit(ctx, message)
}

func (s *Store) CommitMergeResolution(ctx context.Context, message string) error {
	return s.client.request(ctx, "commit_merge_resolution", commitParams{SessionID: s.sessionID, Message: message}, nil)
}

func (s *Store) CommitPending(ctx context.Context, actor string) (bool, error) {
	message := "commit pending changes"
	if actor != "" {
		message = "commit pending changes by " + actor
	}
	if err := s.Commit(ctx, message); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) Branch(ctx context.Context, name string) error {
	return s.client.request(ctx, "branch", refParams{SessionID: s.sessionID, Name: name}, nil)
}

func (s *Store) Checkout(ctx context.Context, branch string) error {
	return s.client.request(ctx, "checkout", refParams{SessionID: s.sessionID, Branch: branch}, nil)
}

func (s *Store) CurrentBranch(ctx context.Context) (string, error) {
	var out struct {
		Branch string `json:"branch"`
	}
	if err := s.client.request(ctx, "current_branch", sessionParams{SessionID: s.sessionID}, &out); err != nil {
		return "", err
	}
	return out.Branch, nil
}

func (s *Store) DeleteBranch(ctx context.Context, branch string) error {
	return s.client.request(ctx, "delete_branch", refParams{SessionID: s.sessionID, Branch: branch}, nil)
}

func (s *Store) ListBranches(ctx context.Context) ([]string, error) {
	var out []string
	if err := s.client.request(ctx, "list_branches", sessionParams{SessionID: s.sessionID}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) CommitExists(ctx context.Context, commitHash string) (bool, error) {
	var out struct {
		OK bool `json:"ok"`
	}
	if err := s.client.request(ctx, "commit_exists", refParams{SessionID: s.sessionID, Hash: commitHash}, &out); err != nil {
		return false, err
	}
	return out.OK, nil
}

func (s *Store) GetCurrentCommit(ctx context.Context) (string, error) {
	var out struct {
		Hash string `json:"hash"`
	}
	if err := s.client.request(ctx, "get_current_commit", sessionParams{SessionID: s.sessionID}, &out); err != nil {
		return "", err
	}
	return out.Hash, nil
}

func (s *Store) Status(ctx context.Context) (*storage.Status, error) {
	var out storage.Status
	if err := s.client.request(ctx, "status", sessionParams{SessionID: s.sessionID}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *Store) Log(ctx context.Context, limit int) ([]storage.CommitInfo, error) {
	var out []storage.CommitInfo
	if err := s.client.request(ctx, "log", refParams{SessionID: s.sessionID, Limit: limit}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) Merge(ctx context.Context, branch string) ([]storage.Conflict, error) {
	var out []storage.Conflict
	if err := s.client.request(ctx, "merge", refParams{SessionID: s.sessionID, Branch: branch}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) GetConflicts(ctx context.Context) ([]storage.Conflict, error) {
	var out []storage.Conflict
	if err := s.client.request(ctx, "get_conflicts", sessionParams{SessionID: s.sessionID}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) ResolveConflicts(ctx context.Context, table string, strategy string) error {
	return s.client.request(ctx, "resolve_conflicts", resolveConflictParams{SessionID: s.sessionID, Table: table, Strategy: strategy}, nil)
}

func (s *Store) History(ctx context.Context, issueID string) ([]*storage.HistoryEntry, error) {
	var out []*storage.HistoryEntry
	if err := s.client.request(ctx, "history", historyParams{SessionID: s.sessionID, IssueID: issueID}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) AsOf(ctx context.Context, issueID string, ref string) (*types.Issue, error) {
	var out types.Issue
	if err := s.client.request(ctx, "as_of", historyParams{SessionID: s.sessionID, IssueID: issueID, Ref: ref}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *Store) Diff(ctx context.Context, fromRef, toRef string) ([]*storage.DiffEntry, error) {
	var out []*storage.DiffEntry
	if err := s.client.request(ctx, "diff", historyParams{SessionID: s.sessionID, FromRef: fromRef, ToRef: toRef}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) AddRemote(ctx context.Context, name, url string) error {
	return s.client.request(ctx, "add_remote", refParams{SessionID: s.sessionID, Name: name, URL: url}, nil)
}

func (s *Store) RemoveRemote(ctx context.Context, name string) error {
	return s.client.request(ctx, "remove_remote", refParams{SessionID: s.sessionID, Name: name}, nil)
}

func (s *Store) HasRemote(ctx context.Context, name string) (bool, error) {
	var out struct {
		OK bool `json:"ok"`
	}
	if err := s.client.request(ctx, "has_remote", refParams{SessionID: s.sessionID, Name: name}, &out); err != nil {
		return false, err
	}
	return out.OK, nil
}

func (s *Store) ListRemotes(ctx context.Context) ([]storage.RemoteInfo, error) {
	var out []storage.RemoteInfo
	if err := s.client.request(ctx, "list_remotes", sessionParams{SessionID: s.sessionID}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) Push(ctx context.Context) error {
	return s.client.request(ctx, "push", sessionParams{SessionID: s.sessionID}, nil)
}

func (s *Store) Pull(ctx context.Context) error {
	return s.client.request(ctx, "pull", sessionParams{SessionID: s.sessionID}, nil)
}

func (s *Store) ForcePush(ctx context.Context) error {
	return s.client.request(ctx, "force_push", sessionParams{SessionID: s.sessionID}, nil)
}

func (s *Store) PushRemote(ctx context.Context, remote string, force bool) error {
	return s.client.request(ctx, "push_remote", refParams{SessionID: s.sessionID, Name: remote, Force: force}, nil)
}

func (s *Store) PullRemote(ctx context.Context, remote string) error {
	return s.client.request(ctx, "pull_remote", refParams{SessionID: s.sessionID, Name: remote}, nil)
}

func (s *Store) Fetch(ctx context.Context, peer string) error {
	return s.client.request(ctx, "fetch", refParams{SessionID: s.sessionID, Peer: peer}, nil)
}

func (s *Store) PushTo(ctx context.Context, peer string) error {
	return s.client.request(ctx, "push_to", refParams{SessionID: s.sessionID, Peer: peer}, nil)
}

func (s *Store) PullFrom(ctx context.Context, peer string) ([]storage.Conflict, error) {
	var out []storage.Conflict
	if err := s.client.request(ctx, "pull_from", refParams{SessionID: s.sessionID, Peer: peer}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) BackupAdd(ctx context.Context, name, url string) error {
	return s.client.request(ctx, "backup_add", backupParams{SessionID: s.sessionID, Name: name, URL: url}, nil)
}

func (s *Store) BackupSync(ctx context.Context, name string) error {
	return s.client.request(ctx, "backup_sync", backupParams{SessionID: s.sessionID, Name: name}, nil)
}

func (s *Store) BackupRemove(ctx context.Context, name string) error {
	return s.client.request(ctx, "backup_remove", backupParams{SessionID: s.sessionID, Name: name}, nil)
}

func (s *Store) BackupDatabase(ctx context.Context, dir string) error {
	return s.client.request(ctx, "backup_database", backupParams{SessionID: s.sessionID, Dir: dir}, nil)
}

func (s *Store) RestoreDatabase(ctx context.Context, dir string, force bool) error {
	return s.client.request(ctx, "restore_database", backupParams{SessionID: s.sessionID, Dir: dir, Force: force}, nil)
}

func (s *Store) AddFederationPeer(ctx context.Context, peer *storage.FederationPeer) error {
	return s.client.request(ctx, "add_federation_peer", federationPeerParams{SessionID: s.sessionID, Peer: peer}, nil)
}

func (s *Store) GetFederationPeer(ctx context.Context, name string) (*storage.FederationPeer, error) {
	var out storage.FederationPeer
	if err := s.client.request(ctx, "get_federation_peer", federationPeerParams{SessionID: s.sessionID, Name: name}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *Store) ListFederationPeers(ctx context.Context) ([]*storage.FederationPeer, error) {
	var out []*storage.FederationPeer
	if err := s.client.request(ctx, "list_federation_peers", sessionParams{SessionID: s.sessionID}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) RemoveFederationPeer(ctx context.Context, name string) error {
	return s.client.request(ctx, "remove_federation_peer", federationPeerParams{SessionID: s.sessionID, Name: name}, nil)
}

func (s *Store) SyncStatus(ctx context.Context, peer string) (*storage.SyncStatus, error) {
	var out storage.SyncStatus
	if err := s.client.request(ctx, "sync_status", refParams{SessionID: s.sessionID, Peer: peer}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *Store) Sync(ctx context.Context, peer string, strategy string) (*storage.SyncResult, error) {
	return nil, fmt.Errorf("backend plugin %q does not support sync(peer=%q, strategy=%q)", s.client.Hello().Backend, peer, strategy)
}

//go:build bench

package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	sqlitestorage "github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// BenchmarkDirectCreate benchmarks direct SQLite create operations
func BenchmarkDirectCreate(b *testing.B) {
	tmpDir, err := os.MkdirTemp("", "bd-bench-direct-*")
	if err != nil {
		b.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := sqlitestorage.New(context.Background(), dbPath)
	if err != nil {
		b.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		issue := &types.Issue{
			Title:       fmt.Sprintf("Benchmark Issue %d", i),
			Description: "Benchmark description",
			IssueType:   "task",
			Priority:    2,
			Status:      types.StatusOpen,
		}
		if err := store.CreateIssue(ctx, issue, "benchmark"); err != nil {
			b.Fatalf("Failed to create issue: %v", err)
		}
	}
}

// BenchmarkDaemonCreate benchmarks RPC create operations
func BenchmarkDaemonCreate(b *testing.B) {
	_, client, cleanup, _ := setupBenchServer(b)
	defer cleanup()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		args := &CreateArgs{
			Title:       fmt.Sprintf("Benchmark Issue %d", i),
			Description: "Benchmark description",
			IssueType:   "task",
			Priority:    2,
		}
		if _, err := client.Create(args); err != nil {
			b.Fatalf("Failed to create issue: %v", err)
		}
	}
}

// BenchmarkDirectUpdate benchmarks direct SQLite update operations
func BenchmarkDirectUpdate(b *testing.B) {
	tmpDir, err := os.MkdirTemp("", "bd-bench-direct-update-*")
	if err != nil {
		b.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := sqlitestorage.New(context.Background(), dbPath)
	if err != nil {
		b.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	issue := &types.Issue{
		Title:       "Test Issue",
		Description: "Test description",
		IssueType:   "task",
		Priority:    2,
		Status:      types.StatusOpen,
	}
	if err := store.CreateIssue(ctx, issue, "benchmark"); err != nil {
		b.Fatalf("Failed to create issue: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		updates := map[string]interface{}{
			"title": fmt.Sprintf("Updated Issue %d", i),
		}
		if err := store.UpdateIssue(ctx, issue.ID, updates, "benchmark"); err != nil {
			b.Fatalf("Failed to update issue: %v", err)
		}
	}
}

// BenchmarkDaemonUpdate benchmarks RPC update operations
func BenchmarkDaemonUpdate(b *testing.B) {
	_, client, cleanup, _ := setupBenchServer(b)
	defer cleanup()

	createArgs := &CreateArgs{
		Title:       "Test Issue",
		Description: "Test description",
		IssueType:   "task",
		Priority:    2,
	}

	resp, err := client.Create(createArgs)
	if err != nil {
		b.Fatalf("Failed to create issue: %v", err)
	}

	var issue types.Issue
	if err := json.Unmarshal(resp.Data, &issue); err != nil {
		b.Fatalf("Failed to unmarshal issue: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		newTitle := fmt.Sprintf("Updated Issue %d", i)
		args := &UpdateArgs{
			ID:    issue.ID,
			Title: &newTitle,
		}
		if _, err := client.Update(args); err != nil {
			b.Fatalf("Failed to update issue: %v", err)
		}
	}
}

// BenchmarkDirectList benchmarks direct SQLite list operations
func BenchmarkDirectList(b *testing.B) {
	tmpDir, err := os.MkdirTemp("", "bd-bench-direct-list-*")
	if err != nil {
		b.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := sqlitestorage.New(context.Background(), dbPath)
	if err != nil {
		b.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	for i := 0; i < 100; i++ {
		issue := &types.Issue{
			Title:       fmt.Sprintf("Issue %d", i),
			Description: "Test description",
			IssueType:   "task",
			Priority:    2,
			Status:      types.StatusOpen,
		}
		if err := store.CreateIssue(ctx, issue, "benchmark"); err != nil {
			b.Fatalf("Failed to create issue: %v", err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		filter := types.IssueFilter{Limit: 50}
		if _, err := store.SearchIssues(ctx, "", filter); err != nil {
			b.Fatalf("Failed to list issues: %v", err)
		}
	}
}

// BenchmarkDaemonList benchmarks RPC list operations
func BenchmarkDaemonList(b *testing.B) {
	_, client, cleanup, _ := setupBenchServer(b)
	defer cleanup()

	for i := 0; i < 100; i++ {
		args := &CreateArgs{
			Title:       fmt.Sprintf("Issue %d", i),
			Description: "Test description",
			IssueType:   "task",
			Priority:    2,
		}
		if _, err := client.Create(args); err != nil {
			b.Fatalf("Failed to create issue: %v", err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		args := &ListArgs{Limit: 50}
		if _, err := client.List(args); err != nil {
			b.Fatalf("Failed to list issues: %v", err)
		}
	}
}

// BenchmarkDaemonLatency measures round-trip latency
func BenchmarkDaemonLatency(b *testing.B) {
	_, client, cleanup, _ := setupBenchServer(b)
	defer cleanup()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := client.Ping(); err != nil {
			b.Fatalf("Ping failed: %v", err)
		}
	}
}

// BenchmarkConcurrentAgents benchmarks concurrent agent throughput
func BenchmarkConcurrentAgents(b *testing.B) {
	server, _, cleanup, dbPath := setupBenchServer(b)
	defer cleanup()

	numAgents := 4
	opsPerAgent := b.N / numAgents

	b.ResetTimer()

	done := make(chan bool, numAgents)
	for i := 0; i < numAgents; i++ {
		go func() {
			client, err := TryConnect(server.socketPath)
			if err != nil {
				b.Errorf("Failed to connect: %v", err)
				done <- false
				return
			}
			defer client.Close()

			// Set dbPath so client validates it's connected to the right daemon
			client.dbPath = dbPath

			for j := 0; j < opsPerAgent; j++ {
				args := &CreateArgs{
					Title:     fmt.Sprintf("Issue %d", j),
					IssueType: "task",
					Priority:  2,
				}
				if _, err := client.Create(args); err != nil {
					b.Errorf("Failed to create issue: %v", err)
					done <- false
					return
				}
			}
			done <- true
		}()
	}

	for i := 0; i < numAgents; i++ {
		<-done
	}
}

func setupBenchServer(b *testing.B) (*Server, *Client, func(), string) {
	tmpDir, err := os.MkdirTemp("", "bd-rpc-bench-*")
	if err != nil {
		b.Fatalf("Failed to create temp dir: %v", err)
	}

	// Create .beads subdirectory so findDatabaseForCwd finds THIS database, not project's
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		os.RemoveAll(tmpDir)
		b.Fatalf("Failed to create .beads dir: %v", err)
	}

	dbPath := filepath.Join(beadsDir, "test.db")
	socketPath := filepath.Join(beadsDir, "bd.sock")

	store, err := sqlitestorage.New(context.Background(), dbPath)
	if err != nil {
		os.RemoveAll(tmpDir)
		b.Fatalf("Failed to create store: %v", err)
	}

	server := NewServer(socketPath, store, tmpDir, dbPath)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		if err := server.Start(ctx); err != nil && err.Error() != "accept unix "+socketPath+": use of closed network connection" {
			b.Logf("Server error: %v", err)
		}
	}()

	time.Sleep(100 * time.Millisecond)

	// Change to tmpDir so client's os.Getwd() finds the test database
	b.Chdir(tmpDir)

	client, err := TryConnect(socketPath)
	if err != nil {
		cancel()
		server.Stop()
		store.Close()
		os.RemoveAll(tmpDir)
		b.Fatalf("Failed to connect client: %v", err)
	}

	// Set the client's dbPath to the test database so it doesn't route to the wrong DB
	client.dbPath = dbPath

	cleanup := func() {
		client.Close()
		cancel()
		server.Stop()
		store.Close()
		os.RemoveAll(tmpDir)
	}

	return server, client, cleanup, dbPath
}

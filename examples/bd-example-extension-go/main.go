package main

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/steveyegge/beads/internal/beads"
)

//go:embed schema.sql
var schema string

func main() {
	dbPath := flag.String("db", "", "Database path (default: auto-discover)")
	flag.Parse()

	if *dbPath == "" {
		*dbPath = beads.FindDatabasePath()
	}
	if *dbPath == "" {
		log.Fatal("No database found. Run 'bd init'")
	}

	// Open bd storage + extension database
	store, _ := beads.NewSQLiteStorage(*dbPath)
	defer store.Close()
	db, _ := sql.Open("sqlite3", *dbPath)
	defer db.Close()
	db.Exec("PRAGMA journal_mode=WAL")
	db.Exec("PRAGMA busy_timeout=5000")
	db.Exec(schema) // Initialize extension schema

	// Get ready work
	ctx := context.Background()
	readyIssues, _ := store.GetReadyWork(ctx, beads.WorkFilter{Limit: 1})
	if len(readyIssues) == 0 {
		fmt.Println("No ready work")
		return
	}

	issue := readyIssues[0]
	fmt.Printf("Claiming: %s\n", issue.ID)

	// Create execution record
	result, _ := db.Exec(`INSERT INTO example_executions (issue_id, status, agent_id, started_at)
		VALUES (?, 'running', 'demo-agent', ?)`, issue.ID, time.Now())
	execID, _ := result.LastInsertId()

	// Update issue in bd
	store.UpdateIssue(ctx, issue.ID, map[string]interface{}{"status": beads.StatusInProgress}, "demo-agent")

	// Create checkpoints
	for _, phase := range []string{"assess", "implement", "test"} {
		data, _ := json.Marshal(map[string]interface{}{"phase": phase, "time": time.Now()})
		db.Exec(`INSERT INTO example_checkpoints (execution_id, phase, checkpoint_data) VALUES (?, ?, ?)`,
			execID, phase, string(data))
		fmt.Printf("  ✓ %s\n", phase)
	}

	// Complete
	db.Exec(`UPDATE example_executions SET status='completed', completed_at=? WHERE id=?`, time.Now(), execID)
	store.CloseIssue(ctx, issue.ID, "Done", "demo-agent")

	// Show status
	fmt.Println("\nStatus:")
	rows, _ := db.Query(`
		SELECT i.id, i.title, i.status, e.agent_id, COUNT(c.id)
		FROM issues i
		LEFT JOIN example_executions e ON i.id = e.issue_id
		LEFT JOIN example_checkpoints c ON e.id = c.execution_id
		GROUP BY i.id, e.id
		ORDER BY i.priority
		LIMIT 5`)
	defer rows.Close()

	for rows.Next() {
		var id, title, status string
		var agent sql.NullString
		var checkpoints int
		rows.Scan(&id, &title, &status, &agent, &checkpoints)
		agentStr := "-"
		if agent.Valid {
			agentStr = agent.String
		}
		fmt.Printf("  %s: %s [%s] agent=%s checkpoints=%d\n", id, title, status, agentStr, checkpoints)
	}
}

package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
)

//go:embed web
var webFiles embed.FS

var (
	// Command-line flags
	port       = flag.Int("port", 8080, "Port for web server")
	host       = flag.String("host", "localhost", "Host to bind to")
	dbPath     = flag.String("db", "", "Path to beads database (optional, will auto-detect)")
	socketPath = flag.String("socket", "", "Path to daemon socket (optional, will auto-detect)")
	devMode    = flag.Bool("dev", false, "Run in development mode (serve web files from disk)")

	// WebSocket upgrader
	upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(r *http.Request) bool {
			// Allow all origins for simplicity (consider restricting in production)
			return true
		},
	}

	// WebSocket client management
	wsClients   = make(map[*websocket.Conn]bool)
	wsClientsMu sync.Mutex
	wsBroadcast = make(chan []byte, 256)

	// RPC client for daemon communication
	daemonClient *rpc.Client

	// File system for web files
	webFS fs.FS
)

func main() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "PANIC in main: %v\n", r)
		}
		fmt.Println("Main function exiting")
	}()

	flag.Parse()

	// Set up web file system
	if *devMode {
		fmt.Println("⚠️  Running in DEVELOPMENT mode: serving web files from disk")
		webFS = os.DirFS("web")
	} else {
		var err error
		webFS, err = fs.Sub(webFiles, "web")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error accessing embedded web files: %v\n", err)
			os.Exit(1)
		}
	}

	// Find database path if not specified
	dbPathResolved := *dbPath
	if dbPathResolved == "" {
		if foundDB := beads.FindDatabasePath(); foundDB != "" {
			dbPathResolved = foundDB
		} else {
			fmt.Fprintf(os.Stderr, "Error: no beads database found\n")
			fmt.Fprintf(os.Stderr, "Hint: run 'bd init' to create a database in the current directory\n")
			fmt.Fprintf(os.Stderr, "Or specify database path with -db flag\n")
			os.Exit(1)
		}
	}

	// Resolve socket path
	socketPathResolved := *socketPath
	if socketPathResolved == "" {
		socketPathResolved = getSocketPath(dbPathResolved)
	}

	// Connect to daemon
	if err := connectToDaemon(socketPathResolved, dbPathResolved); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Start WebSocket broadcaster
	go handleWebSocketBroadcast()

	// Start mutation polling
	go pollMutations()

	// Set up HTTP routes
	http.HandleFunc("/", handleIndex)
	http.HandleFunc("/api/issues", handleAPIIssues)
	http.HandleFunc("/api/issues/", handleAPIIssueDetail)
	http.HandleFunc("/api/ready", handleAPIReady)
	http.HandleFunc("/api/stats", handleAPIStats)
	http.HandleFunc("/ws", handleWebSocket)

	// Serve static files
	http.Handle("/static/", http.StripPrefix("/", http.FileServer(http.FS(webFS))))

	addr := fmt.Sprintf("%s:%d", *host, *port)
	fmt.Printf("🖥️  bd monitor-webui starting on http://%s\n", addr)
	fmt.Printf("📊 Open your browser to view real-time issue tracking\n")
	fmt.Printf("🔌 WebSocket endpoint available at ws://%s/ws\n", addr)
	fmt.Printf("Press Ctrl+C to stop\n\n")

	if err := http.ListenAndServe(addr, nil); err != nil {
		fmt.Fprintf(os.Stderr, "Error starting server: %v\n", err)
		os.Exit(1)
	}
}

// getSocketPath returns the Unix socket path for the daemon
func getSocketPath(dbPath string) string {
	// The daemon always creates the socket as "bd.sock" in the same directory as the database
	dbDir := filepath.Dir(dbPath)
	return filepath.Join(dbDir, "bd.sock")
}

// connectToDaemon establishes connection to the daemon
func connectToDaemon(socketPath, dbPath string) error {
	client, err := rpc.TryConnect(socketPath)
	if err != nil || client == nil {
		return fmt.Errorf("bd monitor-webui requires the daemon to be running\n\n"+
			"The monitor uses the daemon's RPC interface to avoid database locking conflicts.\n"+
			"Please start the daemon first:\n\n"+
			"  bd daemon\n\n"+
			"Then start the monitor:\n\n"+
			"  %s\n", os.Args[0])
	}

	// Check daemon health
	health, err := client.Health()
	if err != nil || health.Status != "healthy" {
		_ = client.Close()
		if err != nil {
			return fmt.Errorf("daemon health check failed: %v", err)
		}
		errMsg := fmt.Sprintf("daemon is not healthy (status: %s)", health.Status)
		if health.Error != "" {
			errMsg += fmt.Sprintf("\nError: %s", health.Error)
		}
		return fmt.Errorf("%s\n\nTry restarting the daemon:\n  bd daemon stop\n  bd daemon", errMsg)
	}

	// Set database path
	absDBPath, _ := filepath.Abs(dbPath)
	client.SetDatabasePath(absDBPath)

	daemonClient = client

	fmt.Printf("✓ Connected to daemon (version %s)\n", health.Version)
	return nil
}

// handleIndex serves the main HTML page
func handleIndex(w http.ResponseWriter, r *http.Request) {
	// Only serve index for root path
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	data, err := fs.ReadFile(webFS, "index.html")
	if err != nil {
		http.Error(w, "Error reading index.html", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

// handleAPIIssues returns all issues as JSON
func handleAPIIssues(w http.ResponseWriter, r *http.Request) {
	var issues []*types.Issue

	if daemonClient == nil {
		http.Error(w, "Daemon client not initialized", http.StatusInternalServerError)
		return
	}

	// Use RPC to get issues from daemon
	resp, err := daemonClient.List(&rpc.ListArgs{})
	if err != nil {
		http.Error(w, fmt.Sprintf("Error fetching issues via RPC: %v", err), http.StatusInternalServerError)
		return
	}

	if err := json.Unmarshal(resp.Data, &issues); err != nil {
		http.Error(w, fmt.Sprintf("Error unmarshaling issues: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(issues)
}

// handleAPIIssueDetail returns a single issue's details
func handleAPIIssueDetail(w http.ResponseWriter, r *http.Request) {
	// Extract issue ID from URL path (e.g., /api/issues/bd-1)
	issueID := r.URL.Path[len("/api/issues/"):]
	if issueID == "" {
		http.Error(w, "Issue ID required", http.StatusBadRequest)
		return
	}

	if daemonClient == nil {
		http.Error(w, "Daemon client not initialized", http.StatusInternalServerError)
		return
	}

	var issue *types.Issue

	// Use RPC to get issue from daemon
	resp, err := daemonClient.Show(&rpc.ShowArgs{ID: issueID})
	if err != nil {
		http.Error(w, fmt.Sprintf("Issue not found: %v", err), http.StatusNotFound)
		return
	}

	if err := json.Unmarshal(resp.Data, &issue); err != nil {
		http.Error(w, fmt.Sprintf("Error unmarshaling issue: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(issue)
}

// handleAPIReady returns ready work (no blockers)
func handleAPIReady(w http.ResponseWriter, r *http.Request) {
	var issues []*types.Issue

	if daemonClient == nil {
		http.Error(w, "Daemon client not initialized", http.StatusInternalServerError)
		return
	}

	// Use RPC to get ready work from daemon
	resp, err := daemonClient.Ready(&rpc.ReadyArgs{})
	if err != nil {
		http.Error(w, fmt.Sprintf("Error fetching ready work via RPC: %v", err), http.StatusInternalServerError)
		return
	}

	if err := json.Unmarshal(resp.Data, &issues); err != nil {
		http.Error(w, fmt.Sprintf("Error unmarshaling issues: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(issues)
}

// handleAPIStats returns issue statistics
func handleAPIStats(w http.ResponseWriter, r *http.Request) {
	var stats *types.Statistics

	if daemonClient == nil {
		http.Error(w, "Daemon client not initialized", http.StatusInternalServerError)
		return
	}

	// Use RPC to get stats from daemon
	resp, err := daemonClient.Stats()
	if err != nil {
		http.Error(w, fmt.Sprintf("Error fetching statistics via RPC: %v", err), http.StatusInternalServerError)
		return
	}

	if err := json.Unmarshal(resp.Data, &stats); err != nil {
		http.Error(w, fmt.Sprintf("Error unmarshaling statistics: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// handleWebSocket upgrades HTTP connection to WebSocket and manages client lifecycle
func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	// Upgrade connection to WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error upgrading to WebSocket: %v\n", err)
		return
	}

	// Register client
	wsClientsMu.Lock()
	wsClients[conn] = true
	wsClientsMu.Unlock()

	fmt.Printf("WebSocket client connected (total: %d)\n", len(wsClients))

	// Handle client disconnection
	defer func() {
		wsClientsMu.Lock()
		delete(wsClients, conn)
		wsClientsMu.Unlock()
		conn.Close()
		fmt.Printf("WebSocket client disconnected (total: %d)\n", len(wsClients))
	}()

	// Keep connection alive and handle client messages
	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			break
		}
	}
}

// handleWebSocketBroadcast sends messages to all connected WebSocket clients
func handleWebSocketBroadcast() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "PANIC in handleWebSocketBroadcast: %v\n", r)
		}
	}()
	for {
		// Wait for message to broadcast
		message := <-wsBroadcast

		// Send to all connected clients
		wsClientsMu.Lock()
		for client := range wsClients {
			err := client.WriteMessage(websocket.TextMessage, message)
			if err != nil {
				// Client disconnected, will be cleaned up by handleWebSocket
				fmt.Fprintf(os.Stderr, "Error writing to WebSocket client: %v\n", err)
				client.Close()
				delete(wsClients, client)
			}
		}
		wsClientsMu.Unlock()
	}
}

// pollMutations polls the daemon for mutations and broadcasts them to WebSocket clients
func pollMutations() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "PANIC in pollMutations: %v\n", r)
		}
	}()
	lastPollTime := int64(0) // Start from beginning

	ticker := time.NewTicker(2 * time.Second) // Poll every 2 seconds
	defer ticker.Stop()

	for range ticker.C {
		if daemonClient == nil {
			continue
		}

		// Call GetMutations RPC
		resp, err := daemonClient.GetMutations(&rpc.GetMutationsArgs{
			Since: lastPollTime,
		})
		if err != nil {
			// Daemon might be down or restarting, just skip this poll
			continue
		}

		var mutations []rpc.MutationEvent
		if err := json.Unmarshal(resp.Data, &mutations); err != nil {
			fmt.Fprintf(os.Stderr, "Error unmarshaling mutations: %v\n", err)
			continue
		}

		// Broadcast each mutation to WebSocket clients
		for _, mutation := range mutations {
			data, _ := json.Marshal(mutation)
			wsBroadcast <- data

			// Update last poll time to this mutation's timestamp
			mutationTimeMillis := mutation.Timestamp.UnixMilli()
			if mutationTimeMillis > lastPollTime {
				lastPollTime = mutationTimeMillis
			}
		}
	}
}

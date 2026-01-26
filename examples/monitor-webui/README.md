# Monitor WebUI - Real-time Issue Tracking Dashboard

A standalone web-based monitoring interface for beads that provides real-time issue tracking through a clean, responsive web UI.

## Overview

The Monitor WebUI is a separate runtime that connects to the beads daemon via RPC to provide:

- **Real-time updates** via WebSocket connections
- **Responsive design** with desktop table view and mobile card view
- **Issue filtering** by status and priority
- **Statistics dashboard** showing issue counts by status
- **Detailed issue views** with full metadata
- **Clean, modern UI** styled with Milligram CSS

## Architecture

The Monitor WebUI demonstrates how to build custom interfaces on top of beads using:

- **RPC Protocol**: Connects to the daemon's Unix socket for database operations
- **WebSocket Broadcasting**: Polls mutation events and broadcasts to connected clients
- **Embedded Web Assets**: HTML, CSS, and JavaScript served from the binary
- **Standalone Binary**: Runs independently from the `bd` CLI

## Prerequisites

Before running the monitor, you must have:

1. A beads database initialized (run `bd init` in your project)
2. The beads daemon running (run `bd daemon`)

## Building

From this directory:

```bash
go build
```

Or using bun (if available):

```bash
bun run go build
```

This creates a `monitor-webui` binary in the current directory.

## Usage

### Basic Usage

Start the monitor on default port 8080:

```bash
./monitor-webui
```

Then open your browser to http://localhost:8080

### Custom Port

Start on a different port:

```bash
./monitor-webui -port 3000
```

### Bind to All Interfaces

To access from other machines on your network:

```bash
./monitor-webui -host 0.0.0.0 -port 8080
```

### Custom Database Path

If your database is not in the current directory:

```bash
./monitor-webui -db /path/to/your/beads.db
```

### Custom Socket Path

If you need to specify a custom daemon socket:

```bash
./monitor-webui -socket /path/to/beads.db.sock
```

## Command-Line Flags

- `-port` - Port for web server (default: 8080)
- `-host` - Host to bind to (default: "localhost")
- `-db` - Path to beads database (optional, will auto-detect)
- `-socket` - Path to daemon socket (optional, will auto-detect)

## API Endpoints

The monitor exposes several HTTP endpoints:

### Web UI
- `GET /` - Main HTML interface
- `GET /static/*` - Static assets (CSS, JavaScript)

### REST API
- `GET /api/issues` - List all issues as JSON
- `GET /api/issues/:id` - Get specific issue details
- `GET /api/ready` - Get ready work (no blockers)
- `GET /api/stats` - Get issue statistics

### WebSocket
- `WS /ws` - WebSocket endpoint for real-time updates

## Features

### Real-time Updates

The monitor polls the daemon every 2 seconds for mutation events and broadcasts them to all connected WebSocket clients. This provides instant updates when issues are created, modified, or closed.

### Responsive Design

- **Desktop**: Full table view with sortable columns
- **Mobile**: Card-based view optimized for small screens
- **Tablet**: Adapts to medium screen sizes

### Filtering

- **Status Filter**: Multi-select for Open, In Progress, and Closed
- **Priority Filter**: Single-select for P1, P2, P3, or All

### Statistics

Real-time statistics showing:
- Total issues
- In-progress issues
- Open issues
- Closed issues

## Development

### Project Structure

```
monitor-webui/
├── main.go              # Main application with HTTP server and RPC client
├── go.mod               # Go module dependencies
├── go.sum               # (generated) Dependency checksums
├── README.md            # This file
└── web/                 # Web assets (embedded in binary)
    ├── index.html       # Main HTML page
    └── static/
        ├── css/
        │   └── styles.css   # Custom styles
        └── js/
            └── app.js       # JavaScript application logic
```

### Modifying the Web Assets

The HTML, CSS, and JavaScript files are embedded into the binary using Go's `embed` package. After making changes to files in the `web/` directory, rebuild the binary to see your changes.

### Extending the API

To add new API endpoints:

1. Define a new handler function in `main.go`
2. Register it with `http.HandleFunc()` in the `main()` function
3. Use `daemonClient` to make RPC calls to the daemon
4. Return JSON responses using `json.NewEncoder(w).Encode()`

## Deployment

### As a Standalone Service

You can run the monitor as a systemd service. Example service file:

```ini
[Unit]
Description=Beads Monitor WebUI
After=network.target

[Service]
Type=simple
User=youruser
WorkingDirectory=/path/to/your/project
ExecStart=/path/to/monitor-webui -host 0.0.0.0 -port 8080
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
```

Save as `/etc/systemd/system/beads-monitor.service` and enable:

```bash
sudo systemctl enable beads-monitor
sudo systemctl start beads-monitor
```

### Behind a Reverse Proxy

Example nginx configuration:

```nginx
server {
    listen 80;
    server_name monitor.example.com;

    location / {
        proxy_pass http://localhost:8080;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
    }
}
```

## Troubleshooting

### "No beads database found"

Make sure you've initialized a beads database with `bd init` or specify the database path with `-db`.

### "Daemon is not running"

The monitor requires the daemon to avoid SQLite locking conflicts. Start the daemon first:

```bash
bd daemon start
```

### WebSocket disconnects frequently

Check if there's a reverse proxy or firewall between the client and server that might be closing idle connections. Consider adjusting timeout settings.

### Port already in use

If port 8080 is already in use, specify a different port:

```bash
./monitor-webui -port 3001
```

## Security Considerations

### Production Deployment

When deploying to production:

1. **Restrict Origins**: Update the `CheckOrigin` function in `main.go` to validate WebSocket origins
2. **Use HTTPS**: Deploy behind a reverse proxy with TLS (nginx, Caddy, etc.)
3. **Authentication**: Add authentication middleware if exposing publicly
4. **Firewall**: Use firewall rules to restrict access to trusted networks

### Current Security Model

The current implementation:
- Allows WebSocket connections from any origin
- Provides read-only access to issue data
- Does not include authentication
- Connects to local daemon socket only

This is appropriate for local development but requires additional security measures for production use.

## License

Same as the main beads project.

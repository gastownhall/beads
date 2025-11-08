#!/bin/bash
# Stop Agent Mail server
# Usage: ./stop-agent-mail-server.sh

PID_FILE="${AGENT_MAIL_PID:-$HOME/agent-mail.pid}"

if [[ ! -f "$PID_FILE" ]]; then
    echo "⚠️  No PID file found: $PID_FILE"
    echo "   Attempting to kill by process name..."
    
    if pkill -f "mcp_agent_mail.cli serve-http"; then
        echo "✅ Killed Agent Mail server by process name"
    else
        echo "ℹ️  No Agent Mail server process found"
    fi
    exit 0
fi

PID=$(cat "$PID_FILE")

if ! ps -p "$PID" > /dev/null 2>&1; then
    echo "⚠️  Process $PID not running (stale PID file)"
    rm -f "$PID_FILE"
    exit 0
fi

echo "🛑 Stopping Agent Mail server (PID: $PID)..."
kill "$PID"

# Wait for graceful shutdown
for i in {1..5}; do
    if ! ps -p "$PID" > /dev/null 2>&1; then
        echo "✅ Server stopped gracefully"
        rm -f "$PID_FILE"
        exit 0
    fi
    sleep 1
done

# Force kill if needed
if ps -p "$PID" > /dev/null 2>&1; then
    echo "⚠️  Server didn't stop gracefully, forcing..."
    kill -9 "$PID"
    sleep 1
fi

if ! ps -p "$PID" > /dev/null 2>&1; then
    echo "✅ Server stopped (forced)"
    rm -f "$PID_FILE"
else
    echo "❌ Failed to stop server"
    exit 1
fi

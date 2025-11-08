#!/bin/bash
# View Agent Mail server status and active projects
# Usage: ./agent-mail-status.sh

PORT="${AGENT_MAIL_PORT:-8765}"
URL="http://127.0.0.1:$PORT"
PID_FILE="${AGENT_MAIL_PID:-$HOME/agent-mail.pid}"

echo "=== Agent Mail Status ==="
echo ""

# Check PID file
if [[ -f "$PID_FILE" ]]; then
    PID=$(cat "$PID_FILE")
    if ps -p "$PID" > /dev/null 2>&1; then
        echo "Server Process: ✅ Running (PID: $PID)"
    else
        echo "Server Process: ⚠️  Stale PID file (process $PID not found)"
    fi
else
    echo "Server Process: ⚠️  No PID file found"
fi

# Check health endpoint
echo ""
echo "Server Health:"
if HEALTH=$(curl -sf "$URL/health" 2>/dev/null); then
    echo "  ✅ $(echo $HEALTH | jq -r '.status // "OK"')"
    echo "  URL: $URL"
else
    echo "  ❌ UNREACHABLE at $URL"
    echo ""
    echo "Start server with:"
    echo "  ./scripts/start-agent-mail-server.sh"
    exit 1
fi

# List projects
echo ""
echo "Active Projects:"
if PROJECTS=$(curl -sf "$URL/api/projects" 2>/dev/null); then
    if [[ $(echo "$PROJECTS" | jq -r '. | length') -eq 0 ]]; then
        echo "  (none yet)"
    else
        echo "$PROJECTS" | jq -r '.[] | "  • \(.slug)\n    Path: \(.human_key)"'
    fi
else
    echo "  (failed to fetch)"
fi

# List reservations
echo ""
echo "Active File Reservations:"
if RESERVATIONS=$(curl -sf "$URL/api/file_reservations" 2>/dev/null); then
    if [[ $(echo "$RESERVATIONS" | jq -r '. | length') -eq 0 ]]; then
        echo "  (none)"
    else
        echo "$RESERVATIONS" | jq -r '.[] | "  • \(.agent_name) → \(.resource_id)\n    Project: \(.project_id)\n    Expires: \(.expires_at)"'
    fi
else
    echo "  (failed to fetch)"
fi

echo ""
echo "Web UI: $URL/mail"
echo ""
echo "Commands:"
echo "  Start:  ./scripts/start-agent-mail-server.sh"
echo "  Stop:   ./scripts/stop-agent-mail-server.sh"
echo "  Logs:   tail -f $HOME/agent-mail.log"

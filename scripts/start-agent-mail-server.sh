#!/bin/bash
# Start Agent Mail server in background
# Usage: ./start-agent-mail-server.sh

set -e

AGENT_MAIL_DIR="${AGENT_MAIL_DIR:-$HOME/src/mcp_agent_mail}"
LOG_FILE="${AGENT_MAIL_LOG:-$HOME/agent-mail.log}"
PID_FILE="${AGENT_MAIL_PID:-$HOME/agent-mail.pid}"
PORT="${AGENT_MAIL_PORT:-8765}"

# Check if server already running
if [[ -f "$PID_FILE" ]]; then
    PID=$(cat "$PID_FILE")
    if ps -p "$PID" > /dev/null 2>&1; then
        echo "⚠️  Agent Mail server already running (PID: $PID)"
        echo "   Stop it first: kill $PID"
        exit 1
    else
        echo "🗑️  Removing stale PID file"
        rm -f "$PID_FILE"
    fi
fi

# Check if directory exists
if [[ ! -d "$AGENT_MAIL_DIR" ]]; then
    echo "❌ Agent Mail directory not found: $AGENT_MAIL_DIR"
    echo ""
    echo "Install with:"
    echo "  git clone https://github.com/Dicklesworthstone/mcp_agent_mail.git $AGENT_MAIL_DIR"
    echo "  cd $AGENT_MAIL_DIR"
    echo "  python3 -m venv .venv"
    echo "  source .venv/bin/activate"
    echo "  pip install -e ."
    exit 1
fi

# Check if venv exists
if [[ ! -d "$AGENT_MAIL_DIR/.venv" ]]; then
    echo "❌ Virtual environment not found in $AGENT_MAIL_DIR/.venv"
    echo ""
    echo "Create with:"
    echo "  cd $AGENT_MAIL_DIR"
    echo "  python3 -m venv .venv"
    echo "  source .venv/bin/activate"
    echo "  pip install -e ."
    exit 1
fi

# Start server
echo "🚀 Starting Agent Mail server..."
echo "   Directory: $AGENT_MAIL_DIR"
echo "   Log file: $LOG_FILE"
echo "   Port: $PORT"

cd "$AGENT_MAIL_DIR"
source .venv/bin/activate

nohup python -m mcp_agent_mail.cli serve-http \
    --host 127.0.0.1 \
    --port "$PORT" \
    > "$LOG_FILE" 2>&1 &

echo $! > "$PID_FILE"

# Wait a moment for server to start
sleep 2

# Check if server is healthy
if curl -sf http://127.0.0.1:$PORT/health > /dev/null; then
    echo "✅ Agent Mail server started successfully!"
    echo "   PID: $(cat $PID_FILE)"
    echo "   Health: http://127.0.0.1:$PORT/health"
    echo "   Web UI: http://127.0.0.1:$PORT/mail"
    echo ""
    echo "View logs:"
    echo "   tail -f $LOG_FILE"
    echo ""
    echo "Stop server:"
    echo "   kill $(cat $PID_FILE)"
else
    echo "❌ Server failed to start"
    echo "   Check logs: tail -f $LOG_FILE"
    rm -f "$PID_FILE"
    exit 1
fi

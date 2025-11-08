#!/bin/bash
# Setup Agent Mail configuration for a beads workspace
# Usage: ./setup-agent-mail-workspace.sh [workspace-path]

set -e

WORKSPACE="${1:-$(pwd)}"
cd "$WORKSPACE"

WORKSPACE_NAME=$(basename "$WORKSPACE")
PARENT=$(basename $(dirname "$WORKSPACE"))
HOSTNAME=$(hostname -s)

# Determine project ID based on workspace type
determine_project_id() {
    local ws_name="$1"
    
    case "$ws_name" in
        beads)
            echo "beads.dev"
            ;;
        vc)
            echo "vc.dev"
            ;;
        wyvern)
            echo "wyvern.dev"
            ;;
        *)
            echo "unknown.dev"
            ;;
    esac
}

PROJECT_ID=$(determine_project_id "$WORKSPACE_NAME")
AGENT_NAME="${PARENT}-${WORKSPACE_NAME}-${HOSTNAME}"

# Create .envrc for direnv
cat > .envrc <<EOF
# Agent Mail Configuration
# Generated: $(date)
# Workspace: $WORKSPACE
# Coupling: $(basename "$PROJECT_ID")

export BEADS_AGENT_MAIL_URL=http://127.0.0.1:8765
export BEADS_AGENT_NAME=$AGENT_NAME
export BEADS_PROJECT_ID=$PROJECT_ID

# Optional: Uncomment for debugging
# export BEADS_AGENT_MAIL_DEBUG=1
EOF

echo "✅ Created .envrc in $WORKSPACE"
echo ""
echo "Configuration:"
echo "  BEADS_AGENT_MAIL_URL: http://127.0.0.1:8765"
echo "  BEADS_AGENT_NAME: $AGENT_NAME"
echo "  BEADS_PROJECT_ID: $PROJECT_ID"
echo ""
echo "Next steps:"
echo "  1. Review .envrc and adjust if needed"
echo "  2. Run: direnv allow"
echo "  3. Test: bd info | grep -i agent"
echo ""
echo "To install direnv:"
echo "  brew install direnv"
echo "  echo 'eval \"\$(direnv hook zsh)\"' >> ~/.zshrc"
echo "  source ~/.zshrc"

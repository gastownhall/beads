#!/usr/bin/env bash
#
# Beads (bd) installation script
# Usage: curl -fsSL https://raw.githubusercontent.com/steveyegge/beads/main/install.sh | bash
#

set -e

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${BLUE}==>${NC} $1"
}

log_success() {
    echo -e "${GREEN}==>${NC} $1"
}

log_warning() {
    echo -e "${YELLOW}==>${NC} $1"
}

log_error() {
    echo -e "${RED}Error:${NC} $1" >&2
}

# Detect OS and architecture
detect_platform() {
    local os arch

    case "$(uname -s)" in
        Darwin)
            os="darwin"
            ;;
        Linux)
            os="linux"
            ;;
        *)
            log_error "Unsupported operating system: $(uname -s)"
            exit 1
            ;;
    esac

    case "$(uname -m)" in
        x86_64|amd64)
            arch="amd64"
            ;;
        aarch64|arm64)
            arch="arm64"
            ;;
        *)
            log_error "Unsupported architecture: $(uname -m)"
            exit 1
            ;;
    esac

    echo "${os}-${arch}"
}

# Check if Go is installed and meets minimum version
check_go() {
    if command -v go &> /dev/null; then
        local go_version=$(go version | awk '{print $3}' | sed 's/go//')
        log_info "Go detected: $(go version)"

        # Extract major and minor version numbers
        local major=$(echo "$go_version" | cut -d. -f1)
        local minor=$(echo "$go_version" | cut -d. -f2)

        # Check if Go version is 1.23 or later
        if [ "$major" -eq 1 ] && [ "$minor" -lt 23 ]; then
            log_error "Go 1.23 or later is required (found: $go_version)"
            echo ""
            echo "Please upgrade Go:"
            echo "  - Download from https://go.dev/dl/"
            echo "  - Or use your package manager to update"
            echo ""
            return 1
        fi

        return 0
    else
        return 1
    fi
}

# Install using go install
install_with_go() {
    log_info "Installing bd using 'go install'..."

    if go install github.com/steveyegge/beads/cmd/bd@latest; then
        log_success "bd installed successfully via go install"

        # Check if GOPATH/bin is in PATH
        local gopath_bin="$(go env GOPATH)/bin"
        if [[ ":$PATH:" != *":$gopath_bin:"* ]]; then
            log_warning "GOPATH/bin is not in your PATH"
            echo ""
            echo "Add this to your shell profile (~/.bashrc, ~/.zshrc, etc.):"
            echo "  export PATH=\"\$PATH:$gopath_bin\""
            echo ""
        fi

        return 0
    else
        log_error "go install failed"
        return 1
    fi
}

# Build from source
build_from_source() {
    log_info "Building bd from source..."

    local tmp_dir
    tmp_dir=$(mktemp -d)

    cd "$tmp_dir"
    log_info "Cloning repository..."

    if git clone --depth 1 https://github.com/steveyegge/beads.git; then
        cd beads
        log_info "Building binary..."

        if go build -o bd ./cmd/bd; then
            # Determine install location
            local install_dir
            if [[ -w /usr/local/bin ]]; then
                install_dir="/usr/local/bin"
            else
                install_dir="$HOME/.local/bin"
                mkdir -p "$install_dir"
            fi

            log_info "Installing to $install_dir..."
            if [[ -w "$install_dir" ]]; then
                mv bd "$install_dir/"
            else
                sudo mv bd "$install_dir/"
            fi

            log_success "bd installed to $install_dir/bd"

            # Check if install_dir is in PATH
            if [[ ":$PATH:" != *":$install_dir:"* ]]; then
                log_warning "$install_dir is not in your PATH"
                echo ""
                echo "Add this to your shell profile (~/.bashrc, ~/.zshrc, etc.):"
                echo "  export PATH=\"\$PATH:$install_dir\""
                echo ""
            fi

            cd - > /dev/null
            rm -rf "$tmp_dir"
            return 0
        else
            log_error "Build failed"
            cd - > /dev/null
            rm -rf "$tmp_dir"
            return 1
        fi
    else
        log_error "Failed to clone repository"
        rm -rf "$tmp_dir"
        return 1
    fi
}

# Install Go if not present (optional)
offer_go_installation() {
    log_warning "Go is not installed"
    echo ""
    echo "bd requires Go 1.23 or later. You can:"
    echo "  1. Install Go from https://go.dev/dl/"
    echo "  2. Use your package manager:"
    echo "     - macOS: brew install go"
    echo "     - Ubuntu/Debian: sudo apt install golang"
    echo "     - Other Linux: Check your distro's package manager"
    echo ""
    echo "After installing Go, run this script again."
    exit 1
}

# Verify installation
verify_installation() {
    if command -v bd &> /dev/null; then
        log_success "bd is installed and ready!"
        echo ""
        bd version 2>/dev/null || echo "bd (development build)"
        echo ""
        echo "Get started:"
        echo "  cd your-project"
        echo "  bd init"
        echo "  bd quickstart"
        echo ""
        return 0
    else
        log_error "bd was installed but is not in PATH"
        return 1
    fi
}

# Main installation flow
main() {
    echo ""
    echo "🔗 Beads (bd) Installer"
    echo ""

    log_info "Detecting platform..."
    local platform
    platform=$(detect_platform)
    log_info "Platform: $platform"

    # Try go install first
    if check_go; then
        if install_with_go; then
            verify_installation
            exit 0
        fi
    fi

    # If go install failed or Go not present, try building from source
    log_warning "Falling back to building from source..."

    if ! check_go; then
        offer_go_installation
    fi

    if build_from_source; then
        verify_installation
        exit 0
    fi

    # All methods failed
    log_error "Installation failed"
    echo ""
    echo "Manual installation:"
    echo "  1. Install Go from https://go.dev/dl/"
    echo "  2. Run: go install github.com/steveyegge/beads/cmd/bd@latest"
    echo ""
    exit 1
}

main "$@"

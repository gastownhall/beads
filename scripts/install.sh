#!/usr/bin/env bash
#
# Beads (bd) installation script
# Usage: curl -fsSL https://raw.githubusercontent.com/steveyegge/beads/main/scripts/install.sh | bash
#
# ⚠️ IMPORTANT: This script must be EXECUTED, never SOURCED
# ❌ WRONG: source install.sh (will exit your shell on errors)
# ✅ CORRECT: bash install.sh
# ✅ CORRECT: curl -fsSL ... | bash
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

release_has_asset() {
    local release_json=$1
    local asset_name=$2

    if echo "$release_json" | grep -Fq "\"name\": \"$asset_name\""; then
        return 0
    fi

    return 1
}

# Re-sign binary for macOS to avoid slow Gatekeeper checks
# See: https://github.com/steveyegge/beads/issues/466
resign_for_macos() {
    local binary_path=$1

    # Only run on macOS
    if [[ "$(uname -s)" != "Darwin" ]]; then
        return 0
    fi

    # Check if codesign is available
    if ! command -v codesign &> /dev/null; then
        log_warning "codesign not found, skipping re-signing"
        return 0
    fi

    log_info "Re-signing binary for macOS..."
    codesign --remove-signature "$binary_path" 2>/dev/null || true
    if codesign --force --sign - "$binary_path"; then
        log_success "Binary re-signed for this machine"
    else
        log_warning "Failed to re-sign binary (non-fatal)"
    fi
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
        FreeBSD)
            os="freebsd"
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
        armv7*|armv6*|armhf|arm)
            arch="arm"
            ;;
        *)
            log_error "Unsupported architecture: $(uname -m)"
            exit 1
            ;;
    esac

    echo "${os}_${arch}"
}

# Create 'beads' symlink alias for bd
create_beads_alias() {
    local install_dir=$1

    log_info "Creating 'beads' alias..."
    rm -f "$install_dir/beads"
    if [[ -w "$install_dir" ]]; then
        ln -s bd "$install_dir/beads"
    else
        sudo ln -s bd "$install_dir/beads"
    fi
    log_success "Created 'beads' alias -> bd"
}

# Download and install from GitHub releases
install_from_release() {
    log_info "Installing bd from GitHub releases..."

    local platform=$1
    local tmp_dir
    tmp_dir=$(mktemp -d)

    # Get latest release version
    log_info "Fetching latest release..."
    local latest_url="https://api.github.com/repos/steveyegge/beads/releases/latest"
    local version
    local release_json

    if command -v curl &> /dev/null; then
        release_json=$(curl -fsSL "$latest_url")
    elif command -v wget &> /dev/null; then
        release_json=$(wget -qO- "$latest_url")
    else
        log_error "Neither curl nor wget found. Please install one of them."
        return 1
    fi

    version=$(echo "$release_json" | grep '"tag_name"' | sed -E 's/.*"tag_name": "([^"]+)".*/\1/')

    if [ -z "$version" ]; then
        log_error "Failed to fetch latest version"
        return 1
    fi

    log_info "Latest version: $version"

    # Download URL
    local archive_name="beads_${version#v}_${platform}.tar.gz"
    local download_url="https://github.com/steveyegge/beads/releases/download/${version}/${archive_name}"

    if ! release_has_asset "$release_json" "$archive_name"; then
        log_warning "No prebuilt archive available for platform ${platform}. Falling back to source installation methods."
        rm -rf "$tmp_dir"
        return 1
    fi
    
    log_info "Downloading $archive_name..."
    
    cd "$tmp_dir"
    if command -v curl &> /dev/null; then
        if ! curl -fsSL -o "$archive_name" "$download_url"; then
            log_error "Download failed"
            cd - > /dev/null || cd "$HOME"
            rm -rf "$tmp_dir"
            return 1
        fi
    elif command -v wget &> /dev/null; then
        if ! wget -q -O "$archive_name" "$download_url"; then
            log_error "Download failed"
            cd - > /dev/null || cd "$HOME"
            rm -rf "$tmp_dir"
            return 1
        fi
    fi

    # Extract archive
    log_info "Extracting archive..."
    if ! tar -xzf "$archive_name"; then
        log_error "Failed to extract archive"
        rm -rf "$tmp_dir"
        return 1
    fi

    # Determine install location
    local install_dir
    if [[ -w /usr/local/bin ]]; then
        install_dir="/usr/local/bin"
    else
        install_dir="$HOME/.local/bin"
        mkdir -p "$install_dir"
    fi

    # Install binary
    log_info "Installing to $install_dir..."
    if [[ -w "$install_dir" ]]; then
        mv bd "$install_dir/"
    else
        sudo mv bd "$install_dir/"
    fi

    # Re-sign for macOS to avoid Gatekeeper delays
    resign_for_macos "$install_dir/bd"

    # Create 'beads' alias symlink
    create_beads_alias "$install_dir"

    log_success "bd installed to $install_dir/bd"

    # Check if install_dir is in PATH
    if [[ ":$PATH:" != *":$install_dir:"* ]]; then
        log_warning "$install_dir is not in your PATH"
        echo ""
        echo "Add this to your shell profile (~/.bashrc, ~/.zshrc, etc.):"
        echo "  export PATH=\"\$PATH:$install_dir\""
        echo ""
    fi

    cd - > /dev/null || cd "$HOME"
    rm -rf "$tmp_dir"
    return 0
}

# Check if Go is installed and meets minimum version
check_go() {
    if command -v go &> /dev/null; then
        local go_version=$(go version | awk '{print $3}' | sed 's/go//')
        log_info "Go detected: $(go version)"

        # Extract major and minor version numbers
    local major=$(echo "$go_version" | cut -d. -f1)
    local minor=$(echo "$go_version" | cut -d. -f2)

    # Check if Go version is 1.24 or later
    if [ "$major" -eq 1 ] && [ "$minor" -lt 24 ]; then
        log_error "Go 1.24 or later is required (found: $go_version)"
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

# Verify a built/installed binary has CGO enabled.
verify_binary_has_cgo() {
    local binary_path=$1
    local install_method=$2

    if [[ ! -f "$binary_path" ]]; then
        log_error "Expected binary not found at $binary_path"
        return 1
    fi

    if ! command -v strings &> /dev/null; then
        log_warning "'strings' not found; unable to verify CGO metadata for $binary_path"
        return 0
    fi

    if strings "$binary_path" | awk '/^build[[:space:]]+CGO_ENABLED=0$/ { found=1 } END { exit(found?0:1) }'; then
        log_error "Binary produced by ${install_method} was built without CGO support"
        log_warning "CGO is required for some features. Install ICU headers and retry."
        return 1
    fi

    log_success "Verified CGO support in $binary_path"
    return 0
}

# Install using go install (fallback)
install_with_go() {
    log_info "Installing bd using 'go install'..."

    if CGO_ENABLED=1 go install github.com/steveyegge/beads/cmd/bd@latest; then
        log_success "bd installed successfully via go install"

        # Record where we expect the binary to have been installed
        # Prefer GOBIN if set, otherwise GOPATH/bin
        local gobin
        gobin=$(go env GOBIN 2>/dev/null || true)
        if [ -n "$gobin" ]; then
            bin_dir="$gobin"
        else
            bin_dir="$(go env GOPATH)/bin"
        fi
        LAST_INSTALL_PATH="$bin_dir/bd"

        if ! verify_binary_has_cgo "$LAST_INSTALL_PATH" "go install"; then
            return 1
        fi

        # Re-sign for macOS to avoid Gatekeeper delays
        resign_for_macos "$bin_dir/bd"

        # Create 'beads' alias symlink
        create_beads_alias "$bin_dir"

        # Check if GOPATH/bin (or GOBIN) is in PATH
        if [[ ":$PATH:" != *":$bin_dir:"* ]]; then
            log_warning "$bin_dir is not in your PATH"
            echo ""
            echo "Add this to your shell profile (~/.bashrc, ~/.zshrc, etc.):"
            echo "  export PATH=\"\$PATH:$bin_dir\""
            echo ""
        fi

        return 0
    else
        log_error "go install failed"
        log_warning "If you see 'unicode/uregex.h' missing, install ICU headers (macOS: brew install icu4c; Linux: libicu-dev or libicu-devel) and try again."
        return 1
    fi
}

# Build from source (last resort)
build_from_source() {
    log_info "Building bd from source..."

    local tmp_dir
    tmp_dir=$(mktemp -d)

    cd "$tmp_dir"
    log_info "Cloning repository..."

    if git clone --depth 1 https://github.com/steveyegge/beads.git; then
        cd beads
        log_info "Building binary..."

        if CGO_ENABLED=1 go build -o bd ./cmd/bd; then
            if ! verify_binary_has_cgo "./bd" "source build"; then
                cd - > /dev/null || cd "$HOME"
                rm -rf "$tmp_dir"
                return 1
            fi

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

            # Re-sign for macOS to avoid Gatekeeper delays
            resign_for_macos "$install_dir/bd"

            # Create 'beads' alias symlink
            create_beads_alias "$install_dir"

            log_success "bd installed to $install_dir/bd"

            # Record where we installed the binary when building from source
            LAST_INSTALL_PATH="$install_dir/bd"

            # Check if install_dir is in PATH
            if [[ ":$PATH:" != *":$install_dir:"* ]]; then
                log_warning "$install_dir is not in your PATH"
                echo ""
                echo "Add this to your shell profile (~/.bashrc, ~/.zshrc, etc.):"
                echo "  export PATH=\"\$PATH:$install_dir\""
                echo ""
            fi

            cd - > /dev/null || cd "$HOME"
            rm -rf "$tmp_dir"
            return 0
        else
            log_error "Build failed"
            log_warning "If you see 'unicode/uregex.h' missing, install ICU headers (macOS: brew install icu4c; Linux: libicu-dev or libicu-devel) and try again."
    cd - > /dev/null || cd "$HOME"
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

# Verify installation
verify_installation() {
    # If multiple 'bd' binaries exist on PATH, warn the user before verification
    warn_if_multiple_bd || true

    if command -v bd &> /dev/null; then
        log_success "bd is installed and ready!"
        echo ""
        bd version 2>/dev/null || echo "bd (development build)"
        echo ""
        echo "You can use either 'bd' or 'beads' to run the command."
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

# Returns a list of full paths to 'bd' found in PATH (earlier entries first)
get_bd_paths_in_path() {
    local IFS=':'
    local -a entries
    read -ra entries <<< "$PATH"
    local -a found
    local p
    for p in "${entries[@]}"; do
        [ -z "$p" ] && continue
        if [ -x "$p/bd" ]; then
            # Resolve symlink if possible
            if command -v readlink >/dev/null 2>&1; then
                resolved=$(readlink -f "$p/bd" 2>/dev/null || printf '%s' "$p/bd")
            else
                resolved="$p/bd"
            fi
            # avoid duplicates
            skip=0
            for existing in "${found[@]:-}"; do
                if [ "$existing" = "$resolved" ]; then skip=1; break; fi
            done
            if [ $skip -eq 0 ]; then
                found+=("$resolved")
            fi
        fi
    done
    # print results, one per line
    for item in "${found[@]:-}"; do
        printf '%s\n' "$item"
    done
}

warn_if_multiple_bd() {
    # Use bash 3.2-compatible approach instead of mapfile (bash 4.0+)
    bd_paths=()
    while IFS= read -r line; do
        bd_paths+=("$line")
    done < <(get_bd_paths_in_path)
    if [ "${#bd_paths[@]}" -le 1 ]; then
        return 0
    fi

    log_warning "Multiple 'bd' executables found on your PATH. An older copy may be executed instead of the one we installed."
    echo "Found the following 'bd' executables (entries earlier in PATH take precedence):"
    local i=1
    for p in "${bd_paths[@]}"; do
        local ver
        if [ -x "$p" ]; then
            ver=$("$p" version 2>/dev/null || true)
        fi
        if [ -z "$ver" ]; then ver="<unknown version>"; fi
        echo "  $i. $p  -> $ver"
        i=$((i+1))
    done

    if [ -n "$LAST_INSTALL_PATH" ]; then
        echo ""
        echo "We installed to: $LAST_INSTALL_PATH"
        # Compare first PATH entry vs installed path
        first="${bd_paths[0]}"
        if [ "$first" != "$LAST_INSTALL_PATH" ]; then
            log_warning "The 'bd' executable that appears first in your PATH is different from the one we installed. To make the newly installed 'bd' the one you get when running 'bd', either:"
            echo "  - Remove or rename the older $first from your PATH, or"
            echo "  - Reorder your PATH so that $(dirname "$LAST_INSTALL_PATH") appears before $(dirname "$first")"
            echo "After updating PATH, restart your shell and run 'bd version' to confirm."
        else
            echo "The installed 'bd' is first in your PATH.";
        fi
    else
        log_warning "We couldn't determine where we installed 'bd' during this run.";
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

    # Try downloading from GitHub releases first
    if install_from_release "$platform"; then
        verify_installation
        exit 0
    fi

    log_warning "Failed to install from releases, trying alternative methods..."

    # Try go install as fallback
    if check_go; then
        if install_with_go; then
            verify_installation
            exit 0
        fi
    fi

    # Try building from source as last resort
    log_warning "Falling back to building from source..."

    if ! check_go; then
        log_warning "Go is not installed"
        echo ""
        echo "bd requires Go 1.24 or later to build from source. You can:"
        echo "  1. Install Go from https://go.dev/dl/"
        echo "  2. Use your package manager:"
        echo "     - macOS: brew install go"
        echo "     - Ubuntu/Debian: sudo apt install golang"
        echo "     - Other Linux: Check your distro's package manager"
        echo ""
        echo "After installing Go, run this script again."
        exit 1
    fi

    if build_from_source; then
        verify_installation
        exit 0
    fi

    # All methods failed
    log_error "Installation failed"
    echo ""
    echo "Manual installation:"
    echo "  1. Download from https://github.com/steveyegge/beads/releases/latest"
    echo "  2. Extract and move 'bd' to your PATH"
    echo ""
    echo "Or install from source:"
    echo "  1. Install Go from https://go.dev/dl/"
    echo "  2. Run: CGO_ENABLED=1 go install github.com/steveyegge/beads/cmd/bd@latest"
    echo ""
    exit 1
}

main "$@"

#!/usr/bin/env bash
#
# Beads (bd) installation script
# Usage: curl -fsSL https://raw.githubusercontent.com/gastownhall/beads/main/scripts/install.sh | bash
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
    echo -e "${BLUE}==>${NC} $1" >&2
}

log_success() {
    echo -e "${GREEN}==>${NC} $1" >&2
}

log_warning() {
    echo -e "${YELLOW}==>${NC} $1" >&2
}

log_error() {
    echo -e "${RED}Error:${NC} $1" >&2
}

print_missing_build_deps_help() {
    local system
    system=$(uname -s)

    case "$system" in
        Darwin)
            log_warning "Build from source requires CGO and a C toolchain."
            log_warning "Install Xcode Command Line Tools: xcode-select --install"
            ;;
        Linux)
            log_warning "Build from source requires CGO and a C toolchain."
            log_warning "Install build tools with your package manager, for example:"
            log_warning "  Debian/Ubuntu: sudo apt-get install -y build-essential pkg-config libzstd-dev"
            log_warning "  Fedora/RHEL: sudo dnf install -y gcc gcc-c++ make pkgconf-pkg-config libzstd-devel"
            ;;
        FreeBSD)
            log_warning "Build from source requires CGO and a C toolchain."
            log_warning "Install them with: pkg install -y gcc gmake pkgconf zstd"
            ;;
    esac
}

release_has_asset() {
    local release_json=$1
    local asset_name=$2

    if echo "$release_json" | grep -Fq "\"name\": \"$asset_name\""; then
        return 0
    fi

    return 1
}

download_file() {
    local url=$1
    local output_path=$2

    if command -v curl &> /dev/null; then
        curl -fsSL -o "$output_path" "$url"
        return $?
    fi

    if command -v wget &> /dev/null; then
        wget -q -O "$output_path" "$url"
        return $?
    fi

    log_error "Neither curl nor wget found. Please install one of them."
    return 1
}

sha256_file() {
    local file_path=$1

    if command -v sha256sum &> /dev/null; then
        sha256sum "$file_path" | awk '{print $1}'
        return 0
    fi

    if command -v shasum &> /dev/null; then
        shasum -a 256 "$file_path" | awk '{print $1}'
        return 0
    fi

    if command -v openssl &> /dev/null; then
        openssl dgst -sha256 "$file_path" | awk '{print $2}'
        return 0
    fi

    return 1
}

verify_release_checksum() {
    local release_json=$1
    local version=$2
    local archive_name=$3
    local archive_path=$4

    local checksums_name="checksums.txt"
    local checksums_url="https://github.com/gastownhall/beads/releases/download/${version}/${checksums_name}"

    if ! release_has_asset "$release_json" "$checksums_name"; then
        log_error "Release metadata is missing ${checksums_name}; refusing to install unverified binary"
        return 1
    fi

    if ! download_file "$checksums_url" "$checksums_name"; then
        log_error "Failed to download ${checksums_name}; refusing to install unverified binary"
        return 1
    fi

    local expected
    expected=$(awk -v target="$archive_name" '{name=$2; sub(/^\*/, "", name); if (name == target) {print $1; exit}}' "$checksums_name")
    if [ -z "$expected" ]; then
        log_error "No checksum entry found for ${archive_name} in ${checksums_name}"
        return 1
    fi

    local actual
    actual=$(sha256_file "$archive_path") || {
        log_error "No SHA256 tool found (need one of: sha256sum, shasum, openssl)"
        return 1
    }

    if [ "$expected" != "$actual" ]; then
        log_error "Checksum mismatch for ${archive_name}; refusing to install"
        return 1
    fi

    log_success "Checksum verified for ${archive_name}"
    return 0
}

find_extracted_bd() {
    local search_dir=$1

    if [ -x "$search_dir/bd" ]; then
        printf '%s\n' "$search_dir/bd"
        return 0
    fi

    local extracted_bd
    extracted_bd=$(find "$search_dir" -mindepth 2 -maxdepth 2 -type f -name bd | head -n 1)
    if [ -n "$extracted_bd" ] && [ -x "$extracted_bd" ]; then
        printf '%s\n' "$extracted_bd"
        return 0
    fi

    return 1
}

# Re-sign binary for macOS only when explicitly requested.
# This replaces the upstream signature with a local ad-hoc signature.
resign_for_macos() {
    local binary_path=$1

    # Only run on macOS
    if [[ "$(uname -s)" != "Darwin" ]]; then
        return 0
    fi

    # Keep re-signing opt-in so users can decide whether to preserve
    # the release signature/Gatekeeper behavior.
    if [ "${BEADS_INSTALL_RESIGN_MACOS:-0}" != "1" ]; then
        log_info "Skipping macOS ad-hoc re-signing (default)"
        log_info "Set BEADS_INSTALL_RESIGN_MACOS=1 to opt in"
        return 0
    fi

    # Check if codesign is available
    if ! command -v codesign &> /dev/null; then
        log_warning "codesign not found, skipping re-signing"
        return 0
    fi

    log_warning "Opt-in macOS re-sign enabled: replacing release signature with local ad-hoc signature"
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

    # Detect Windows environments where this bash script won't produce a usable install.
    # MSYS2, Git Bash, and Cygwin report MINGW*, MSYS*, or CYGWIN* from uname -s.
    case "$(uname -s)" in
        MINGW*|MSYS*|CYGWIN*)
            log_error "Windows detected ($(uname -s))."
            echo "" >&2
            echo "  This bash installer is for macOS/Linux. On Windows, use the PowerShell installer:" >&2
            echo "" >&2
            echo "    irm https://raw.githubusercontent.com/gastownhall/beads/main/install.ps1 | iex" >&2
            echo "" >&2
            exit 1
            ;;
    esac

    # Detect WSL (Windows Subsystem for Linux).
    # WSL reports uname -s as "Linux" but installs into the Linux filesystem,
    # which is not accessible from native Windows tools.
    if [ -f /proc/version ] && grep -qi 'microsoft\|wsl' /proc/version 2>/dev/null; then
        log_warning "WSL (Windows Subsystem for Linux) detected."
        echo "" >&2
        echo "  This will install the Linux version of bd, usable only inside WSL." >&2
        echo "  If you want bd available in native Windows (PowerShell, cmd), use:" >&2
        echo "" >&2
        echo "    irm https://raw.githubusercontent.com/gastownhall/beads/main/install.ps1 | iex" >&2
        echo "" >&2
        # Only show interactive message and pause if running in a terminal (skip in CI/non-interactive shells)
        if [ -t 0 ]; then
            echo "  Continuing with Linux install for WSL in 5 seconds... (Ctrl+C to cancel)" >&2
            sleep 5
        else
            echo "  Continuing with Linux install (non-interactive mode)..." >&2
        fi
    fi

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
    local latest_url="https://api.github.com/repos/gastownhall/beads/releases/latest"
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
    local download_url="https://github.com/gastownhall/beads/releases/download/${version}/${archive_name}"

    if ! release_has_asset "$release_json" "$archive_name"; then
        log_warning "No prebuilt archive available for platform ${platform}. Falling back to source installation methods."
        rm -rf "$tmp_dir"
        return 1
    fi
    
    log_info "Downloading $archive_name..."
    
    cd "$tmp_dir"
    if ! download_file "$download_url" "$archive_name"; then
        log_error "Download failed"
        cd - > /dev/null || cd "$HOME"
        rm -rf "$tmp_dir"
        return 1
    fi

    log_info "Verifying release checksum..."
    if ! verify_release_checksum "$release_json" "$version" "$archive_name" "$archive_name"; then
        cd - > /dev/null || cd "$HOME"
        rm -rf "$tmp_dir"
        return 1
    fi

    # Extract archive
    log_info "Extracting archive..."
    if ! tar -xzf "$archive_name"; then
        log_error "Failed to extract archive"
        rm -rf "$tmp_dir"
        return 1
    fi

    local extracted_bd
    if ! extracted_bd=$(find_extracted_bd "$tmp_dir"); then
        log_error "Extracted archive does not contain an executable 'bd' binary"
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

    # Install binary
    log_info "Installing to $install_dir..."
    if [[ -w "$install_dir" ]]; then
        if ! mv "$extracted_bd" "$install_dir/bd"; then
            log_error "Failed to install bd to $install_dir"
            cd - > /dev/null || cd "$HOME"
            rm -rf "$tmp_dir"
            return 1
        fi
    else
        if ! sudo mv "$extracted_bd" "$install_dir/bd"; then
            log_error "Failed to install bd to $install_dir"
            cd - > /dev/null || cd "$HOME"
            rm -rf "$tmp_dir"
            return 1
        fi
    fi

    # Optional local ad-hoc re-sign for macOS (off by default)
    resign_for_macos "$install_dir/bd"

    # Create 'beads' alias symlink
    create_beads_alias "$install_dir"

    log_success "bd installed to $install_dir/bd"

    # Record where we installed the binary so PATH precedence warnings can
    # point to the newly installed release binary.
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
        log_warning "CGO is required for some features. Install a working C toolchain and retry."
        return 1
    fi

    log_success "Verified CGO support in $binary_path"
    return 0
}

# Install using go install (fallback).
#
# Tries CGO_ENABLED=1 first for an embedded-capable binary. If that fails
# (host lacks C toolchain or transitive Dolt deps' headers), falls back to
# CGO_ENABLED=0 which yields a server-mode-only binary that still works on
# any Go-capable box. See docs/ICU-POLICY.md and docs/INSTALLING.md.
install_with_go() {
    log_info "Installing bd using 'go install'..."

    local gobin bin_dir
    gobin=$(go env GOBIN 2>/dev/null || true)
    if [ -n "$gobin" ]; then
        bin_dir="$gobin"
    else
        bin_dir="$(go env GOPATH)/bin"
    fi

    if CGO_ENABLED=1 GOFLAGS="${GOFLAGS:+$GOFLAGS }-tags=gms_pure_go" go install github.com/gastownhall/beads/cmd/bd@latest; then
        log_success "bd installed via go install (embedded-capable)"
        LAST_INSTALL_PATH="$bin_dir/bd"

        if ! verify_binary_has_cgo "$LAST_INSTALL_PATH" "go install"; then
            return 1
        fi
    else
        log_warning "go install with CGO failed; retrying without CGO (server-mode-only binary)"
        if CGO_ENABLED=0 go install github.com/gastownhall/beads/cmd/bd@latest; then
            log_success "bd installed via go install (CGO_ENABLED=0, server mode only)"
            log_warning "This bd cannot use embedded Dolt. Run 'bd init --server' to use an external dolt sql-server, or reinstall with a C toolchain for embedded mode."
            LAST_INSTALL_PATH="$bin_dir/bd"
        else
            log_error "go install failed both with and without CGO"
            print_missing_build_deps_help
            return 1
        fi
    fi

    # Optional local ad-hoc re-sign for macOS (off by default)
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
}

# Build from source (last resort)
build_from_source() {
    log_info "Building bd from source..."

    local tmp_dir
    tmp_dir=$(mktemp -d)

    cd "$tmp_dir"
    log_info "Cloning repository..."

    if git clone --depth 1 https://github.com/gastownhall/beads.git; then
        cd beads
        log_info "Building binary..."

        if CGO_ENABLED=1 go build -tags gms_pure_go -o bd ./cmd/bd; then
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

            # Optional local ad-hoc re-sign for macOS (off by default)
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
            print_missing_build_deps_help
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
    echo "  1. Download from https://github.com/gastownhall/beads/releases/latest"
    echo "  2. Verify SHA256 checksum against checksums.txt"
    echo "  3. Extract and move 'bd' to your PATH"
    echo ""
    echo "Or install from source:"
    echo "  1. Install Go from https://go.dev/dl/"
    echo "  2. Run: CGO_ENABLED=1 GOFLAGS=-tags=gms_pure_go go install github.com/gastownhall/beads/cmd/bd@latest"
    echo ""
    exit 1
}

main "$@"

#!/usr/bin/env bash
set -euo pipefail

# ==============================================================================
# caam installer - Coding Agent Account Manager
# ==============================================================================
# Usage: curl -fsSL "https://raw.githubusercontent.com/.../install.sh" | bash
#
# Options (via env vars or arguments):
#   --version=VERSION   Install specific version (e.g., v1.2.3)
#   --channel=CHANNEL   Release channel: stable (default) or beta
#   --verify            Run self-test after install (caam --version)
#   --force             Force reinstall even if same version exists
#   --dry-run           Show what would be done without installing
#   --help              Show this help message
#
# Environment variables:
#   INSTALL_DIR         Override install directory (default: auto-detect)
#   CAAM_SKIP_VERIFY    Skip signature/checksum verification (not recommended)
#   CAAM_NO_CACHE_BUST  Disable cache-busting query params
#   NO_COLOR            Disable colored output
#   CI                  Enable non-interactive mode (implies NO_COLOR)
# ==============================================================================

readonly SCRIPT_VERSION="2.0.0"
readonly REPO_OWNER="Dicklesworthstone"
readonly REPO_NAME="coding_agent_account_manager"
readonly BIN_NAME="caam"

# Exit codes for CI/automation
readonly EXIT_SUCCESS=0
readonly EXIT_UP_TO_DATE=0
readonly EXIT_UPDATED=0
readonly EXIT_ERROR=1
readonly EXIT_DEPS_MISSING=2
readonly EXIT_VERIFY_FAILED=3
readonly EXIT_DOWNLOAD_FAILED=4
readonly EXIT_BUILD_FAILED=5

# Configuration (can be overridden by args)
INSTALL_VERSION=""
INSTALL_CHANNEL="stable"
VERIFY_AFTER_INSTALL=false
FORCE_INSTALL=false
DRY_RUN=false
SHOW_HELP=false

TMP_DIRS=()

# ==============================================================================
# Argument Parsing
# ==============================================================================
parse_args() {
    while [ $# -gt 0 ]; do
        case "$1" in
            --version=*)
                INSTALL_VERSION="${1#*=}"
                ;;
            --channel=*)
                INSTALL_CHANNEL="${1#*=}"
                if [[ "$INSTALL_CHANNEL" != "stable" && "$INSTALL_CHANNEL" != "beta" ]]; then
                    die "Invalid channel: $INSTALL_CHANNEL (must be 'stable' or 'beta')"
                fi
                ;;
            --verify)
                VERIFY_AFTER_INSTALL=true
                ;;
            --force)
                FORCE_INSTALL=true
                ;;
            --dry-run)
                DRY_RUN=true
                ;;
            --help|-h)
                SHOW_HELP=true
                ;;
            *)
                warn "Unknown option: $1"
                ;;
        esac
        shift
    done
}

show_help() {
    cat <<'EOF'
caam installer - Coding Agent Account Manager

Usage:
  curl -fsSL "https://raw.githubusercontent.com/Dicklesworthstone/coding_agent_account_manager/main/install.sh" | bash
  ./install.sh [OPTIONS]

Options:
  --version=VERSION   Install specific version (e.g., v1.2.3)
  --channel=CHANNEL   Release channel: stable (default) or beta
  --verify            Run self-test after install
  --force             Force reinstall even if same version exists
  --dry-run           Show what would be done without installing
  --help              Show this help message

Environment Variables:
  INSTALL_DIR         Override install directory
  CAAM_SKIP_VERIFY    Skip signature/checksum verification
  CAAM_NO_CACHE_BUST  Disable cache-busting query params
  NO_COLOR            Disable colored output
  CI                  Enable non-interactive mode

Exit Codes:
  0  Success (installed or already up-to-date)
  1  General error
  2  Missing dependencies
  3  Verification failed
  4  Download failed
  5  Build failed

Examples:
  # Install latest stable
  curl -fsSL "URL" | bash

  # Install specific version
  curl -fsSL "URL" | bash -s -- --version=v1.2.3

  # Install beta with verification
  curl -fsSL "URL" | bash -s -- --channel=beta --verify
EOF
}

# ==============================================================================
# Output Formatting (gum-enhanced with fallback)
# ==============================================================================
HAS_GUM=false
IS_TTY=false
USE_COLOR=true

detect_terminal_features() {
    # Detect TTY
    if [ -t 0 ] && [ -t 1 ]; then
        IS_TTY=true
    fi

    # Detect color support
    if [ -n "${NO_COLOR:-}" ] || [ -n "${CI:-}" ]; then
        USE_COLOR=false
    fi

    # Detect gum
    if command -v gum >/dev/null 2>&1 && [ "$IS_TTY" = true ] && [ "$USE_COLOR" = true ]; then
        HAS_GUM=true
    fi
}

# Colored/styled output with gum fallback
info() {
    local msg="$1"
    if [ "$HAS_GUM" = true ]; then
        gum style --foreground 39 "==> $msg"
    elif [ "$USE_COLOR" = true ]; then
        printf "\033[1;34m==>\033[0m %s\n" "$msg"
    else
        printf "==> %s\n" "$msg"
    fi
}

success() {
    local msg="$1"
    if [ "$HAS_GUM" = true ]; then
        gum style --foreground 82 "==> $msg"
    elif [ "$USE_COLOR" = true ]; then
        printf "\033[1;32m==>\033[0m %s\n" "$msg"
    else
        printf "==> %s\n" "$msg"
    fi
}

warn() {
    local msg="$1"
    if [ "$HAS_GUM" = true ]; then
        gum style --foreground 214 "==> WARNING: $msg"
    elif [ "$USE_COLOR" = true ]; then
        printf "\033[1;33m==>\033[0m WARNING: %s\n" "$msg" >&2
    else
        printf "==> WARNING: %s\n" "$msg" >&2
    fi
}

error() {
    local msg="$1"
    if [ "$HAS_GUM" = true ]; then
        gum style --foreground 196 "==> ERROR: $msg"
    elif [ "$USE_COLOR" = true ]; then
        printf "\033[1;31m==>\033[0m ERROR: %s\n" "$msg" >&2
    else
        printf "==> ERROR: %s\n" "$msg" >&2
    fi
}

die() {
    error "$1"
    exit "${2:-$EXIT_ERROR}"
}

# Spinner for long operations
spin() {
    local msg="$1"
    shift
    if [ "$HAS_GUM" = true ] && [ "$IS_TTY" = true ]; then
        gum spin --spinner dot --title "$msg" -- "$@"
    else
        info "$msg"
        "$@"
    fi
}

# Confirmation prompt
confirm() {
    local prompt="$1"
    local default="${2:-Y}"

    if [ "$IS_TTY" = false ] || [ -n "${CI:-}" ]; then
        # Non-interactive: use default
        [ "$default" = "Y" ] || [ "$default" = "y" ]
        return $?
    fi

    if [ "$HAS_GUM" = true ]; then
        gum confirm "$prompt"
        return $?
    fi

    local reply
    printf "%s [Y/n] " "$prompt"
    read -r reply
    [[ "$reply" =~ ^[Yy]?$ ]]
}

cleanup_tmp_dirs() {
    local dir
    for dir in ${TMP_DIRS[@]+"${TMP_DIRS[@]}"}; do
        [ -n "$dir" ] && rm -rf "$dir"
    done
}

make_tmp_dir() {
    local dir
    dir=$(mktemp -d)
    TMP_DIRS+=("$dir")
    printf '%s\n' "$dir"
}

trap cleanup_tmp_dirs EXIT

default_install_dir() {
    if [ -n "${INSTALL_DIR:-}" ]; then
        echo "$INSTALL_DIR"
        return
    fi

    # Prefer writable Homebrew/standard prefixes on macOS first
    for dir in /usr/local/bin /opt/homebrew/bin /opt/local/bin; do
        if [ -d "$dir" ] && [ -w "$dir" ]; then
            echo "$dir"
            return
        fi
    done

    # Fall back to the first writable entry in PATH
    IFS=: read -r -a path_entries <<<"${PATH:-}"
    for dir in "${path_entries[@]}"; do
        if [ -d "$dir" ] && [ -w "$dir" ]; then
            echo "$dir"
            return
        fi
    done

    echo "/usr/local/bin"
}

INSTALL_DIR="$(default_install_dir)"

# Backwards-compatible aliases for existing code
print_info() { info "$1"; }
print_success() { success "$1"; }
print_error() { error "$1"; }
print_warn() { warn "$1"; }

# ==============================================================================
# Cache-busting URL helper
# ==============================================================================
cache_bust_url() {
    local url="$1"
    if [ -n "${CAAM_NO_CACHE_BUST:-}" ]; then
        echo "$url"
        return
    fi
    local ts
    ts=$(date +%s 2>/dev/null || echo "0")
    if [[ "$url" == *"?"* ]]; then
        echo "${url}&_cb=${ts}"
    else
        echo "${url}?_cb=${ts}"
    fi
}

# ==============================================================================
# Version comparison and idempotency
# ==============================================================================
get_installed_version() {
    local bin_path="$INSTALL_DIR/$BIN_NAME"
    if [ -x "$bin_path" ]; then
        # Try 'version' subcommand first (caam style), then --version flag
        local version_output
        version_output=$("$bin_path" version 2>/dev/null || "$bin_path" --version 2>/dev/null || echo "")
        # Extract version from output like "caam v1.2.3 (...)" or "caam 1.2.3"
        echo "$version_output" | head -1 | grep -oE 'v?[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.]+)?' | head -1 || echo ""
    else
        echo ""
    fi
}

normalize_version() {
    local v="$1"
    # Strip leading 'v' if present
    echo "${v#v}"
}

versions_equal() {
    local v1 v2
    v1=$(normalize_version "$1")
    v2=$(normalize_version "$2")
    [ "$v1" = "$v2" ]
}

# ==============================================================================
# Backup and rollback
# ==============================================================================
BACKUP_PATH=""

backup_existing_binary() {
    local bin_path="$INSTALL_DIR/$BIN_NAME"
    if [ -f "$bin_path" ]; then
        BACKUP_PATH="${bin_path}.backup.$(date +%Y%m%d%H%M%S)"
        info "Backing up existing binary to $BACKUP_PATH"
        if [ -w "$(dirname "$bin_path")" ]; then
            cp "$bin_path" "$BACKUP_PATH"
        else
            sudo cp "$bin_path" "$BACKUP_PATH"
        fi
    fi
}

rollback_on_failure() {
    if [ -n "$BACKUP_PATH" ] && [ -f "$BACKUP_PATH" ]; then
        warn "Installation failed, rolling back to previous version..."
        local bin_path="$INSTALL_DIR/$BIN_NAME"
        if [ -w "$(dirname "$bin_path")" ]; then
            mv "$BACKUP_PATH" "$bin_path"
        else
            sudo mv "$BACKUP_PATH" "$bin_path"
        fi
        success "Rolled back to previous version"
    fi
}

cleanup_backup() {
    if [ -n "$BACKUP_PATH" ] && [ -f "$BACKUP_PATH" ]; then
        if [ -w "$(dirname "$BACKUP_PATH")" ]; then
            rm -f "$BACKUP_PATH"
        else
            sudo rm -f "$BACKUP_PATH"
        fi
    fi
}

# ==============================================================================
# Self-verification
# ==============================================================================
verify_installation() {
    local bin_path="$INSTALL_DIR/$BIN_NAME"
    info "Verifying installation..."

    if [ ! -x "$bin_path" ]; then
        error "Binary not found or not executable: $bin_path"
        return 1
    fi

    local version_output
    # Try 'version' subcommand first (caam style), then --version flag
    if version_output=$("$bin_path" version 2>&1); then
        success "Verification passed: $version_output"
        return 0
    elif version_output=$("$bin_path" --version 2>&1); then
        success "Verification passed: $version_output"
        return 0
    else
        error "Failed to run '$BIN_NAME version'"
        return 1
    fi
}

detect_platform() {
    local os arch

    os="$(uname -s | tr '[:upper:]' '[:lower:]')"
    arch="$(uname -m)"

    case "$os" in
        linux) os="linux" ;;
        darwin) os="darwin" ;;
        mingw*|msys*|cygwin*) os="windows" ;;
        *) print_error "Unsupported OS: $os"; return 1 ;;
    esac

    case "$arch" in
        x86_64|amd64) arch="amd64" ;;
        arm64|aarch64) arch="arm64" ;;
        *) print_error "Unsupported architecture: $arch"; return 1 ;;
    esac

    echo "${os}_${arch}"
}

get_release() {
    local version="${1:-}"
    local url

    if [ -n "$version" ]; then
        # Specific version
        url="https://api.github.com/repos/${REPO_OWNER}/${REPO_NAME}/releases/tags/${version}"
    elif [ "$INSTALL_CHANNEL" = "beta" ]; then
        # Beta: get all releases and find latest (including pre-releases)
        url="https://api.github.com/repos/${REPO_OWNER}/${REPO_NAME}/releases"
    else
        # Stable: latest release only
        url="https://api.github.com/repos/${REPO_OWNER}/${REPO_NAME}/releases/latest"
    fi

    url=$(cache_bust_url "$url")

    local response
    if command -v curl >/dev/null 2>&1; then
        response=$(curl -fsSL "$url" 2>/dev/null) || return 1
    elif command -v wget >/dev/null 2>&1; then
        response=$(wget -qO- "$url" 2>/dev/null) || return 1
    else
        die "Neither curl nor wget found. Please install one of them." $EXIT_DEPS_MISSING
    fi

    # For beta channel, extract first release from array
    if [ "$INSTALL_CHANNEL" = "beta" ] && [ -z "$version" ]; then
        echo "$response" | ensure_python && "$PYTHON_CMD" -c "
import json, sys
data = json.load(sys.stdin)
if isinstance(data, list) and len(data) > 0:
    print(json.dumps(data[0]))
else:
    sys.exit(1)
"
    else
        echo "$response"
    fi
}

# Backward compatibility alias
get_latest_release() {
    get_release ""
}

download_file() {
    local url="$1"
    local dest="$2"
    local use_cache_bust="${3:-true}"

    if [ "$use_cache_bust" = "true" ]; then
        url=$(cache_bust_url "$url")
    fi

    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$url" -o "$dest" || return 1
    elif command -v wget >/dev/null 2>&1; then
        wget -q "$url" -O "$dest" || return 1
    else
        die "Neither curl nor wget found. Please install one of them:
  - macOS: brew install curl
  - Ubuntu/Debian: sudo apt install curl
  - Fedora: sudo dnf install curl" $EXIT_DEPS_MISSING
    fi
}

ensure_install_dir() {
    local dir="$1"

    if [ -d "$dir" ]; then
        return 0
    fi

    if mkdir -p "$dir" 2>/dev/null; then
        return 0
    fi

    print_info "Creating $dir requires sudo..."
    sudo mkdir -p "$dir"
}

PYTHON_CMD=""

ensure_python() {
    if [ -n "$PYTHON_CMD" ]; then
        return 0
    fi

    if command -v python3 >/dev/null 2>&1; then
        PYTHON_CMD="$(command -v python3)"
        return 0
    fi

    if command -v python >/dev/null 2>&1; then
        PYTHON_CMD="$(command -v python)"
        return 0
    fi

    print_error "Python 3 is required to parse GitHub release metadata."
    print_error "Please install python3 (e.g., 'xcode-select --install' on macOS) or install jq."
    return 1
}

fetch_latest_go_pkg() {
    ensure_python || return 1

    local arch
    arch="$(uname -m)"
    case "$arch" in
        arm64|aarch64) arch="arm64" ;;
        x86_64|amd64) arch="amd64" ;;
        *) print_error "Unsupported macOS architecture for Go install: $arch"; return 1 ;;
    esac

    "$PYTHON_CMD" - "$arch" <<'PY'
import json
import sys
import urllib.request


def main() -> int:
    arch = sys.argv[1]
    try:
        with urllib.request.urlopen("https://go.dev/dl/?mode=json") as resp:
            data = json.load(resp)
    except Exception as exc:
        sys.stderr.write(f"Failed to fetch Go releases: {exc}\n")
        return 1

    release = next((r for r in data if r.get("stable")), None)
    if not release:
        return 1

    version = release.get("version") or ""
    files = release.get("files") or []
    pkg = next(
        (f for f in files if f.get("os") == "darwin" and f.get("arch") == arch and f.get("filename", "").endswith(".pkg")),
        None,
    )

    if not pkg:
        return 1

    url = pkg.get("url") or ""
    print(version)
    print(url)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
PY
}

install_go_from_pkg() {
    local version url tmpdir pkg_path

    read -r version url < <(fetch_latest_go_pkg) || return 1

    if [ -z "$version" ] || [ -z "$url" ]; then
        return 1
    fi

    print_info "Downloading Go $version (.pkg)..."
    tmpdir=$(mktemp -d)
    pkg_path="$tmpdir/go.pkg"

    if ! curl -fsSL "$url" -o "$pkg_path"; then
        print_error "Failed to download Go installer from $url"
        rm -rf "$tmpdir"
        return 1
    fi

    print_info "Installing Go $version (requires sudo)..."
    if sudo installer -pkg "$pkg_path" -target / >/dev/null; then
        print_success "Installed Go $version"
        rm -rf "$tmpdir"
        return 0
    fi

    print_error "Go installer failed"
    rm -rf "$tmpdir"
    return 1
}

version_ge() {
    local IFS=.
    local i ver1=($1) ver2=($2)
    for ((i=0; i<${#ver1[@]} || i<${#ver2[@]}; i++)); do
        local v1=${ver1[i]:-0}
        local v2=${ver2[i]:-0}
        if ((10#$v1 > 10#$v2)); then return 0; fi
        if ((10#$v1 < 10#$v2)); then return 1; fi
    done
    return 0
}

select_release_asset() {
    local platform="$1"
    ensure_python || return 1

    local release_json
    release_json=$(cat) || return 1

    CAAM_RELEASE_JSON="$release_json" "$PYTHON_CMD" - "$platform" "$BIN_NAME" <<'PY'
import json
import os
import sys


def pick_asset(data, platform, bin_name):
    ext = ".zip" if platform.startswith("windows_") else ".tar.gz"
    assets = data.get("assets") or []

    # Prefer exact platform match with expected ext
    for asset in assets:
        name = asset.get("name") or ""
        if platform in name and name.endswith(ext):
            url = asset.get("browser_download_url") or ""
            if url:
                return name, url

    # Fallback: any asset that contains platform and correct ext
    for asset in assets:
        name = asset.get("name") or ""
        url = asset.get("browser_download_url") or ""
        if platform.replace("_", "") in name.replace("_", "") and name.endswith(ext) and url:
            return name, url

    return None, None


def main():
    if len(sys.argv) < 3:
        return 1
    platform = sys.argv[1]
    bin_name = sys.argv[2]
    release_json = os.environ.get("CAAM_RELEASE_JSON", "")
    if not release_json:
        sys.stderr.write("Missing release metadata\n")
        return 1
    try:
        data = json.loads(release_json)
    except Exception as exc:
        sys.stderr.write(f"Failed to parse release JSON: {exc}\n")
        return 1

    version = data.get("tag_name") or ""
    name, url = pick_asset(data, platform, bin_name)

    print(version)
    print(url or "")
    print(name or "")

    return 0 if url else 1


if __name__ == "__main__":
    raise SystemExit(main())
PY
}

select_named_asset() {
    local asset_name="$1"
    ensure_python || return 1

    local release_json
    release_json=$(cat) || return 1

    CAAM_RELEASE_JSON="$release_json" "$PYTHON_CMD" - "$asset_name" <<'PY'
import json
import os
import sys


def main():
    if len(sys.argv) < 2:
        return 1
    target = sys.argv[1]
    release_json = os.environ.get("CAAM_RELEASE_JSON", "")
    if not release_json:
        sys.stderr.write("Missing release metadata\n")
        return 1
    try:
        data = json.loads(release_json)
    except Exception as exc:
        sys.stderr.write(f"Failed to parse release JSON: {exc}\n")
        return 1

    assets = data.get("assets") or []
    for asset in assets:
        name = asset.get("name") or ""
        if name == target:
            url = asset.get("browser_download_url") or ""
            if url:
                print(url)
                return 0
    print("")
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
PY
}

verify_release_assets() {
    local release_json="$1"
    local version="$2"
    local asset_name="$3"
    local archive_path="$4"
    local tmp_dir="$5"

    if [ -n "${CAAM_SKIP_VERIFY:-}" ]; then
        print_warn "Skipping release verification (CAAM_SKIP_VERIFY set)."
        return 0
    fi

    local checksums_url signature_url
    checksums_url=$(printf '%s' "$release_json" | select_named_asset "SHA256SUMS") || true
    signature_url=$(printf '%s' "$release_json" | select_named_asset "SHA256SUMS.sig") || true

    if [ -z "$checksums_url" ] || [ -z "$signature_url" ]; then
        print_error "Release is missing SHA256SUMS or SHA256SUMS.sig assets."
        return 1
    fi

    local checksums_path="$tmp_dir/SHA256SUMS"
    local signature_path="$tmp_dir/SHA256SUMS.sig"

    print_info "Downloading checksums and signature..."
    if ! download_file "$checksums_url" "$checksums_path"; then
        print_error "Failed to download SHA256SUMS."
        return 1
    fi
    if ! download_file "$signature_url" "$signature_path"; then
        print_error "Failed to download SHA256SUMS.sig."
        return 1
    fi

    if ! command -v cosign >/dev/null 2>&1; then
        if [ "${CI:-}" = "true" ] || [ "${NONINTERACTIVE:-}" = "1" ]; then
            print_warn "cosign not found — skipping signature verification (CI/non-interactive mode)."
            print_warn "Install cosign for signature verification in production."
            # Fall through to checksum-only verification below
        else
            error "cosign is required to verify release signatures."
            error ""
            error "Install cosign:"
            error "  macOS:        brew install cosign"
            error "  Ubuntu/Debian: sudo apt install cosign"
            error "  Go:           go install github.com/sigstore/cosign/v2/cmd/cosign@latest"
            error ""
            error "Or bypass verification (not recommended for security):"
            error "  CAAM_SKIP_VERIFY=1 curl -fsSL ... | bash"
            return 1
        fi
    else
        local identity="https://github.com/${REPO_OWNER}/${REPO_NAME}/.github/workflows/release.yml@refs/tags/${version}"
        if ! cosign verify-blob \
            --bundle "$signature_path" \
            --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
            --certificate-identity "$identity" \
            "$checksums_path" >/dev/null 2>&1; then
            print_error "Signature verification failed."
            return 1
        fi
    fi

    if [ -z "$asset_name" ]; then
        asset_name=$(basename "$archive_path")
    fi

    local expected actual
    expected=$(grep -F " $asset_name" "$checksums_path" | awk '{print $1}' | head -1)
    if [ -z "$expected" ]; then
        print_error "Checksum entry not found for $asset_name."
        return 1
    fi

    if command -v sha256sum >/dev/null 2>&1; then
        actual=$(sha256sum "$archive_path" | awk '{print $1}')
    elif command -v shasum >/dev/null 2>&1; then
        actual=$(shasum -a 256 "$archive_path" | awk '{print $1}')
    else
        print_error "sha256sum or shasum is required for checksum verification."
        return 1
    fi

    if [ "$expected" != "$actual" ]; then
        print_error "Checksum verification failed."
        return 1
    fi

    print_success "Verified release signature and checksum."
    return 0
}

is_tty() {
    [ "$IS_TTY" = true ]
}

ensure_go() {
    local min_version="1.21"
    local go_version=""

    if command -v go >/dev/null 2>&1; then
        go_version=$(go version 2>/dev/null | awk '{print $3}' | sed 's/^go//')
        if version_ge "$go_version" "$min_version"; then
            printf '%s' "$go_version"
            return 0
        fi
        print_warn "Go $min_version or later is required. Found: go$go_version"
    else
        print_warn "Go is not installed."
    fi

    # Try to install/upgrade via Homebrew on macOS
    if command -v brew >/dev/null 2>&1; then
        if is_tty; then
            printf "Install/upgrade Go via Homebrew now? [Y/n] "
            read -r reply
            if [[ "$reply" =~ ^[Nn] ]]; then
                return 1
            fi
        else
            print_info "Attempting non-interactive install of Go via Homebrew..."
        fi

        if brew install go || brew upgrade go; then
            go_version=$(go version 2>/dev/null | awk '{print $3}' | sed 's/^go//')
            if version_ge "$go_version" "$min_version"; then
                print_success "Installed Go $go_version via Homebrew"
                printf '%s' "$go_version"
                return 0
            fi
        else
            print_error "Homebrew installation of Go failed."
        fi
    else
        print_warn "Homebrew not found."
    fi

    # Fallback: download official macOS pkg directly
    if [ "$(uname -s)" = "Darwin" ]; then
        if is_tty; then
            printf "Download and install the latest Go from go.dev now? [Y/n] "
            read -r reply
            if [[ "$reply" =~ ^[Nn] ]]; then
                return 1
            fi
        else
            print_info "Attempting non-interactive Go install via official pkg..."
        fi

        if install_go_from_pkg; then
            local candidates=( "go" "/usr/local/go/bin/go" "/usr/local/bin/go" )
            for candidate in "${candidates[@]}"; do
                if command -v "$candidate" >/dev/null 2>&1; then
                    go_version=$("$candidate" version 2>/dev/null | awk '{print $3}' | sed 's/^go//')
                    if [ -n "$go_version" ] && version_ge "$go_version" "$min_version"; then
                        print_success "Detected Go $go_version after pkg install"
                        printf '%s' "$go_version"
                        return 0
                    fi
                fi
            done
        fi
    fi

    return 1
}

try_binary_install() {
    local platform="$1"
    local tmp_dir

    info "Checking for pre-built binary..."

    local release_json
    if [ -n "$INSTALL_VERSION" ]; then
        release_json=$(get_release "$INSTALL_VERSION") || {
            error "Failed to fetch release $INSTALL_VERSION"
            error "Check that the version exists: https://github.com/${REPO_OWNER}/${REPO_NAME}/releases"
            return 1
        }
    else
        release_json=$(get_release) || {
            error "Failed to fetch release information from GitHub"
            error "Check your internet connection or try again later"
            error "You can also try: CAAM_SKIP_VERIFY=1 to skip verification"
            return 1
        }
    fi

    local parsed version download_url asset_name
    parsed=$(printf '%s' "$release_json" | select_release_asset "$platform") || true

    version=$(printf '%s' "$parsed" | sed -n '1p')
    download_url=$(printf '%s' "$parsed" | sed -n '2p')
    asset_name=$(printf '%s' "$parsed" | sed -n '3p')

    if [ -z "$download_url" ]; then
        warn "No pre-built binary found for $platform"
        warn "Available platforms may differ. Falling back to source build."
        return 1
    fi

    if [ -z "$version" ]; then
        version="unknown"
    fi

    # Check for idempotency (skip if same version already installed)
    local installed_version
    installed_version=$(get_installed_version)
    if [ -n "$installed_version" ] && versions_equal "$installed_version" "$version"; then
        if [ "$FORCE_INSTALL" = true ]; then
            info "Same version already installed ($installed_version), but --force specified"
        else
            success "$BIN_NAME $version is already installed and up-to-date"
            info "Use --force to reinstall"
            return 0
        fi
    fi

    info "Release version: $version"
    if [ -n "$asset_name" ]; then
        info "Selected asset: $asset_name"
    fi

    info "Downloading from GitHub..."

    tmp_dir=$(make_tmp_dir)

    local ext=".tar.gz"
    if [[ "$download_url" == *.zip ]]; then
        ext=".zip"
    fi

    local archive_path="$tmp_dir/archive${ext}"

    # Use spin for download if gum available
    if [ "$HAS_GUM" = true ]; then
        if ! spin "Downloading $asset_name" download_file "$download_url" "$archive_path" "false"; then
            error "Download failed from: $download_url"
            error "Try again or check https://github.com/${REPO_OWNER}/${REPO_NAME}/releases"
            return 1
        fi
    else
        if ! download_file "$download_url" "$archive_path" "false"; then
            error "Download failed from: $download_url"
            error "Try again or check https://github.com/${REPO_OWNER}/${REPO_NAME}/releases"
            return 1
        fi
    fi

    if ! verify_release_assets "$release_json" "$version" "$asset_name" "$archive_path" "$tmp_dir"; then
        error "Release verification failed"
        error "This could indicate a tampered download or network issue"
        error "Set CAAM_SKIP_VERIFY=1 to bypass (not recommended)"
        return 1
    fi

    info "Extracting archive..."

    if [[ "$ext" == ".zip" ]]; then
        if command -v unzip >/dev/null 2>&1; then
            unzip -q "$archive_path" -d "$tmp_dir"
        else
            error "unzip is required but not found"
            error "Install it with: sudo apt install unzip (Debian/Ubuntu)"
            error "               : brew install unzip (macOS)"
            return 1
        fi
    else
        tar -xzf "$archive_path" -C "$tmp_dir"
    fi

    local binary_path
    binary_path=$(find "$tmp_dir" -type f -name "$BIN_NAME" -perm -111 2>/dev/null | head -1)

    if [ -z "$binary_path" ]; then
        binary_path=$(find "$tmp_dir" -type f -name "$BIN_NAME" 2>/dev/null | head -1)
    fi

    if [ -z "$binary_path" ] && [[ "$platform" == windows_* ]]; then
        binary_path=$(find "$tmp_dir" -type f -name "${BIN_NAME}.exe" 2>/dev/null | head -1)
    fi

    if [ -z "$binary_path" ]; then
        error "Binary not found in archive"
        error "The downloaded archive may be corrupted"
        return 1
    fi

    chmod +x "$binary_path"

    ensure_install_dir "$INSTALL_DIR"
    local dest_path="$INSTALL_DIR/$BIN_NAME"

    # Backup existing binary before replacing
    backup_existing_binary

    if [ -w "$INSTALL_DIR" ]; then
        mv "$binary_path" "$dest_path"
    else
        info "Installing to $INSTALL_DIR requires sudo..."
        sudo mv "$binary_path" "$dest_path"
    fi

    success "Installed $BIN_NAME $version to $dest_path"
    return 0
}

try_go_install() {
    info "Attempting to build from source with go build..."

    local go_version
    if ! go_version=$(ensure_go); then
        error "Go 1.21 or later is required for building from source."
        error ""
        error "Install Go:"
        error "  macOS:         brew install go"
        error "  Ubuntu/Debian: sudo apt install golang-go"
        error "  Official:      https://go.dev/dl/"
        return 1
    fi

    info "Using Go $go_version"

    local tmp_dir src_dir repo_url tarball_url tarball_path build_output fetched=0
    tmp_dir=$(make_tmp_dir)
    src_dir="$tmp_dir/src"
    repo_url="https://github.com/${REPO_OWNER}/${REPO_NAME}.git"

    info "Fetching source..."

    if command -v git >/dev/null 2>&1; then
        if git clone --depth 1 "$repo_url" "$src_dir" >/dev/null 2>&1; then
            fetched=1
        else
            warn "git clone failed, attempting tarball download..."
        fi
    fi

    if [ "$fetched" -ne 1 ]; then
        tarball_url=$(cache_bust_url "https://codeload.github.com/${REPO_OWNER}/${REPO_NAME}/tar.gz/refs/heads/main")
        tarball_path="$tmp_dir/source.tar.gz"
        if ! download_file "$tarball_url" "$tarball_path" "false"; then
            error "Failed to download source tarball from GitHub."
            error "Check your internet connection and try again."
            return 1
        fi
        tar -xzf "$tarball_path" -C "$tmp_dir"
        src_dir=$(find "$tmp_dir" -maxdepth 1 -type d -name "${REPO_NAME}-*" | head -1)
        if [ -z "$src_dir" ]; then
            error "Could not locate extracted source directory."
            error "The downloaded tarball may be corrupted."
            return 1
        fi
    fi

    info "Building $BIN_NAME from source (this may take a minute)..."
    build_output="$tmp_dir/$BIN_NAME"

    local build_cmd="cd \"$src_dir\" && GO111MODULE=on CGO_ENABLED=0 go build -o \"$build_output\" \"./cmd/$BIN_NAME\""

    if [ "$HAS_GUM" = true ]; then
        if ! spin "Compiling $BIN_NAME" bash -c "$build_cmd"; then
            error "Go build failed."
            error ""
            error "Troubleshooting:"
            error "  1. Ensure Go 1.21+ is installed: go version"
            error "  2. Check for network issues (module downloads)"
            error "  3. Try manually: git clone $repo_url && cd ${REPO_NAME} && go build ./cmd/$BIN_NAME"
            return 1
        fi
    else
        if ! (cd "$src_dir" && GO111MODULE=on CGO_ENABLED=0 go build -o "$build_output" "./cmd/$BIN_NAME"); then
            error "Go build failed."
            error ""
            error "Troubleshooting:"
            error "  1. Ensure Go 1.21+ is installed: go version"
            error "  2. Check for network issues (module downloads)"
            error "  3. Try manually: git clone $repo_url && cd ${REPO_NAME} && go build ./cmd/$BIN_NAME"
            return 1
        fi
    fi

    ensure_install_dir "$INSTALL_DIR"
    local dest_path="$INSTALL_DIR/$BIN_NAME"

    # Backup existing binary before replacing
    backup_existing_binary

    if [ -w "$INSTALL_DIR" ]; then
        mv "$build_output" "$dest_path"
    else
        info "Installing to $INSTALL_DIR requires sudo..."
        sudo mv "$build_output" "$dest_path"
    fi

    success "Built and installed $BIN_NAME from source to $dest_path"
    return 0
}

show_quick_start() {
    echo ""
    info "Quick start:"
    info "  1. Login to your AI tool normally (e.g., 'claude' then '/login')"
    info "  2. Backup: caam backup claude my-account"
    info "  3. Switch: caam activate claude other-account"
    echo ""
    info "Run 'caam --help' for all commands."
    echo ""
    echo "Tip: You can also install via Homebrew:"
    echo "  brew install dicklesworthstone/tap/caam"
}

main() {
    # Parse arguments first
    parse_args "$@"

    # Initialize terminal feature detection
    detect_terminal_features

    # Show help if requested
    if [ "$SHOW_HELP" = true ]; then
        show_help
        exit $EXIT_SUCCESS
    fi

    # Log mode info
    if [ -n "${CI:-}" ]; then
        info "Running in CI/non-interactive mode"
    fi

    if [ "$DRY_RUN" = true ]; then
        info "Dry-run mode: no changes will be made"
    fi

    info "Installing $BIN_NAME - Coding Agent Account Manager (installer v$SCRIPT_VERSION)..."

    # Detect platform
    local platform
    platform=$(detect_platform) || {
        warn "Could not detect platform, will try building from source"
        if [ "$DRY_RUN" = true ]; then
            info "[DRY-RUN] Would build from source"
            exit $EXIT_SUCCESS
        fi
        try_go_install
        show_quick_start
        exit $EXIT_SUCCESS
    }

    info "Detected platform: $platform"
    info "Install directory: $INSTALL_DIR"
    if [ -n "$INSTALL_VERSION" ]; then
        info "Requested version: $INSTALL_VERSION"
    fi
    info "Channel: $INSTALL_CHANNEL"

    # Check for existing installation (idempotency)
    local installed_version
    installed_version=$(get_installed_version)
    if [ -n "$installed_version" ]; then
        info "Currently installed: $installed_version"
    fi

    # Dry-run: show what would happen and exit
    if [ "$DRY_RUN" = true ]; then
        info "[DRY-RUN] Would check for latest release"
        info "[DRY-RUN] Would download and verify binary for $platform"
        if [ -n "$installed_version" ]; then
            info "[DRY-RUN] Would backup existing binary before replacing"
        fi
        info "[DRY-RUN] Would install to $INSTALL_DIR/$BIN_NAME"
        if [ "$VERIFY_AFTER_INSTALL" = true ]; then
            info "[DRY-RUN] Would verify installation by running '$BIN_NAME --version'"
        fi
        exit $EXIT_SUCCESS
    fi

    # First, try to download pre-built binary
    if try_binary_install "$platform"; then
        # Cleanup backup on success
        cleanup_backup

        # Run verification if requested
        if [ "$VERIFY_AFTER_INSTALL" = true ]; then
            if ! verify_installation; then
                rollback_on_failure
                die "Installation verification failed" $EXIT_VERIFY_FAILED
            fi
        fi

        show_quick_start
        exit $EXIT_SUCCESS
    fi

    # Fall back to building from source
    info "Pre-built binary not available, falling back to source build..."

    if ! try_go_install; then
        rollback_on_failure
        die "Failed to install $BIN_NAME" $EXIT_BUILD_FAILED
    fi

    # Cleanup backup on success
    cleanup_backup

    # Run verification if requested
    if [ "$VERIFY_AFTER_INSTALL" = true ]; then
        if ! verify_installation; then
            rollback_on_failure
            die "Installation verification failed" $EXIT_VERIFY_FAILED
        fi
    fi

    show_quick_start
    exit $EXIT_SUCCESS
}

if [[ ${BASH_SOURCE+x} != x ]]; then
    main "$@"
elif [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
    main "$@"
fi

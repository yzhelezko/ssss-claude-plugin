#!/bin/bash
set -e

# SSSS - Stupid Simple Semantic Search
# Installation Script

REPO="yzhelezko/ssss-claude-plugin"
INSTALL_DIR="${SSSS_INSTALL_DIR:-$HOME/.ssss-claude-plugin}"
BIN_DIR="${SSSS_BIN_DIR:-$INSTALL_DIR/bin}"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

print_banner() {
    echo -e "${BLUE}"
    echo "╔═══════════════════════════════════════════════════════════╗"
    echo "║     SSSS - Stupid Simple Semantic Search                  ║"
    echo "║     AI-powered code search using local embeddings         ║"
    echo "╚═══════════════════════════════════════════════════════════╝"
    echo -e "${NC}"
}

log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

detect_platform() {
    OS=$(uname -s | tr '[:upper:]' '[:lower:]')
    ARCH=$(uname -m)

    case "$ARCH" in
        x86_64|amd64)
            ARCH="amd64"
            ;;
        arm64|aarch64)
            ARCH="arm64"
            ;;
        *)
            log_error "Unsupported architecture: $ARCH"
            exit 1
            ;;
    esac

    case "$OS" in
        linux)
            PLATFORM="linux"
            ;;
        darwin)
            PLATFORM="darwin"
            ;;
        mingw*|msys*|cygwin*)
            PLATFORM="windows"
            ;;
        *)
            log_error "Unsupported operating system: $OS"
            exit 1
            ;;
    esac

    log_info "Detected platform: ${PLATFORM}-${ARCH}"
}

get_latest_version() {
    log_info "Fetching latest version..."
    LATEST_VERSION=$(curl -s "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')

    if [ -z "$LATEST_VERSION" ]; then
        log_error "Failed to fetch latest version. Using v1.0.0 as fallback."
        LATEST_VERSION="v1.0.0"
    fi

    log_info "Latest version: $LATEST_VERSION"
}

download_binary() {
    local url="https://github.com/${REPO}/releases/download/${LATEST_VERSION}/ssss-${PLATFORM}-${ARCH}.tar.gz"
    local tmp_dir=$(mktemp -d)
    local archive="${tmp_dir}/ssss.tar.gz"

    log_info "Downloading from: $url"

    if command -v curl &> /dev/null; then
        curl -fsSL "$url" -o "$archive"
    elif command -v wget &> /dev/null; then
        wget -q "$url" -O "$archive"
    else
        log_error "Neither curl nor wget found. Please install one of them."
        exit 1
    fi

    log_info "Extracting archive..."
    mkdir -p "$BIN_DIR"
    tar -xzf "$archive" -C "$tmp_dir"

    # Find and move the binary
    local binary=$(find "$tmp_dir" -name "ssss*" -type f ! -name "*.tar.gz" | head -1)
    if [ -z "$binary" ]; then
        log_error "Binary not found in archive"
        exit 1
    fi

    mv "$binary" "$BIN_DIR/ssss"
    chmod +x "$BIN_DIR/ssss"

    rm -rf "$tmp_dir"
    log_success "Binary installed to: $BIN_DIR/ssss"
}

setup_env() {
    log_info "Setting up environment..."

    # Create data directory
    mkdir -p "$INSTALL_DIR/data"

    # Create env file (still useful for CLI usage)
    cat > "$INSTALL_DIR/env.sh" << EOF
# SSSS Environment Configuration
# Source this file or add to your shell profile

# Add to PATH if not already present
if [[ ":\$PATH:" != *":$BIN_DIR:"* ]]; then
    export PATH="$BIN_DIR:\$PATH"
fi
EOF

    log_success "Environment file created: $INSTALL_DIR/env.sh"
}

update_mcp_config() {
    log_info "Installing MCP server configuration..."

    local binary_path="$BIN_DIR/ssss"
    local data_dir="$INSTALL_DIR/data"

    local mcp_config='{
  "ssss": {
    "command": "'"$binary_path"'",
    "args": [],
    "env": {
      "MCP_DB_PATH": "'"$data_dir"'",
      "MCP_OLLAMA_URL": "http://localhost:11434",
      "MCP_EMBEDDING_MODEL": "qwen3-embedding:8b",
      "MCP_WEBUI_ENABLED": "true",
      "MCP_WEBUI_PORT": "9420",
      "MCP_AUTO_OPEN_UI": "true",
      "MCP_AUTO_INDEX": "true",
      "MCP_WATCH_ENABLED": "true",
      "MCP_EMBEDDING_WORKERS": "4",
      "MCP_MAX_FILE_SIZE": "1048576",
      "MCP_DEBOUNCE_MS": "500"
    }
  }
}'

    # Create Claude plugin directories and .mcp.json files
    local claude_dir="$HOME/.claude"
    local plugin_locations=(
        "$claude_dir/plugins/cache/yzhelezko/ssss/${LATEST_VERSION#v}"
        "$claude_dir/plugins/marketplaces/yzhelezko"
    )

    for location in "${plugin_locations[@]}"; do
        # Create directory if it doesn't exist
        if [ ! -d "$location" ]; then
            mkdir -p "$location"
            log_info "Created directory: $location"
        fi

        # Always write .mcp.json
        local mcp_file="$location/.mcp.json"
        echo "$mcp_config" > "$mcp_file"
        log_info "Created: $mcp_file"

        # Also check for version subdirectories
        for version_dir in "$location"/*/; do
            if [ -d "$version_dir" ]; then
                version_dir="${version_dir%/}"
                if [[ "$(basename "$version_dir")" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
                    local version_mcp_file="$version_dir/.mcp.json"
                    echo "$mcp_config" > "$version_mcp_file"
                    log_info "Created: $version_mcp_file"
                fi
            fi
        done
    done

    log_success "MCP server configuration installed"
}

setup_shell() {
    local shell_rc=""
    local shell_name=$(basename "$SHELL")

    case "$shell_name" in
        bash)
            shell_rc="$HOME/.bashrc"
            ;;
        zsh)
            shell_rc="$HOME/.zshrc"
            ;;
        fish)
            log_warn "Fish shell detected. Please manually add to config.fish"
            return
            ;;
        *)
            log_warn "Unknown shell: $shell_name. Please manually source $INSTALL_DIR/env.sh"
            return
            ;;
    esac

    local source_line="source \"$INSTALL_DIR/env.sh\""

    if [ -f "$shell_rc" ] && grep -q "ssss-claude-plugin" "$shell_rc"; then
        log_info "Shell configuration already exists in $shell_rc"
    else
        echo "" >> "$shell_rc"
        echo "# SSSS - Semantic Search for Source code" >> "$shell_rc"
        echo "$source_line" >> "$shell_rc"
        log_success "Added to $shell_rc"
    fi
}

check_ollama() {
    log_info "Checking Ollama installation..."

    if command -v ollama &> /dev/null; then
        log_success "Ollama is installed"

        # Check if the model is available
        local model="${MCP_EMBEDDING_MODEL:-qwen3-embedding:8b}"
        if ollama list 2>/dev/null | grep -q "$model"; then
            log_success "Model '$model' is available"
        else
            log_warn "Model '$model' not found. Run: ollama pull $model"
        fi
    else
        log_warn "Ollama not found. Please install from: https://ollama.ai"
    fi
}

print_next_steps() {
    echo ""
    echo -e "${GREEN}Installation complete!${NC}"
    echo ""
    echo "Next steps:"
    echo "  1. Ensure Ollama is running: ollama serve"
    echo "  2. Pull the embedding model: ollama pull qwen3-embedding:8b"
    echo "  3. Install the Claude Code plugin: /plugin install github:yzhelezko/ssss-claude-plugin"
    echo "  4. Restart Claude Code to load the plugin"
    echo ""
    echo "Binary location: $BIN_DIR/ssss"
    echo "Data directory:  $INSTALL_DIR/data"
    echo ""
    echo "Documentation: https://github.com/yzhelezko/ssss-claude-plugin"
}

main() {
    print_banner
    detect_platform
    get_latest_version
    download_binary
    setup_env
    setup_shell
    update_mcp_config
    check_ollama
    print_next_steps
}

main "$@"

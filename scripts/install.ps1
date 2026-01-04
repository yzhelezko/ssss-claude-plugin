# SSSS - Stupid Simple Semantic Search
# Windows Installation Script

$ErrorActionPreference = "Stop"

$Repo = "yzhelezko/ssss-claude-plugin"
$InstallDir = if ($env:SSSS_INSTALL_DIR) { $env:SSSS_INSTALL_DIR } else { "$env:USERPROFILE\.ssss-claude-plugin" }
$BinDir = "$InstallDir\bin"

function Write-Banner {
    Write-Host ""
    Write-Host "╔═══════════════════════════════════════════════════════════╗" -ForegroundColor Blue
    Write-Host "║     SSSS - Stupid Simple Semantic Search                  ║" -ForegroundColor Blue
    Write-Host "║     AI-powered code search using local embeddings         ║" -ForegroundColor Blue
    Write-Host "╚═══════════════════════════════════════════════════════════╝" -ForegroundColor Blue
    Write-Host ""
}

function Write-Info($Message) {
    Write-Host "[INFO] " -ForegroundColor Blue -NoNewline
    Write-Host $Message
}

function Write-Success($Message) {
    Write-Host "[SUCCESS] " -ForegroundColor Green -NoNewline
    Write-Host $Message
}

function Write-Warn($Message) {
    Write-Host "[WARN] " -ForegroundColor Yellow -NoNewline
    Write-Host $Message
}

function Write-Error($Message) {
    Write-Host "[ERROR] " -ForegroundColor Red -NoNewline
    Write-Host $Message
}

function Get-LatestVersion {
    Write-Info "Fetching latest version..."

    try {
        $Release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest"
        $Version = $Release.tag_name
        Write-Info "Latest version: $Version"
        return $Version
    }
    catch {
        Write-Warn "Failed to fetch latest version. Using v1.0.0 as fallback."
        return "v1.0.0"
    }
}

function Install-Binary($Version) {
    $Arch = if ([Environment]::Is64BitOperatingSystem) { "amd64" } else { "386" }
    $Url = "https://github.com/$Repo/releases/download/$Version/ssss-windows-$Arch.zip"

    Write-Info "Downloading from: $Url"

    # Create directories
    New-Item -ItemType Directory -Force -Path $BinDir | Out-Null

    # Download
    $TempFile = Join-Path $env:TEMP "ssss.zip"
    Invoke-WebRequest -Uri $Url -OutFile $TempFile

    # Extract
    Write-Info "Extracting archive..."
    $TempDir = Join-Path $env:TEMP "ssss-extract"
    Expand-Archive -Path $TempFile -DestinationPath $TempDir -Force

    # Find and move binary
    $Binary = Get-ChildItem -Path $TempDir -Filter "ssss*.exe" -Recurse | Select-Object -First 1
    if (-not $Binary) {
        throw "Binary not found in archive"
    }

    Copy-Item -Path $Binary.FullName -Destination "$BinDir\ssss.exe" -Force

    # Cleanup
    Remove-Item -Path $TempFile -Force
    Remove-Item -Path $TempDir -Recurse -Force

    Write-Success "Binary installed to: $BinDir\ssss.exe"
}

function Set-Environment {
    Write-Info "Setting up environment..."

    # Create data directory
    $DataDir = "$InstallDir\data"
    New-Item -ItemType Directory -Force -Path $DataDir | Out-Null

    # Add to PATH (still useful for CLI usage)
    $CurrentPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if ($CurrentPath -notlike "*$BinDir*") {
        $NewPath = "$BinDir;$CurrentPath"
        [Environment]::SetEnvironmentVariable("Path", $NewPath, "User")
        Write-Success "Added $BinDir to PATH"
    }

    # Also set for current session
    $env:Path = "$BinDir;$env:Path"

    Write-Success "Environment configured"
}

function Update-McpConfig {
    Write-Info "Installing MCP server configuration..."

    $DataDir = "$InstallDir\data"
    $BinaryPath = "$BinDir\ssss.exe"

    # Escape backslashes for JSON
    $BinaryPathJson = $BinaryPath -replace '\\', '\\'
    $DataDirJson = $DataDir -replace '\\', '\\'

    $McpConfig = @"
{
  "ssss": {
    "command": "$BinaryPathJson",
    "args": [],
    "env": {
      "MCP_DB_PATH": "$DataDirJson",
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
}
"@

    # Create Claude plugin directories and .mcp.json files
    $ClaudeDir = "$env:USERPROFILE\.claude"
    $PluginLocations = @(
        "$ClaudeDir\plugins\cache\yzhelezko\ssss\1.0.0",
        "$ClaudeDir\plugins\marketplaces\yzhelezko"
    )

    foreach ($Location in $PluginLocations) {
        # Create directory if it doesn't exist
        if (-not (Test-Path $Location)) {
            New-Item -ItemType Directory -Force -Path $Location | Out-Null
            Write-Info "Created directory: $Location"
        }

        # Always write .mcp.json
        $McpFile = Join-Path $Location ".mcp.json"
        $McpConfig | Set-Content -Path $McpFile -Encoding UTF8
        Write-Info "Created: $McpFile"

        # Also check for version subdirectories
        $VersionDirs = Get-ChildItem -Path $Location -Directory -ErrorAction SilentlyContinue | Where-Object { $_.Name -match '^\d+\.\d+\.\d+$' }
        foreach ($VersionDir in $VersionDirs) {
            $McpFile = Join-Path $VersionDir.FullName ".mcp.json"
            $McpConfig | Set-Content -Path $McpFile -Encoding UTF8
            Write-Info "Created: $McpFile"
        }
    }

    Write-Success "MCP server configuration installed"
}

function Test-Ollama {
    Write-Info "Checking Ollama installation..."

    $OllamaPath = Get-Command "ollama" -ErrorAction SilentlyContinue
    if ($OllamaPath) {
        Write-Success "Ollama is installed"

        $Model = if ($env:MCP_EMBEDDING_MODEL) { $env:MCP_EMBEDDING_MODEL } else { "qwen3-embedding:8b" }
        try {
            $Models = & ollama list 2>$null
            if ($Models -match $Model) {
                Write-Success "Model '$Model' is available"
            }
            else {
                Write-Warn "Model '$Model' not found. Run: ollama pull $Model"
            }
        }
        catch {
            Write-Warn "Could not check models. Ensure Ollama is running."
        }
    }
    else {
        Write-Warn "Ollama not found. Please install from: https://ollama.ai"
    }
}

function Write-NextSteps {
    Write-Host ""
    Write-Host "Installation complete!" -ForegroundColor Green
    Write-Host ""
    Write-Host "Next steps:"
    Write-Host "  1. Ensure Ollama is running: ollama serve"
    Write-Host "  2. Pull the embedding model: ollama pull qwen3-embedding:8b"
    Write-Host "  3. Install the Claude Code plugin: /plugin install github:yzhelezko/ssss-claude-plugin"
    Write-Host "  4. Restart Claude Code to load the plugin"
    Write-Host ""
    Write-Host "Binary location: $BinDir\ssss.exe"
    Write-Host "Data directory:  $InstallDir\data"
    Write-Host ""
    Write-Host "Documentation: https://github.com/yzhelezko/ssss-claude-plugin"
}

# Main
Write-Banner
$Version = Get-LatestVersion
Install-Binary -Version $Version
Set-Environment
Update-McpConfig
Test-Ollama
Write-NextSteps

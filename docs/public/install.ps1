#Requires -Version 5.1
<#
.SYNOPSIS
    One-command zenflow installer for Windows.

.DESCRIPTION
    Detects arch (amd64 / arm64), fetches the matching zip + checksums.txt
    from the latest GitHub Release, verifies SHA-256, extracts to
    $env:ZENFLOW_INSTALL_DIR (default $env:LOCALAPPDATA\Programs\zenflow),
    and prints a PATH hint if the dir is not on the user PATH.

.EXAMPLE
    iwr -useb https://zenflow.sh/install.ps1 | iex

.EXAMPLE
    $env:ZENFLOW_VERSION = 'v0.1.0-pre'; iwr -useb https://zenflow.sh/install.ps1 | iex
#>

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$Repo = 'zendev-sh/zenflow'

# ---------- helpers ----------
function Write-Info($Message) { Write-Host "==> $Message" -ForegroundColor Cyan }
function Write-Err($Message) { Write-Host "error: $Message" -ForegroundColor Red; exit 1 }

# ---------- detect arch ----------
$Arch = switch -Regex ($env:PROCESSOR_ARCHITECTURE) {
    '^(AMD64|x86_64)$' { 'x86_64'; break }
    '^ARM64$'          { 'arm64'; break }
    default            { Write-Err "unsupported arch: $env:PROCESSOR_ARCHITECTURE" }
}

# ---------- resolve version ----------
if ($env:ZENFLOW_VERSION) {
    $Tag = $env:ZENFLOW_VERSION
} else {
    Write-Info 'resolving latest release'
    try {
        $Latest = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest" -UseBasicParsing
        $Tag = $Latest.tag_name
    } catch {
        # No stable release yet (prerelease-only); fall back to most-recent release.
        try {
            $List = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases?per_page=1" -UseBasicParsing
            $Tag = $List[0].tag_name
        } catch {
            Write-Err "could not resolve any release tag: $($_.Exception.Message)"
        }
    }
}
if (-not $Tag) { Write-Err 'empty release tag' }

# goreleaser archive name_template uses {{ .Version }} which strips the leading
# `v` from the tag, so URLs must use the un-prefixed version even when the tag
# itself is `vX.Y.Z`.
$Version = $Tag -replace '^v', ''

$Archive  = "zenflow_${Version}_windows_${Arch}.zip"
$Url      = "https://github.com/$Repo/releases/download/$Tag/$Archive"
$SumsUrl  = "https://github.com/$Repo/releases/download/$Tag/checksums.txt"

# ---------- install dir ----------
if ($env:ZENFLOW_INSTALL_DIR) {
    $InstallDir = $env:ZENFLOW_INSTALL_DIR
} else {
    $InstallDir = Join-Path $env:LOCALAPPDATA 'Programs\zenflow'
}

# ---------- temp workspace ----------
$Tmp = New-Item -ItemType Directory -Path (Join-Path $env:TEMP "zenflow-install-$(Get-Random)")
try {
    $ArchivePath  = Join-Path $Tmp $Archive
    $SumsPath     = Join-Path $Tmp 'checksums.txt'

    Write-Info "downloading $Archive"
    try {
        Invoke-WebRequest -Uri $Url -OutFile $ArchivePath -UseBasicParsing
    } catch {
        Write-Err "download failed: $Url -- $($_.Exception.Message)"
    }

    Write-Info 'downloading checksums.txt'
    try {
        Invoke-WebRequest -Uri $SumsUrl -OutFile $SumsPath -UseBasicParsing
    } catch {
        Write-Err "download failed: $SumsUrl -- $($_.Exception.Message)"
    }

    # ---------- verify ----------
    Write-Info 'verifying SHA-256'
    $ExpectedLine = Get-Content $SumsPath | Where-Object { $_ -match "  $([Regex]::Escape($Archive))$" } | Select-Object -First 1
    if (-not $ExpectedLine) { Write-Err "checksum line for $Archive not found in checksums.txt" }
    $Expected = ($ExpectedLine -split '\s+')[0]

    $Actual = (Get-FileHash -Algorithm SHA256 -Path $ArchivePath).Hash.ToLower()
    if ($Expected.ToLower() -ne $Actual) {
        Write-Err "SHA-256 mismatch (expected $Expected, got $Actual); refusing to install tampered archive"
    }

    # ---------- install ----------
    Write-Info "extracting to $InstallDir"
    if (-not (Test-Path $InstallDir)) {
        New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    }
    Expand-Archive -Path $ArchivePath -DestinationPath $InstallDir -Force

    Write-Info "installed zenflow $Tag to $InstallDir\zenflow.exe"

    # ---------- PATH hint ----------
    $UserPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if ($UserPath -notlike "*$InstallDir*") {
        Write-Host ''
        Write-Host "note: $InstallDir is not on your user PATH." -ForegroundColor Yellow
        Write-Host '  add it for this session:'
        Write-Host "    `$env:Path = `"$InstallDir;`" + `$env:Path"
        Write-Host '  add it permanently (user PATH):'
        Write-Host "    [Environment]::SetEnvironmentVariable('Path', `"$InstallDir;`" + [Environment]::GetEnvironmentVariable('Path','User'), 'User')"
        Write-Host ''
    }

    Write-Info 'verify with: zenflow --version'
}
finally {
    Remove-Item -Recurse -Force $Tmp -ErrorAction SilentlyContinue
}

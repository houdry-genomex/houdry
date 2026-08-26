# Houdry public installer — Windows PowerShell 5+ / PowerShell 7
# Usage:
#   irm https://github.com/Orchestrator-sih-2026/houdry/releases/latest/download/install.ps1 | iex
$ErrorActionPreference = "Stop"

$Repo = if ($env:HOODRY_REPO) { $env:HOODRY_REPO } else { "Orchestrator-sih-2026/houdry" }
$Version = if ($env:HOODRY_VERSION) { $env:HOODRY_VERSION } else { "latest" }

$arch = "amd64"
try {
  $a = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLower()
  switch ($a) {
    "x64" { $arch = "amd64" }
    "arm64" { $arch = "arm64" }
    default { $arch = $a }
  }
} catch {
  if (-not [Environment]::Is64BitOperatingSystem) {
    throw "Unsupported architecture: 32-bit Windows"
  }
}

$os = "windows"
if ($IsLinux) { $os = "linux" }
elseif ($IsMacOS) { $os = "darwin" }

$ext = ""
if ($os -eq "windows") { $ext = ".exe" }
$asset = "houdry-$os-$arch$ext"
if ($Version -eq "latest") {
  $url = "https://github.com/$Repo/releases/latest/download/$asset"
} else {
  $url = "https://github.com/$Repo/releases/download/$Version/$asset"
}

Write-Host "Downloading Houdry ($Version) for $os/$arch"
Write-Host "  $url"

$homeDir = if ($env:HOODRY_HOME) { $env:HOODRY_HOME } else { Join-Path $HOME ".houdry" }
$binDir = Join-Path $homeDir "bin"
New-Item -ItemType Directory -Force -Path $binDir | Out-Null
$dest = Join-Path $binDir "houdry$ext"

[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
Invoke-WebRequest -Uri $url -OutFile $dest -UseBasicParsing

$configPath = Join-Path $homeDir "config.json"
if (-not (Test-Path $configPath)) {
  $server = if ($env:HOODRY_SERVER) { $env:HOODRY_SERVER } else { "" }
  $config = @{ server = $server; node_id = "" } | ConvertTo-Json
  Set-Content -Path $configPath -Value $config -Encoding UTF8
}

if ($os -eq "windows") {
  $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
  if (-not $userPath) { $userPath = "" }
  if ($userPath -notlike "*$binDir*") {
    $joined = if ($userPath) { "$binDir;$userPath" } else { $binDir }
    [Environment]::SetEnvironmentVariable("Path", $joined, "User")
  }
  $env:Path = "$binDir;$env:Path"
} else {
  $env:PATH = "$binDir;$env:PATH"
}

Write-Host ""
Write-Host "Houdry installed to $dest"
Write-Host ""
Write-Host "Detect GPUs on this machine:"
Write-Host "  houdry gpu detect"
Write-Host ""
Write-Host "If 'houdry' is not found, open a new terminal."

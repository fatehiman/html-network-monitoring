# Builds netmon for Windows and Linux into ..\dist (no C compiler needed).
# Usage: .\build.ps1 [-Version 1.0.0]
param([string]$Version = "")
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot
if (-not $Version) { $Version = (git describe --tags --always --dirty 2>$null); if (-not $Version) { $Version = "dev" } }
$out = Join-Path $PSScriptRoot "..\dist"
if (Test-Path $out) { Remove-Item -Recurse -Force $out }
New-Item -ItemType Directory -Force $out | Out-Null
$env:CGO_ENABLED = "0"; $env:GOARM = "7"
foreach ($t in "windows/amd64","windows/arm64","linux/amd64","linux/arm64","linux/arm") {
  $os, $arch = $t.Split("/")
  $name = "netmon-$os-$arch"; $gui = ""
  # Windows: GUI program (tray icon, no console window)
  if ($os -eq "windows") { $name += ".exe"; $gui = "-H windowsgui" }
  Write-Host "building $name"
  $env:GOOS = $os; $env:GOARCH = $arch
  go build -trimpath -ldflags "-s -w $gui -X main.version=$Version" -o (Join-Path $out $name) .
  if ($LASTEXITCODE -ne 0) { throw "build failed: $t" }
}
Remove-Item Env:GOOS, Env:GOARCH
Get-ChildItem $out

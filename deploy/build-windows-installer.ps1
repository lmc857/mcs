# Build mcs.exe + MCS-Setup-*.exe on Windows.
# Prerequisites: Go 1.24+, Inno Setup 6 (https://jrsoftware.org/isinfo.php)
param(
  [string]$Version = "0.1.0",
  [string]$InnoSetup = "C:\Program Files (x86)\Inno Setup 6\ISCC.exe"
)
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Push-Location $root
try {
  New-Item -ItemType Directory -Force -Path "dist" | Out-Null
  $env:CGO_ENABLED = "0"
  $env:GOOS = "windows"
  $env:GOARCH = "amd64"
  go build -trimpath -ldflags "-X main.version=$Version" -o "dist\mcs-windows-amd64.exe" ".\cmd\mcs"
  if (-not (Test-Path $InnoSetup)) {
    throw "Inno Setup compiler not found: $InnoSetup`nInstall Inno Setup 6 or pass -InnoSetup <ISCC.exe>"
  }
  & $InnoSetup "/DMyAppVersion=$Version" "deploy\mcs.iss"
  Write-Host "Installer: dist\MCS-Setup-$Version.exe"
} finally {
  Pop-Location
}

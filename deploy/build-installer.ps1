# Build MCS-Setup-*.exe from the prebuilt dist\mcs-windows-amd64.exe.
# Requires Inno Setup 6 installed; Go is NOT required.
param(
  [string]$Version = "0.1.0",
  [string]$InnoSetup = "C:\Program Files (x86)\Inno Setup 6\ISCC.exe"
)
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Push-Location $root
try {
  $exe = Join-Path $root "dist\mcs-windows-amd64.exe"
  if (-not (Test-Path $exe)) { throw "missing $exe" }
  if (-not (Test-Path $InnoSetup)) {
    throw "Inno Setup compiler not found: $InnoSetup`nInstall Inno Setup 6 or pass -InnoSetup <ISCC.exe>"
  }
  & $InnoSetup "/DMyAppVersion=$Version" (Join-Path $root "deploy\mcs.iss")
  if ($LASTEXITCODE -ne 0) { throw "Inno Setup failed with exit code $LASTEXITCODE" }
  Write-Host "Installer: dist\MCS-Setup-$Version.exe"
} finally {
  Pop-Location
}

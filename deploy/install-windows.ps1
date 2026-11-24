# MCS Windows installer (native Windows Service).
# Usage (Administrator PowerShell, from repo root):
#   powershell -ExecutionPolicy Bypass -File deploy\install-windows.ps1
param(
  [string]$Binary = "dist\mcs-windows-amd64.exe",
  [string]$InstallDir = "C:\Program Files\MCS",
  [string]$DataDir = "C:\ProgramData\MCS",
  [int]$HTTPPort = 8080
)
$ErrorActionPreference = "Stop"
if (-not ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
  throw "Run PowerShell as Administrator"
}
if (-not (Test-Path $Binary)) { throw "binary not found: $Binary" }
New-Item -ItemType Directory -Force -Path $InstallDir, $DataDir, "$DataDir\data" | Out-Null
Copy-Item $Binary "$InstallDir\mcs.exe" -Force
$config = "$InstallDir\config.yaml"
if (-not (Test-Path $config)) {
  Copy-Item (Join-Path $PSScriptRoot "..\config.example.yaml") $config
  $raw = (Get-Content $config -Raw -Encoding UTF8).Replace('data_dir: "./data"', ('data_dir: "' + ($DataDir -replace '\\','/') + '/data"'))
  [System.IO.File]::WriteAllText($config, $raw, ([System.Text.UTF8Encoding]::new($false)))
  Write-Host "Installed default config: $config (edit advertise/domain/devices)"
}

# Remove the old scheduled-task deployment if present.
schtasks /Delete /TN "MCS" /F 2>$null | Out-Null

# Remove an existing service so binPath/log changes take effect.
$existing = Get-Service -Name "MCS" -ErrorAction SilentlyContinue
if ($existing) {
  if ($existing.Status -ne "Stopped") { Stop-Service -Name "MCS" -Force -ErrorAction SilentlyContinue; Start-Sleep -Seconds 2 }
  sc.exe delete MCS | Out-Null
  Start-Sleep -Seconds 1
}

$logFile = "$DataDir\mcs.log"
$binPath = "`"$InstallDir\mcs.exe`" -service -config `"$config`" -log `"$logFile`""
New-Service -Name "MCS" -BinaryPathName $binPath -DisplayName "MCS 报警对讲广播服务器" -StartupType Automatic
sc.exe description MCS "MCS SIP server (alarm / intercom / broadcast)" | Out-Null
sc.exe failure MCS reset= 86400 actions= restart/5000/restart/10000/restart/30000 | Out-Null
sc.exe failureflag MCS 1 | Out-Null
Start-Service -Name "MCS"

# SIP/HTTP ports.
New-NetFirewallRule -DisplayName "MCS SIP UDP" -Direction Inbound -Protocol UDP -LocalPort 5060 -Action Allow -ErrorAction SilentlyContinue | Out-Null
New-NetFirewallRule -DisplayName "MCS SIP TLS" -Direction Inbound -Protocol TCP -LocalPort 5061 -Action Allow -ErrorAction SilentlyContinue | Out-Null
Remove-NetFirewallRule -DisplayName "MCS Web" -ErrorAction SilentlyContinue
New-NetFirewallRule -DisplayName "MCS Web" -Direction Inbound -Protocol TCP -LocalPort $HTTPPort -Action Allow -ErrorAction SilentlyContinue | Out-Null
New-NetFirewallRule -DisplayName "MCS Web HTTPS" -Direction Inbound -Protocol TCP -LocalPort 8443 -Action Allow -ErrorAction SilentlyContinue | Out-Null
# RTP/RTCP use dynamic UDP ports, scoped to mcs.exe.
New-NetFirewallRule -DisplayName "MCS RTP dynamic UDP" -Direction Inbound -Program "$InstallDir\mcs.exe" -Protocol UDP -Action Allow -ErrorAction SilentlyContinue | Out-Null

Get-Service -Name "MCS" | Format-Table Name, Status, StartType
Write-Host "MCS installed as a Windows Service."
Write-Host "Config : $config"
Write-Host "Log    : $logFile"
Write-Host "Edit config, then: Restart-Service MCS"
Write-Host "Web console: http://<host>:8080"

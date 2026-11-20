# Create/start the MCS Windows Service using the HTTP port from config.yaml.
param(
  [Parameter(Mandatory=$true)][string]$InstallDir,
  [Parameter(Mandatory=$true)][string]$DataDir,
  [int]$HTTPPort = 0,
  [switch]$Uninstall,
  [switch]$StopOnly
)
$ErrorActionPreference = "Stop"
$serviceName = "MCS"
$exe = Join-Path $InstallDir "mcs.exe"
$config = Join-Path $InstallDir "config.yaml"
$log = Join-Path $DataDir "mcs.log"
$installLog = Join-Path $DataDir "install-service.log"
$statusFile = Join-Path $DataDir "install-status.txt"

New-Item -ItemType Directory -Force -Path $DataDir | Out-Null
function Write-Log([string]$Message) {
  $line = "$(Get-Date -Format o) $Message"
  Add-Content -Path $installLog -Value $line -Encoding UTF8
  Write-Host $Message
}
function Set-Status([string]$State, [string]$Message) {
  "$State`n$Message`n$((Get-Date).ToString('o'))" |
    Out-File -FilePath $statusFile -Encoding UTF8
}
function Remove-OldTask {
  # schtasks.exe may be unavailable in minimal Windows images.
  # Route through cmd.exe and suppress both output and stderr.
  & cmd.exe /c 'schtasks /Delete /TN "MCS" /F >nul 2>&1' | Out-Null
}
function Stop-MCS {
  $svc = Get-Service -Name $serviceName -ErrorAction SilentlyContinue
  if ($svc) {
    if ($svc.Status -ne "Stopped") {
      Stop-Service -Name $serviceName -Force -ErrorAction SilentlyContinue
      Start-Sleep -Seconds 2
    }
    & cmd.exe /c ('sc.exe delete ' + $serviceName + ' >nul 2>&1') | Out-Null
    for ($i = 0; $i -lt 10; $i++) {
      if (-not (Get-Service -Name $serviceName -ErrorAction SilentlyContinue)) { break }
      Start-Sleep -Seconds 1
    }
  }
}
function Remove-FirewallRules {
  @("MCS SIP UDP","MCS SIP TLS","MCS Web","MCS Web HTTPS","MCS RTP dynamic UDP") | ForEach-Object {
    Remove-NetFirewallRule -DisplayName $_ -ErrorAction SilentlyContinue
  }
}

Write-Log "MCS install-service: InstallDir=$InstallDir DataDir=$DataDir Uninstall=$Uninstall"

if ($Uninstall) {
  Stop-MCS
  Remove-OldTask
  Remove-FirewallRules
  Write-Log "MCS service and firewall rules removed. Data kept: $DataDir"
  exit 0
}
if ($StopOnly) {
  Stop-MCS
  Remove-OldTask
  Write-Log "Old MCS service stopped before upgrade"
  exit 0
}

if (-not (Test-Path $exe)) { throw "mcs.exe not found: $exe" }
if (-not (Test-Path $config)) { throw "config.yaml not found: $config" }
New-Item -ItemType Directory -Force -Path (Join-Path $DataDir "data") | Out-Null

if ($HTTPPort -le 0) {
  $m = Select-String -Path $config -Pattern '^\s*http:\s*"([^"]+)"' | Select-Object -First 1
  if ($m -and $m.Matches[0].Groups[1].Value -match ':(\d+)$') {
    $HTTPPort = [int]$Matches[1]
  } else {
    $HTTPPort = 8080
  }
}
Write-Log "HTTP port from config: $HTTPPort"

Remove-OldTask
Stop-MCS
# A manually started mcs.exe holds 5060/8080 and the SQLite DB; stop it before the service.
Get-Process -Name mcs -ErrorAction SilentlyContinue | ForEach-Object {
  try {
    Stop-Process -Id $_.Id -Force -ErrorAction Stop
    Write-Log "Stopped manually running mcs.exe PID=$($_.Id) path=$($_.Path)"
  } catch {
    Write-Log "Could not stop mcs.exe PID=$($_.Id): $_"
  }
}
Start-Sleep -Seconds 1

$binPath = "`"$exe`" -service -config `"$config`" -log `"$log`""
Write-Log "Creating service with: $binPath"
try {
  New-Service -Name $serviceName -BinaryPathName $binPath -DisplayName "MCS 报警对讲广播服务器" -StartupType Automatic
  sc.exe description $serviceName "MCS SIP server (alarm / intercom / broadcast)" | Out-Null
  sc.exe failure $serviceName reset= 86400 actions= restart/5000/restart/10000/restart/30000 | Out-Null
} catch {
  Set-Status "FAIL" "Create service failed: $_"
  throw
}

Remove-NetFirewallRule -DisplayName "MCS Web" -ErrorAction SilentlyContinue
New-NetFirewallRule -DisplayName "MCS SIP UDP" -Direction Inbound -Protocol UDP -LocalPort 5060 -Action Allow -ErrorAction SilentlyContinue | Out-Null
New-NetFirewallRule -DisplayName "MCS SIP TLS" -Direction Inbound -Protocol TCP -LocalPort 5061 -Action Allow -ErrorAction SilentlyContinue | Out-Null
New-NetFirewallRule -DisplayName "MCS Web" -Direction Inbound -Protocol TCP -LocalPort $HTTPPort -Action Allow -ErrorAction SilentlyContinue | Out-Null
New-NetFirewallRule -DisplayName "MCS Web HTTPS" -Direction Inbound -Protocol TCP -LocalPort 8443 -Action Allow -ErrorAction SilentlyContinue | Out-Null
New-NetFirewallRule -DisplayName "MCS RTP dynamic UDP" -Direction Inbound -Program $exe -Protocol UDP -Action Allow -ErrorAction SilentlyContinue | Out-Null

try {
  Start-Service -Name $serviceName
  Start-Sleep -Seconds 4
  $svc = Get-Service -Name $serviceName
  if ($svc.Status -ne "Running") {
    throw "service status=$($svc.Status)"
  }
} catch {
  $msg = "MCS service failed to start: $_`nLog: $log`nInstall log: $installLog"
  Set-Status "FAIL" $msg
  Write-Log "FAIL: $msg"
  throw $msg
}

$svc = Get-Service -Name $serviceName
Set-Status "OK" "service=Running http=0.0.0.0:$HTTPPort log=$log"
Write-Log "MCS service is running. Web: http://127.0.0.1:$HTTPPort/ Log: $log"
$svc | Format-Table Name, Status, StartType

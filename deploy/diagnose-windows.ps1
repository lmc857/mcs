# Read-only diagnostics for MCS on Windows. Run as Administrator.
$ErrorActionPreference = "Continue"
$install = "C:\Program Files\MCS"
$data = "C:\ProgramData\MCS"
$config = Join-Path $install "config.yaml"
$log = Join-Path $data "mcs.log"
$installLog = Join-Path $data "install-service.log"

Write-Host "`n=== MCS service ===" -ForegroundColor Cyan
Get-Service -Name MCS -ErrorAction SilentlyContinue | Format-List Name,Status,StartType
sc.exe qc MCS
sc.exe queryex MCS

Write-Host "`n=== mcs.exe process ===" -ForegroundColor Cyan
Get-CimInstance Win32_Process -Filter "Name='mcs.exe'" | Select-Object ProcessId,ExecutablePath,CommandLine | Format-List

Write-Host "`n=== config (selected) ===" -ForegroundColor Cyan
if (Test-Path $config) {
  Get-Content $config | Select-String -Pattern '^\s*(sip_udp|http|https|advertise|data_dir|sip_tls):'
} else {
  Write-Host "config not found: $config"
}

$http = $null
$port = 8080
if (Test-Path $config) {
  $m = Select-String -Path $config -Pattern '^\s*http:\s*"([^"]+)"' | Select-Object -First 1
  if ($m) {
    $http = $m.Matches.Groups[1].Value
    if ($http -match ':(\d+)$') { $port = [int]$Matches[1] }
  }
}
Write-Host "HTTP bind: $http  port: $port"

Write-Host "`n=== listeners ===" -ForegroundColor Cyan
Get-NetTCPConnection -State Listen -ErrorAction SilentlyContinue | Where-Object { $_.LocalPort -in @($port, 5060, 8443) } | Format-Table LocalAddress,LocalPort,OwningProcess -AutoSize
Get-NetUDPEndpoint -ErrorAction SilentlyContinue | Where-Object { $_.LocalPort -eq 5060 } | Format-Table LocalAddress,LocalPort,OwningProcess -AutoSize

Write-Host "`n=== local HTTP probe ===" -ForegroundColor Cyan
try { curl.exe -sS -m 5 -o NUL -w "HTTP %{http_code}`n" "http://127.0.0.1:$port/api/v1/healthz" } catch { Write-Host $_ }
try { Test-NetConnection -ComputerName 127.0.0.1 -Port $port -WarningAction SilentlyContinue | Select-Object ComputerName,RemotePort,TcpTestSucceeded | Format-Table } catch {}

Write-Host "`n=== firewall rules ===" -ForegroundColor Cyan
Get-NetFirewallRule -DisplayName "MCS*" -ErrorAction SilentlyContinue | Format-Table DisplayName,Enabled,Direction,Action -AutoSize

Write-Host "`n=== logs ===" -ForegroundColor Cyan
foreach ($f in @($log,$installLog)) {
  Write-Host "-- $f"
  if (Test-Path $f) { Get-Content $f -Tail 80 } else { Write-Host "missing" }
}

Write-Host "`n=== recent service errors ===" -ForegroundColor Cyan
Get-WinEvent -FilterHashtable @{LogName='System'; StartTime=(Get-Date).AddHours(-2)} -ErrorAction SilentlyContinue |
  Where-Object { $_.ProviderName -eq 'Service Control Manager' -and $_.Message -like '*MCS*' } |
  Select-Object -First 10 TimeCreated,Id,LevelDisplayName,Message | Format-List

Write-Host "`nDone. Copy this output and C:\ProgramData\MCS\mcs.log for support." -ForegroundColor Green

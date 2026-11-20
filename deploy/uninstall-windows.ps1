# Stop and remove the MCS Windows Service (keeps config/data).
$ErrorActionPreference = "Stop"
$svc = Get-Service -Name "MCS" -ErrorAction SilentlyContinue
if ($svc) {
  if ($svc.Status -ne "Stopped") { Stop-Service -Name "MCS" -Force; Start-Sleep -Seconds 2 }
  sc.exe delete MCS | Out-Null
}
schtasks /Delete /TN "MCS" /F 2>$null | Out-Null
Write-Host "MCS service removed. Config/data under C:\Program Files\MCS and C:\ProgramData\MCS were kept."

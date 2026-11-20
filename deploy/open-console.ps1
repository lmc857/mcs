# Open the MCS console using the HTTP port currently in config.yaml.
param([string]$Config = "C:\Program Files\MCS\config.yaml")
$ErrorActionPreference = "Continue"
if (-not (Test-Path $Config)) {
  Write-Host "config not found: $Config"
  exit 1
}
$m = Select-String -Path $Config -Pattern '^\s*http:\s*"([^"]+)"' | Select-Object -First 1
$port = 8080
if ($m -and $m.Matches[0].Groups[1].Value -match ':(\d+)$') { $port = [int]$Matches[1] }
$url = "http://127.0.0.1:$port/"
try {
  $res = Invoke-WebRequest -UseBasicParsing -Uri ($url + "api/v1/healthz") -TimeoutSec 5
  Write-Host "MCS console: $url  health=$($res.StatusCode)"
  Start-Process $url
} catch {
  Write-Host "MCS console not ready: $url"
  Write-Host $_
  exit 1
}

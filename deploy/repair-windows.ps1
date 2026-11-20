# One-click repair: bind HTTP to 0.0.0.0, open firewall, restart service, open console.
# Called by MCS一键修复.cmd. Must run elevated.
$ErrorActionPreference = "Stop"
$install = "C:\Program Files\MCS"
$data = "C:\ProgramData\MCS"
$config = Join-Path $install "config.yaml"
$log = Join-Path $data "mcs.log"

Write-Host "=== MCS 一键修复 ===" -ForegroundColor Cyan
if (-not (Test-Path $config)) { throw "找不到配置：$config" }
$svc = Get-Service -Name MCS -ErrorAction SilentlyContinue
if (-not $svc) { throw "找不到 MCS 服务，请先运行 MCS-Setup 安装" }

$text = Get-Content $config -Raw -Encoding UTF8
$m = [regex]::Match($text, '(?m)^\s*http:\s*"([^"]+)"')
if (-not $m.Success) { throw "配置里找不到 http: 行：$config" }
$http = $m.Groups[1].Value
$port = 8080
if ($http -match ':(\d+)$') { $port = [int]$Matches[1] }
Write-Host "当前 HTTP 配置：$http，端口：$port"

Copy-Item $config "$config.bak" -Force
$text = [regex]::Replace($text, '(?m)^\s*http:\s*"[^"]+"', ('http: "0.0.0.0:' + $port + '"'))
[System.IO.File]::WriteAllText($config, $text, ([System.Text.UTF8Encoding]::new($false)))
Write-Host "已改为 http: \"0.0.0.0:$port\"（备份：$config.bak）"

Remove-NetFirewallRule -DisplayName "MCS Web" -ErrorAction SilentlyContinue
New-NetFirewallRule -DisplayName "MCS Web" -Direction Inbound -Protocol TCP -LocalPort $port -Action Allow | Out-Null
Write-Host "已放行防火墙 TCP $port"

Restart-Service -Name MCS -Force
Start-Sleep -Seconds 4
$svc = Get-Service -Name MCS
Write-Host "服务状态：$($svc.Status)"
if ($svc.Status -ne "Running") {
  Write-Host "服务未运行，最近日志：" -ForegroundColor Yellow
  if (Test-Path $log) { Get-Content $log -Tail 80 }
  throw "MCS 服务没有启动，请看上面的日志"
}

try {
  $res = Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:$port/api/v1/healthz" -TimeoutSec 5
  Write-Host "本机健康检查：HTTP $($res.StatusCode) $($res.Content)"
} catch {
  Write-Host "本机 HTTP 检查失败：$_" -ForegroundColor Yellow
  if (Test-Path $log) { Get-Content $log -Tail 80 }
  throw
}

$url = "http://127.0.0.1:$port/"
Write-Host "修复完成，正在打开：$url" -ForegroundColor Green
Start-Process $url

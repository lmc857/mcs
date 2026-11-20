# Auto-detect LAN IP and free HTTP port, then update a fresh MCS config.
param(
  [Parameter(Mandatory=$true)][string]$Config,
  [Parameter(Mandatory=$true)][string]$DataDir,
  [string]$ServerIP = "auto",
  [int]$HTTPPort = 0
)
$ErrorActionPreference = "Stop"
$script:log = Join-Path $DataDir "prepare-config.log"
New-Item -ItemType Directory -Force -Path $DataDir | Out-Null
function Write-ConfigLog([string]$Message) {
  Add-Content -Path $script:log -Value ("$(Get-Date -Format o) $Message") -Encoding UTF8
}
trap {
  $err = $_ | Out-String
  try { Write-ConfigLog "ERROR: $err" } catch {}
  Write-Host $err
  exit 1
}

function Get-LanIP {
  try {
    $route = Get-NetRoute -DestinationPrefix "0.0.0.0/0" -ErrorAction SilentlyContinue |
      Sort-Object RouteMetric | Select-Object -First 1
    if ($route) {
      $ip = Get-NetIPAddress -AddressFamily IPv4 -InterfaceIndex $route.InterfaceIndex -ErrorAction SilentlyContinue |
        Where-Object { $_.IPAddress -notlike "127.*" -and $_.IPAddress -notlike "169.254.*" } |
        Select-Object -First 1 -ExpandProperty IPAddress
      if ($ip) { return $ip }
    }
  } catch {}
  try {
    $ip = Get-NetIPAddress -AddressFamily IPv4 -ErrorAction SilentlyContinue |
      Where-Object { $_.IPAddress -notlike "127.*" -and $_.IPAddress -notlike "169.254.*" } |
      Sort-Object InterfaceMetric | Select-Object -First 1 -ExpandProperty IPAddress
    if ($ip) { return $ip }
  } catch {}
  return "127.0.0.1"
}

function Test-PortFree([int]$Port) {
  try {
    $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Any, $Port)
    $listener.Start()
    $listener.Stop()
    return $true
  } catch {
    return $false
  }
}

function Get-FreeHTTPPort {
  foreach ($p in @(8080, 18080, 18081, 18082, 18083, 18084)) {
    if (Test-PortFree $p) { return $p }
  }
  for ($p = 18085; $p -le 18150; $p++) {
    if (Test-PortFree $p) { return $p }
  }
  throw "没有找到可用的 HTTP 端口（8080/18080-18150）"
}

Write-ConfigLog "start: config=$Config data=$DataDir requestedIP=$ServerIP requestedPort=$HTTPPort"
if (-not (Test-Path $Config)) { throw "config not found: $Config" }
$text = Get-Content $Config -Raw -Encoding UTF8

if ($ServerIP -eq "" -or $ServerIP -eq "auto") {
  $ServerIP = Get-LanIP
}
if ($ServerIP -notmatch '^\d{1,3}(\.\d{1,3}){3}$') {
  throw "invalid ServerIP: $ServerIP"
}

$m = [regex]::Match($text, '(?m)^\s*http:\s*"([^"]+)"')
$currentPort = 0
if ($m.Success -and $m.Groups[1].Value -match ':(\d+)$') {
  $currentPort = [int]$Matches[1]
}

if ($HTTPPort -le 0) {
  # 如果已有非默认的显式 HTTP 端口，升级时保留；否则自动选空闲端口。
  if ($currentPort -gt 0 -and $currentPort -ne 8080) {
    $HTTPPort = $currentPort
  } else {
    $HTTPPort = Get-FreeHTTPPort
  }
}
if (-not (Test-PortFree $HTTPPort)) {
  $HTTPPort = Get-FreeHTTPPort
}

$data = ($DataDir -replace '\\','/') + '/data'
if ($text -match '(?m)^\s*advertise:\s*"(127\.0\.0\.1|0\.0\.0\.0):5060"') {
  $text = [regex]::Replace($text, '(?m)^\s*advertise:\s*"[^"]+"', ('  advertise: "' + $ServerIP + ':5060"'))
}
$text = [regex]::Replace($text, '(?m)^\s*http:\s*"[^"]+"', ('  http: "0.0.0.0:' + $HTTPPort + '"'))
$text = $text.Replace('data_dir: "./data"', ('data_dir: "' + $data + '"'))
[System.IO.File]::WriteAllText($Config, $text, ([System.Text.UTF8Encoding]::new($false)))

$info = @(
  "SERVER_IP=$ServerIP",
  "HTTP_PORT=$HTTPPort",
  "CONFIG=$Config",
  "DATA_DIR=$DataDir"
)
[System.IO.File]::WriteAllLines((Join-Path $DataDir "install-info.txt"), $info, ([System.Text.UTF8Encoding]::new($false)))
Write-ConfigLog "selected: ip=$ServerIP port=$HTTPPort data=$data"
Write-Host "Auto config: advertise=${ServerIP}:5060 http=0.0.0.0:$HTTPPort data=$data"

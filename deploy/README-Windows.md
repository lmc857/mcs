# MCS Windows 安装包构建与排查

这个目录包含预编译的 Windows exe，不需要 Go。

## 0. 最省事的方式（不用打开 PowerShell）

> 建议解压到 `C:\MCS` 或 `D:\MCS` 这种纯英文路径。Windows 的 CMD 对含 emoji、中文、特殊字符的路径兼容性较差，容易出现“命令被截断”的报错。

- **安装**：双击 `MCS-Setup-*.exe`，按向导下一步即可。
- **页面打不开**：双击 `MCS-Diagnose.cmd`，会自动请求管理员权限，收集报告并用记事本打开。
- **想自动修一下**：双击 `MCS-Repair.cmd`，会自动把 HTTP 改成 `0.0.0.0`、放行防火墙、重启服务并打开控制台。

这两个 `.cmd` 内部使用 Windows 自带 PowerShell，但不需要你打开 PowerShell、输入命令或改脚本。  
`.cmd` 已使用纯 ASCII + CRLF，`.ps1` 已加 UTF-8 BOM，避免中文 Windows 下 CMD 解析乱码。

## 1. 直接安装服务（不生成安装包）

管理员 PowerShell：

```powershell
powershell -ExecutionPolicy Bypass -File .\deploy\install-windows.ps1 `
  -Binary .\dist\mcs-windows-amd64.exe `
  -InstallDir "C:\Program Files\MCS" `
  -DataDir "C:\ProgramData\MCS"
```

编辑 `C:\Program Files\MCS\config.yaml`：

```yaml
server:
  advertise: "本机局域网IP:5060"
  http: "0.0.0.0:8080"
```

重启并查看日志：

```powershell
Restart-Service MCS
Get-Content C:\ProgramData\MCS\mcs.log -Tail 80
```

## 2. 生成安装包（双击即可）

1. 安装 [Inno Setup 6](https://jrsoftware.org/isinfo.php)；
2. 双击包根目录的：

```text
Build-Installer.cmd
```

不需要打开 PowerShell、不需要 Go。脚本会自动找 Inno Setup，找不到会打开下载页。

产物：

```text
dist\MCS-Setup-0.16.1.exe
```

也可以指定版本：

```bat
Build-Installer.cmd 0.16.1
```

> 如果安装失败且没有日志，双击 `Install-Debug.cmd` 会在窗口显示每一步错误。0.16.1 已修复 PowerShell 5.1 下 UTF-8 构造函数的兼容问题，并增加了 `installer.log`、`prepare-config.log`。

如果使用命令行，仍然可以：

```powershell
powershell -ExecutionPolicy Bypass -File .\deploy\build-installer.ps1 -Version 0.16.1
```

安装包生成后，双击 `dist\MCS-Setup-0.16.1.exe`，按向导下一步。**不再要求填写 IP 和端口**：

- 自动检测默认路由所在网卡的局域网 IPv4；
- 自动从 8080、18080-18150 中选择第一个空闲端口；
- 自动写入配置、防火墙并启动 `MCS` 服务；
- 完成后自动打开控制台。

检测结果保存在：

```text
C:\ProgramData\MCS\install-info.txt
```

如果自动选错网卡，编辑 `C:\Program Files\MCS\config.yaml` 的 `server.advertise` 后重启服务。

静默安装也可以手动指定（可选）：

```powershell
.\dist\MCS-Setup-0.16.1.exe /VERYSILENT /SERVERIP=192.168.68.106 /HTTPPORT=8080
```

## 2.1 首次登录

首次启动会创建一个 admin 用户：

- 如果 `config.yaml` 设置了 `auth.console_password`，使用该密码；
- 如果没有设置，会自动生成随机密码，保存到：

```text
C:\ProgramData\MCS\data\initial-admin.txt
```

登录后请立即在“用户管理”里修改密码，并删除该文件。

如果网页提示 `/api/v1/devices HTTP 401`：

1. `Ctrl+F5` 强制刷新；
2. 使用上面的初始管理员账号登录；
3. 会话在服务重启后失效，重新登录即可。

## 3. 控制页面打不开怎么查

优先双击 `MCS-Diagnose.cmd`；它会自动生成桌面报告并打开。

也可以手动运行（管理员）：

```powershell
powershell -ExecutionPolicy Bypass -File "C:\Program Files\MCS\diagnose-windows.ps1"
```

或按顺序查：

### 3.1 服务是否运行

```powershell
Get-Service MCS
sc.exe queryex MCS
```

如果不是 `Running`：

```powershell
Get-Content C:\ProgramData\MCS\mcs.log -Tail 100
Get-Content C:\ProgramData\MCS\install-service.log -Tail 100
```

日志不存在，说明服务没起来，查系统事件：

```powershell
Get-WinEvent -FilterHashtable @{LogName='System'; ProviderName='Service Control Manager'} -MaxEvents 50 |
  Where-Object { $_.Message -like '*MCS*' }
```

### 3.2 配置是否监听 0.0.0.0

```powershell
Get-Content "C:\Program Files\MCS\config.yaml" |
  Select-String 'http|advertise|data_dir'
```

正确示例：

```yaml
http: "0.0.0.0:8080"
advertise: "192.168.68.106:5060"
data_dir: "C:/ProgramData/MCS/data"
```

如果 `http` 是 `127.0.0.1:8080`：

- 本机 `http://127.0.0.1:8080` 可能能开；
- 局域网其他电脑打不开；
- 改成 `0.0.0.0:8080`，然后：

```powershell
Restart-Service MCS
```

### 3.3 本机是否能访问

```powershell
curl.exe -v http://127.0.0.1:8080/api/v1/healthz
```

- 本机能访问、别的电脑不能：防火墙或绑定地址；
- 本机也不能：服务/端口/配置问题。

### 3.4 端口是否被占用

```powershell
Get-NetTCPConnection -LocalPort 8080 -State Listen
netstat -ano | findstr :8080
Get-CimInstance Win32_Process -Filter "Name='mcs.exe'" | Select ProcessId,CommandLine
```

如果端口被 IIS/其他程序占用，安装向导里换 `18080` 等端口。

### 3.5 防火墙

```powershell
Get-NetFirewallRule -DisplayName "MCS*" |
  Format-Table DisplayName,Enabled,Direction,Action
```

如果安装时用了非 8080 端口：

```powershell
New-NetFirewallRule -DisplayName "MCS Web 18080" `
  -Direction Inbound -Protocol TCP -LocalPort 18080 -Action Allow
```

### 3.6 从别的电脑测

```powershell
Test-NetConnection 192.168.68.106 -Port 8080
```

失败一般是防火墙，或 `http` 仍绑定在 `127.0.0.1`。

## 4. 日志位置

```text
C:\ProgramData\MCS\mcs.log
C:\ProgramData\MCS\install-service.log
```

实时查看：

```powershell
Get-Content C:\ProgramData\MCS\mcs.log -Wait -Tail 50
```

## 5. 常用命令

```powershell
Get-Service MCS
Restart-Service MCS
Stop-Service MCS
Start-Service MCS
sc.exe qc MCS
Get-Content C:\ProgramData\MCS\mcs.log -Tail 100
```

## 6. 注意事项

- 安装包未做代码签名，SmartScreen 可能提示；
- 服务默认 LocalSystem；
- 卸载默认保留 `C:\ProgramData\MCS` 数据；
- Inno Setup 警告 `x64 deprecated` 和 `RunOnceId` 已在新脚本修复；
- 需要把 `mcs.log` 和诊断脚本输出发给支持人员。

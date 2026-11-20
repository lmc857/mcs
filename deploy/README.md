# MCS 部署（M1）

## Linux（systemd）

```bash
make package
tar xzf dist/mcs-linux-amd64.tar.gz && cd mcs
sudo ./install-linux.sh ./mcs ./config.example.yaml
sudo vi /etc/mcs/config.yaml     # domain / advertise / devices
sudo systemctl restart mcs
```

也可以从仓库直接安装：`sudo ./deploy/install-linux.sh dist/mcs-linux-amd64`。

- 程序：`/opt/mcs/mcs`
- 配置：`/etc/mcs/config.yaml`
- 数据：`/var/lib/mcs/data`（含 `mcs.db`、录音、音频库）
- 日志：`journalctl -u mcs -f`
- 默认端口：SIP UDP 5060、Web 8080
- 控制台登录：编辑 `/etc/mcs/config.yaml` 的 `auth.console_password`（非空启用，默认用户 `admin`）

## Windows（原生 Windows Service）

Windows 10/11 或 Windows Server x64。

### 生成安装包（Inno Setup）

在 Windows 构建机安装 Go 1.24+ 和 [Inno Setup 6](https://jrsoftware.org/isinfo.php)，然后：

```powershell
powershell -ExecutionPolicy Bypass -File deploy\build-windows-installer.ps1 -Version 0.1.0
```

产物：

```text
dist\MCS-Setup-0.1.0.exe
```

安装包是标准 Windows 向导，普通用户双击即可：

1. 选择安装目录；
2. 填写 **MCS 本机局域网 IP**（必须填设备能访问的地址）和控制台 HTTP 端口；
3. 自动安装 `MCS` 服务、配置动态 RTP 防火墙、启动服务；
4. 完成后打开浏览器控制台。

静默安装（管理员）：

```powershell
MCS-Setup-0.1.0.exe /VERYSILENT /SERVERIP=192.168.68.106 /HTTPPORT=8080
```

Inno Setup 脚本：[mcs.iss](mcs.iss)；服务 helper：[install-service.ps1](install-service.ps1)；配置写入：[prepare-config.ps1](prepare-config.ps1)。

> 向导里的 IP 只写入**新配置**；升级时不会覆盖已有 `config.yaml`。静默安装默认 IP 是 `192.168.1.10`，请显式传 `/SERVERIP`。

### 命令行安装（不使用安装包）

以管理员 PowerShell 运行：

```powershell
powershell -ExecutionPolicy Bypass -File deploy\install-windows.ps1
```

安装内容：

- 程序：`C:\Program Files\MCS\mcs.exe`
- 配置：`C:\Program Files\MCS\config.yaml`
- 数据：`C:\ProgramData\MCS\data`
- 日志：`C:\ProgramData\MCS\mcs.log`
- 服务名：`MCS`，开机自启，失败自动重启

服务命令：

```powershell
Get-Service MCS
Restart-Service MCS
Stop-Service MCS
Start-Service MCS
```

卸载服务（保留配置/数据）：

```powershell
powershell -ExecutionPolicy Bypass -File deploy\uninstall-windows.ps1
```

防火墙脚本会放行 5060/5061/8080/8443，并给 `mcs.exe` 增加动态 RTP UDP 入站规则。生产建议用正式证书，并把控制台放在 HTTPS 后面。

> Windows 服务通过 `-service -config ... -log ...` 启动；日志写入 `C:\ProgramData\MCS\mcs.log`。

常用检查：

```powershell
sc.exe query MCS
Get-Content C:\ProgramData\MCS\mcs.log -Tail 50
```

注意：

- 安装包/EXE 未做代码签名，Windows SmartScreen 可能提示；生产建议签名。
- 本仓库在 macOS 上交叉编译 Windows 二进制，安装脚本已做静态检查，但**尚未在真实 Windows 主机上跑过服务安装**；首次部署请先在测试机验证。
- 服务默认 LocalSystem；生产建议改为专用低权限账户，并给 `C:\ProgramData\MCS` 授权。
- 安装脚本会给 `mcs.exe` 添加动态 RTP UDP 入站规则；如果企业策略禁止程序级规则，需要改为固定 RTP 端口段。

## SIP over TLS

生成自签证书（生产请用 CA 证书）：

```bash
sudo mkdir -p /etc/mcs/tls
sudo openssl req -x509 -newkey rsa:2048 -nodes -days 825 \
  -keyout /etc/mcs/tls/server.key -out /etc/mcs/tls/server.crt \
  -subj "/CN=mcs.local"
sudo chown -R mcs:mcs /etc/mcs/tls
```

配置：

```yaml
server:
  sip_tls: "0.0.0.0:5061"
  tls_cert: "/etc/mcs/tls/server.crt"
  tls_key: "/etc/mcs/tls/server.key"
```

重启后话机可用 TLS 传输注册；若只开 TLS，请把 `advertise` 改成对外 TLS 端口。

## HTTPS 控制台

```yaml
server:
  https: "0.0.0.0:8443"
  https_cert: "/etc/mcs/tls/console.crt"
  https_key: "/etc/mcs/tls/console.key"
```

证书为空时复用 `tls_cert`/`tls_key`。启用后 Cookie 自动带 `Secure`，浏览器访问 `https://<host>:8443`。

## SRTP

M1 已支持 **SDES-SRTP**：终端在 SDP 里用 `RTP/SAVP` + `a=crypto`（AES_CM_128_HMAC_SHA1_80/32），MCS 会自动逐腿解密/重加密音频和视频，无需额外开关。

注意：

- SDES 密钥随 SDP 传输，**生产必须同时启用 SIP over TLS**，否则密钥仍会明文暴露。
- SRTCP 已随安全腿转发；寻呼、广播、报警组呼仍是明文 RTP。
- DTLS-SRTP 只用于 WebRTC 坐席，SIP 终端间的 DTLS-SRTP 未实现。

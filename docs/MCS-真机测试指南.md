# MCS 真机测试指南

目标：用真实报警柱、门口机、SIP 音箱、SIP 话机完成一轮可复现的现场联调。
本文以“一台运行 MCS 的电脑 + 同一局域网内的终端”为例。

## 1. 测试拓扑

```text
            同一二层局域网（推荐）
   ┌──────────────┐        ┌──────────────────┐
   │  MCS 服务器   │────────│  SIP 话机/软电话  │
   │ 192.168.68.106 │        │ 1001 值班室     │
   └──────┬───────┘        └──────────────────┘
          │
   ┌──────┴───────┐  ┌──────────────┐  ┌──────────────┐
   │ 报警柱 1002   │  │ 门口机 1003   │  │ SIP 音箱 1004 │
   └──────────────┘  └──────────────┘  └──────────────┘
```

要求：

- MCS 主机使用**固定 LAN IP**（路由器 DHCP 保留），不要用 `127.0.0.1`；
- 终端和 MCS 在同一网段，或路由可达且没有 SIP ALG；
- 关闭路由器/防火墙的 **SIP ALG**（它会改写 SDP，导致单通/无视频）；
- 测试期间允许 MCS 主机的入站 UDP/TCP。

## 2. 一键启动 LAN 测试实例

仓库已提供脚本，自动检测 LAN IP、生成证书、绑定 `0.0.0.0`：

```bash
cd minisip_server
make run-lan
```

当前机器输出示例：

```text
LAN IP      : 192.168.68.106
SIP UDP     : 192.168.68.106:5060
HTTP console: http://192.168.68.106:18080/
HTTPS       : https://192.168.68.106:18443/
data dir    : ./data-lan
```

自定义 IP/端口：

```bash
LAN_IP=192.168.1.10 SIP_PORT=5060 HTTP_PORT=18080 HTTPS_PORT=18443 make run-lan
```

脚本会生成：

- `.run/config.lan.yaml`：`advertise` 已改成 LAN IP；
- `data-lan/`：独立数据库/录音/证书，避免和开发实例混用；
- `data-lan/dev-tls/`：包含 LAN IP 的自签证书。

> 这是测试实例，控制台登录默认关闭。现场交付前务必设置 `auth.console_password` 并换成正式证书。

## 3. 防火墙/端口

| 端口 | 协议 | 用途 |
| --- | --- | --- |
| 5060 | UDP | SIP（默认测试） |
| 5061 | TCP | SIP TLS（可选） |
| 18080 | TCP | 测试控制台 HTTP |
| 18443 | TCP | 测试控制台 HTTPS |
| 动态高位 | UDP | RTP/RTCP（当前未固定端口段） |

当前 RTP 使用 `net.ListenUDP(":0")` 动态端口。现场测试可临时放行 MCS 主机 UDP `1024-65535`；生产前应实现固定 RTP 端口段。

macOS 检查监听：

```bash
lsof -nP -iUDP:5060 -iTCP:18080 -iTCP:18443
```

Linux：

```bash
ss -lunp | grep 5060
ss -ltnp | grep -E '18080|18443'
```

## 4. 终端配置

### 4.1 SIP 话机/软电话

| 项 | 值 |
| --- | --- |
| SIP 服务器 | `192.168.68.106:5060` |
| 传输 | UDP（先测 UDP，再测 TLS） |
| 账号 | `1001` / `pass-1001` |
| 编码 | 只开 G.711U 或 G.711A |
| DTMF | RFC2833 或 SIP INFO |
| 视频 | 只开 H.264 |

### 4.2 报警柱/门口机

- 注册分机 `1002`/`1003`，密码与 `config.yaml`/设备表一致；
- 报警柱 `1002` 呼叫 `8000`；
- 门口机 `1003` 呼叫值班分机 `1001`；
- 视频编码 H.264；
- 开锁 DTMF 码默认 `#`（按设备型号可改）；
- 抓拍 URL 填到设备 `snapshot`，例如 `http://192.168.4.50/snapshot.jpg`。

### 4.3 SIP 音箱

- 注册 `1004`；
- 开启 **自动应答**，并支持 `Alert-Info`（默认值见配置）；
- 组播测试时加入 `239.255.10.1:5004`，交换机开启 IGMP snooping；
- 若音箱只支持单播，先把分区 `mode` 改成 `unicast`。

## 5. 建议测试顺序

### 5.1 注册与在线

1. 设备上电注册；
2. 控制台“设备管理”看到在线、Contact、来源 IP；
3. 重启设备，能重新注册；
4. 修改错误密码，应 401 且不上线；
5. 设备断电/拔网线，约一个注册周期后离线。

**通过标准**：在线状态、Contact、来源地址与设备实际一致。

### 5.2 点对点语音

1. 1001 呼 1002，接听；
2. 双方说话，确认双向有声；
3. 挂断；
4. 控制台“通话与对讲”看到话单；
5. 播放录音，左右声道分别对应主叫/被叫。

**常见失败**：单通/无声 → 优先检查 `advertise`、防火墙、SIP ALG、编码。

### 5.3 报警

1. 报警柱 1002 拨 `8000`；
2. 1001 振铃；15 秒不接，自动取消并呼 1003/备勤；
3. 接听后报警状态变为通话中；
4. 控制台“报警”弹条、声音、事件表更新；
5. 点“处置”关闭；
6. 重启 MCS，报警事件仍在（SQLite）。

### 5.4 广播

1. 控制台“广播”选分区，输入中文，点“TTS 广播”；
2. 自研音箱自动接听并出声；
3. 查看投递数/失败数；
4. 音频库上传 WAV → 选择 → “播放音频”；
5. 高优先级广播抢占低优先级，低优先级会话显示“被抢占”；
6. 测试组播分区，确认交换机 IGMP 和终端组播地址。

### 5.5 实时寻呼

1. 话机拨 `5001`（A 区 dial）；
2. 对麦克风讲话，分区音箱实时出声；
3. 挂机后广播停止；
4. 多终端时确认都在线。

### 5.6 视频对讲/开锁/抓拍

1. 门口机 1003 呼 1001，接听；
2. 控制台“通话与对讲”出现“视频”按钮；
3. 点视频，浏览器看到 H.264 画面并听到门口机声音；
4. 允许麦克风，对门口机说话；
5. 点“开锁”，门口机收到 DTMF 并动作；
6. 点“抓拍”，打开设备快照图片。

**注意**：WebRTC 视频窗需要 HTTPS 或 localhost；跨 NAT 需要先在系统设置配置 STUN/TURN，再测试。

### 5.7 TLS / SRTP

1. 在 MCS 配置 `sip_tls` + 证书；
2. 话机改用 TLS 5061 注册；
3. 打开 SRTP，确认 SDP 为 `RTP/SAVP` + `a=crypto`；
4. 双向通话有声；
5. 抓包确认媒体为 SRTP（不是明文 RTP）。

当前只支持 SDES-SRTP；不支持 DTLS-SRTP SIP 终端。寻呼/广播仍是明文 RTP。

### 5.8 MicroSIP / VPN fake-IP（198.18.x.x）

现象：

- 1001 呼 1002，网页能看到呼叫请求；
- 1002 不振铃，约 32 秒后 `b-leg wait answer failed / Timer_B timed out`；
- 1002 的 Contact 类似：

```text
"1002" <sip:1002@198.18.0.1:60171;ob>
source=192.168.68.47:60171
```

原因：Windows 上的 Clash/Surge 等 TUN/VPN 软件会给 MicroSIP 一个 **fake-IP**（198.18.0.0/15），
REGISTER 的 Contact 和 SDP `c=` 可能写成 `198.18.0.1` 或 `127.0.0.1`，MCS 直接往这个地址发 INVITE/RTP 会超时。

MCS 当前处理：

- Contact 是 loopback / 198.18/15 时，B 腿 INVITE 自动改用 REGISTER 的 **source IP:port**；
- SDP `c=` 是 loopback / 198.18/15 时，RTP 对端自动改用 source IP；
- 日志中可看到：

```text
b-leg invite ... uri=sip:1002@192.168.68.47:60171
b-leg media ... sdp_ip=127.0.0.1 peer_ip=192.168.68.47 port=4004
b-leg response ... code=180 Ringing
```

验证：

```bash
grep -E 'a-leg media|b-leg invite|b-leg media|b-leg response|rtp stats' .run/lan.log
curl -s http://192.168.68.106:18080/api/v1/cdr?limit=1
```

通话结束后 `rtp stats` 会打印每条腿的 RTP 收发计数：

```text
rtp stats ... a_rx=... a_tx=... b_rx=... b_tx=...
```

- `a_tx > 0` / `b_tx > 0`：MCS 确实把媒体发给了该腿；
- 如果目标腿 `tx>0`、设备仍无声，优先查设备扬声器/接收端口；
- 如果目标腿 `tx=0`，查 SDP 地址/端口或 MCS 转发。

如果一端完全没有声音，先看实际 SDP 编码。注意：MCS 是 B2BUA，正常情况下 B 的 answer 必须从 A 的 offer 里选，两端应有共同 codec，**不应依赖服务器转码**。`media.transcode` 默认 `false`，只有确实需要兼容时才开启；日志会打印：

```text
call codecs ... a=PCMA b=PCMU
```

然后查 Windows 麦克风：

- Windows 设置 → 隐私 → 麦克风 → 允许桌面应用；
- MicroSIP 设置 → 声音设备 → 选择正确的输入/输出；
- MicroSIP 界面上的静音按钮；
- 录音 WAV 左声道是主叫、右声道是被叫；若右声道 RMS 为 0，说明被叫没有发送麦克风音频，不是 MCS 转发问题。

### 5.9 稳定性

- 连续通话 30 分钟；
- 连续呼叫 100 次；
- 设备断电/恢复、网线拔插；
- MCS 重启后设备重新注册、话单/报警不丢；
- 广播过程中停止、抢占、TTS 超时。

## 6. 抓包与排查

安装 `sngrep`（Linux/macOS）查看 SIP 信令：

```bash
brew install sngrep
sudo sngrep -d en0 port 5060
```

抓 RTP：

```bash
sudo tcpdump -i en0 -n -s0 'udp port 5060 or udp portrange 10000-40000' -w mcs.pcap
```

检查 API：

```bash
curl -s http://192.168.68.106:18080/api/v1/devices
curl -s http://192.168.68.106:18080/api/v1/calls/active
curl -s http://192.168.68.106:18080/api/v1/cdr
curl -s http://192.168.68.106:18080/api/v1/alarms
```

### 常见问题

| 现象 | 优先检查 |
| --- | --- |
| 设备注册 401 | 分机/密码、Realm、Digest 算法 |
| 注册成功但呼叫无声 | `advertise` 是否 LAN IP；防火墙 RTP；SIP ALG |
| 1001 呼 1002 不振铃，但网页有呼叫 | 看 1002 Contact 是否 `198.18.x.x`（VPN/TUN fake-IP）；MCS 已自动回退到 REGISTER 源地址，确认 B 腿 `b-leg invite` 的 uri 是真实 IP |
| 能振铃/接通但单向音频 | 两边都 PCMU 时先看 `a_tx/b_tx` 是否大于 0；`a_tx>0` 但 1001 无声，重点查 1001 扬声器/接收端口。再查 SDP `c=` 是否 fake IP |
| 只有单向音频 | NAT/防火墙、SDP `c=`、终端编码 |
| 广播无声音 | 音箱是否自动应答、Alert-Info、分区成员、TTS 8399 |
| 视频黑屏 | H.264、`m=video`、WebRTC HTTPS、浏览器编码支持 |
| WebRTC 连不上 | STUN/TURN 配置、HTTPS/证书、ICE 端口、公网 IP |
| 开锁无反应 | DTMF 码、INFO Content-Type、设备型号 |
| 抓拍 404/502 | 设备 `snapshot` URL、设备网络可达 |
| 重启后设备消失 | 设备需重新注册；数据库只存配置，不存在线状态 |
| 配置改设备不生效 | 数据库已有设备表，后续以控制台/DB 为准 |

## 7. 现场验收记录模板

| 用例 | 设备/分机 | 结果 | 证据 |
| --- | --- | --- | --- |
| 注册/注销 | 1001 | ☐ 通过 | 设备页截图 |
| 双向语音 | 1001↔1002 | ☐ 通过 | 录音/抓包 |
| 报警升级 | 1002→8000 | ☐ 通过 | 报警事件/录音 |
| TTS 广播 | 分区 A | ☐ 通过 | 广播状态/现场视频 |
| 实时寻呼 | 5001 | ☐ 通过 | 现场视频 |
| 视频对讲 | 1003→1001 | ☐ 通过 | 浏览器截图 |
| 开锁/抓拍 | 1003 | ☐ 通过 | 门口机动作/图片 |
| TLS/SRTP | 1001 | ☐ 通过 | SDP/抓包 |
| 30 分钟通话 | 1001↔1002 | ☐ 通过 | CDR |
| 重启恢复 | MCS | ☐ 通过 | 话单/报警/重新注册 |

## 8. 测试实例的清理

```bash
kill $(lsof -t -iUDP:5060 | head -1)
rm -rf data-lan .run/config.lan.yaml
```

正式部署不要使用 `data-lan` 和自签证书。

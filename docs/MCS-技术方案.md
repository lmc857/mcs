# MCS 技术方案

| 项目 | 内容 |
| --- | --- |
| 文档版本 | v0.1（待评审） |
| 日期 | 2026-11-16 |
| 关联 | [MCS 产品方案](MCS-产品方案.md) · [MCS 接口与数据模型](MCS-接口与数据模型.md) |
| 技术栈 | Go 1.24+ · sipgo · Pion · SQLite · Vue 3 + Element Plus |
| 部署形态 | 单进程、单二进制、前端 `go:embed`；Windows / Linux / macOS / Docker |
| 容量目标 | 500 设备 · 250 并发通话 · 500 广播终端 · 16 路视频 |

> 说明：本文是早期设计与规划稿，包含尚未实现的能力，部分技术选型（`chi`、`zerolog`、Vue 3、
> 固定 RTP 端口池等）与当前代码不同。当前代码的实际架构以 [MCS 架构说明](MCS-架构说明.md) 为准；
> 两者冲突时，以架构说明和源码为准。

---

## 0. 设计原则

1. **Relay-first**：两端编码一致时只转发 RTP，不解码、不混音、不做 AEC；只有录音、放音、TTS、广播混音、WebRTC 才终止媒体。这样 CPU 低、音质无损、故障面小。
2. **单进程、单二进制**：不引入 MySQL/Redis/Nginx/RabbitMQ；SQLite 内嵌，前端打包进二进制，客户现场只看到一个可执行文件 + 数据目录。
3. **事件驱动**：注册、呼叫、报警、广播、设备状态统一走进程内事件总线；WebSocket、Webhook、MQTT 都是订阅者，业务模块之间不直接调用。
4. **设备模板化**：不同厂商的自动应答头、开锁指令、音量指令、配置格式全部沉到"型号模板"，新设备接入不改代码。
5. **可观测**：每通呼叫有信令 ladder，每个报警有完整时间线，每个广播有逐终端结果，支持一键诊断包。
6. **安全默认**：默认拒绝匿名呼叫、默认关闭开放中继、首次登录强制改密、失败自动封禁。

---

## 1. 总体架构

### 1.1 部署拓扑

```text
        报警柱/报警箱        门口机/对讲         SIP 音箱/广播网关        IP 话机
             │                  │                    │                   │
             └──────────────────┴───────── SIP ───────┴───────────────────┘
                                        │
                                  ┌─────▼─────┐
     浏览器（管理/调度台） ◀─HTTPS/WSS─▶│    MCS     │◀── SIP 中继 ── 运营商/上级平台
                                  │  单进程    │
     摄像头（ONVIF/RTSP）◀──HTTP/RTSP──▶│           │─── Webhook/MQTT ──▶ 报警平台
                                  └─────┬─────┘
                                        │
                              SQLite + 录音/提示音/证书/日志
```

- 单机部署，不依赖外部服务；
- 公网/云主机部署时配置 `media.external_ip`（1:1 NAT）；
- 摄像头只做预览/抓拍，不参与呼叫信令。

### 1.2 进程内架构

```text
┌ MCS 进程
├─ 接入层      SIP UDP/TCP/TLS/WS/WSS · HTTPS · WebSocket · ONVIF/RTSP 客户端
├─ 协议层      sipgo 事务/对话 · Digest · SDP · RTP/RTCP · SRTP/DTLS · ICE
├─ 呼叫层      B2BUA · fork/升级 · 录音 · 保持/转接 · DTMF · 媒体中继
├─ 业务层      报警引擎 · 对讲 · 广播引擎 · 预案 · 事件时间线
├─ 设备层      注册位置 · 型号模板 · 自动配置 · 在线检测 · 摄像头
├─ 数据层      SQLite（配置/话单/事件/索引） · 文件（录音/音频/证书） · 内存状态
└─ 运维层      日志 · 指标 · 抓包 · 诊断 · 许可 · 升级 · 备份
```

### 1.3 并发模型

| 组件 | 模型 |
| --- | --- |
| SIP 监听 | 每个监听器一个 goroutine；UDP 读循环 + 工作池；TCP/TLS/WS 每连接一个读 goroutine |
| 注册位置 | 按 AOR 哈希分片（32 片），`RWMutex` 保护；注册风暴时限流 |
| 呼叫 | **每通呼叫一个 actor**（goroutine + 有界 mailbox），状态只在该 goroutine 内修改，避免锁 |
| 媒体 | 每路 RTP 一个读 goroutine + 一个写 goroutine；写队列满时丢包，**不阻塞信令** |
| 广播 | 每个广播会话一个调度 goroutine；20ms tick 驱动；终端发送并发受限（默认 64 并发） |
| 定时器 | 统一时间轮/最小堆，呼叫做超时、升级链、广播任务共用 |
| 事件总线 | 进程内 pub/sub，订阅者带缓冲队列；关键事件（alarm.*）队列满则断开该订阅并触发全量快照，非关键事件可丢弃计数 |

### 1.4 技术选型

| 层 | 选型 | 说明 |
| --- | --- | --- |
| SIP | `github.com/emiago/sipgo` v1.x（锁定版本） | 事务/对话/UDP/TCP/TLS/WS；封装在 `internal/sipcore` |
| 媒体 | `pion/rtp`、`rtcp`、`srtp`、`dtls`、`ice`、`webrtc` | WebRTC 坐席、SRTP、视频 |
| 数据库 | `modernc.org/sqlite`（纯 Go） | WAL、单写者、迁移 |
| HTTP | `chi` | REST、静态资源、WSS |
| 前端 | Vue 3 + Element Plus + ECharts + Pinia | 管理后台 + 调度台 |
| 日志 | `zerolog` | JSON、call-id/alarm-id 关联 |
| 指标 | Prometheus client | `/metrics`，可选关闭 |
| 抓包 | `gopacket/pcapgo` | 生成 pcap |
| 构建 | goreleaser + Inno Setup + nfpm + pkgbuild | 三平台安装包 |

### 1.5 代码结构

```text
mcs/
├── cmd/mcs/                # server / reset-password / import / license / doctor
├── internal/
│   ├── transport/          # SIP 监听、TLS、WS、DNS
│   ├── sipcore/            # sipgo 封装、事务、对话、SDP
│   ├── registrar/          # 注册、位置、保活、NAT
│   ├── auth/               # Digest、ACL、封禁
│   ├── routing/            # 拨号规则、短号、权限、时段
│   ├── call/               # B2BUA、fork、升级、保持/转接
│   ├── media/              # RTP/SRTP、录音、放音、DTMF、混音、扇出
│   ├── alarm/              # 策略、值班、事件、升级链、通知
│   ├── intercom/           # 门口机、开锁、访客
│   ├── broadcast/          # 分区、会话、优先级、调度、TTS
│   ├── device/             # 模板、配置、状态、摄像头
│   ├── console/            # WS 事件、快照
│   ├── store/              # SQLite、迁移、CDR、保留策略
│   ├── api/                # REST、鉴权、RBAC、审计
│   ├── platform/           # 许可、升级、备份、诊断
│   └── events/             # 事件总线、事件类型
├── web/                    # Vue3 源码
├── deploy/                 # systemd/launchd/windows/docker/installer
├── docs/
└── test/                   # sipp、压测、集成
```

---

## 2. SIP 接入层

### 2.1 传输与监听

| 传输 | 默认端口 | 说明 |
| --- | --- | --- |
| UDP | 5060 | 主力；设备兼容性最好 |
| TCP | 5060 | 大消息、TLS 不可用时的可靠传输 |
| TLS | 5061 | 证书热加载；最低 TLS 1.2 |
| WS | 443 `/sip` | 与 Web 共用端口，前面可挂反向代理 |
| WSS | 443 `/sip` | WebRTC 坐席；必须 HTTPS |

- 1.0 以 IPv4 为主，代码预留双栈；IPv6 作为 2.0 增强；
- TCP/TLS 按 `Content-Length` 分帧，限制单条消息 64KB、每 IP 连接数；
- TLS 证书支持上传 PEM/PFX、自签生成、ACME（可选），SNI 多域名；
- DNS：出局解析 A/AAAA、SRV/NAPTR（RFC3263），支持自定义 hosts。

### 2.2 注册与位置服务

- AOR = 分机号/设备号；一个 AOR 支持多 Contact（一号多机）。
- 保存 `contact`、`expires`、`received`、`rport`、传输、User-Agent、最后注册时间。
- NAT 场景：
  - 响应按 `received`/`rport` 回送；
  - 后续请求发往实际源地址（不是 Contact 里的私网地址）；
  - 服务端每 30s 发 OPTIONS 保活，连续 2 次失败标记离线；
  - TCP/TLS 连接断开即离线。
- 注册限流：每 IP 10 次/秒，每账号 2 次/秒；异常触发封禁。
- 注册状态变化 → 事件总线 → WebSocket / 离线告警 / 通知。

### 2.3 鉴权

- Digest：MD5 与 SHA-256（RFC 8760），`qop=auth`，nonce 5 分钟轮换，`stale=true` 处理。
- 中继：账号密码 或 IP 白名单；两者可组合。
- 账号锁定：连续 5 次失败锁 10 分钟；全局失败率异常触发告警。
- 呼叫权限：分机/设备可配置允许呼叫的号码段（如只能呼值班室、不能呼外线）。
- 默认拒绝：未知来源的 INVITE 一律 403，不做开放中继。
- M1 已实现：控制台单管理员 Cookie 会话（bcrypt，密码先 SHA-256 预哈希）；设备 INVITE 鉴权、账号锁定、RBAC 仍未实现。

### 2.4 路由与拨号规则

| 号码段 | 用途 | 示例 |
| --- | --- | --- |
| `8xxx` | 报警短号/报警组 | 8000 一键报警 |
| `6xxx` | 对讲/门口机 | 6001 东门门口机 |
| `5xxx` | 广播分区 | 5001 教学楼 A 区 |
| `1xxx`~`4xxx` | 分机 | 1001 值班员 |
| `9` 前缀 | 外线 | 9013800138000 |

- 规则引擎：精确匹配 > 最长前缀 > 正则；支持优先级、时间段、来源区域、主叫改写。
- 广播短号与报警短号互斥，配置时校验。
- 路由结果：`fork 到值班组` / `转分机` / `转中继` / `广播区` / `拒绝`。

### 2.5 B2BUA 状态机

```text
Idle ──INVITE──▶ Calling ──180──▶ Ringing ──200──▶ Answered ──BYE──▶ Ended
                    │                 │                 │
                    │                 ├─ 183 ─▶ EarlyMedia
                    │                 │                 ├─ re-INVITE(hold) ─▶ Held
                    │                 │                 └─ REFER ─▶ Transferring
                    └─ 3xx/4xx/5xx/6xx/超时 ─▶ Failed ─▶ 下一分支/升级/结束
```

- A 腿（设备/主叫）与 B 腿（被叫）各自维护对话、CSeq、tag、路由集。
- fork：并行发多个 INVITE，第一个 2xx 获胜，其余 CANCEL；全失败按策略升级或返回 480/486。
- 早期媒体：183 + SDP 透传；报警场景可插入"已受理"提示音（服务端放音）。
- ACK：B 腿 2xx 的 ACK 由 B2BUA 生成；A 腿的 ACK 来自终端并终结事务；非 2xx 的 ACK 按 RFC3261 处理。
- 保持：re-INVITE `a=sendonly/recvonly`，服务端放保持音。
- 转接：REFER 盲转；咨询转先建立咨询呼叫再 REFER。
- 挂断：任一侧 BYE → 终止另一侧；异常断开启动兜底定时器。
- Glare：同 Call-ID 冲突按 RFC 处理，记录日志。
- 呼叫超时：振铃 60s（报警 20s 可配），通话最长 4h（可配）。

### 2.6 定时器与容错

| 定时器 | 用途 |
| --- | --- |
| RFC3261 A/B/D/E/F/G/H/I/J/K | 事务重传、超时 |
| 振铃超时 | 报警/对讲/普通呼叫分别配置 |
| 升级定时器 | 报警升级链每一级 |
| 会话定时器（RFC4028） | 可选，防僵尸会话 |
| 保活定时器 | 注册在线检测 |
| 媒体超时 | 30s 无 RTP 判定单向，记录并告警 |

### 2.7 事件识别头

报警/对讲可以通过以下任一方式识别，按优先级：

1. 请求 URI 的短号（8000/6001）；
2. `X-MCS-Event: alarm|intercom|page`（设备/网关可配）；
3. `Alert-Info`（如 `urn:alert:service:panic`）；
4. 设备型号模板中的匹配规则。

MCS 在 INVITE 上补充 `X-MCS-Device-ID`、`X-MCS-Zone`、`X-MCS-Site`，供下游和日志使用。

---

## 3. 媒体层

### 3.1 RTP 中继

- 端口池 `10000-20000`（可配），偶数端口，RTCP 默认 +1；支持 `rtcp-mux`。
- 收到首包后锁定对端地址（latching），后续只发该地址；防抖与防注入。
- SDP 重写：`c=` 改为服务器地址，`m=` 端口改为分配端口；支持 `a=rtcp`、`a=fmtp` 透传。
- 单向检测：30s 无包 → 事件 + 可选放音提示；不自动挂断（避免误判）。
- 统计：收发包数、丢包、抖动、RTT（RTCP RR）。

### 3.2 SRTP / DTLS-SRTP

- 中继模式支持 SDES-SRTP（`a=crypto`）与 DTLS-SRTP（WebRTC）。
- 策略：可选/强制/关闭，按设备型号和中继配置；密钥不落盘。
- 转码/录音在解密后进行；扇出时按终端能力重新加密。
- M1 已实现：SDES `AES_CM_128_HMAC_SHA1_80/32`，音频/视频逐腿解密再加密，SRTCP 随安全腿转发；不支持 DTLS-SRTP SIP 终端，寻呼/广播/组呼暂用明文 RTP。

### 3.3 编解码

| 编码 | 支持 | 说明 |
| --- | --- | --- |
| G.711 A/U | 必选 | 64kbps，透传优先 |
| G.722 | 可选 | 宽带，透传；录音重采样到 16k |
| Opus | 仅 WebRTC | 浏览器侧优先 PCMU 规避转码；必要时 cgo 仅在 x86_64 构建 |
| G.729 | 不支持 | 专利与复杂度 |
| H.264 | 视频 | 透传；WebRTC 观看 |

转码矩阵：G.711A↔G.711U（查表）、G.711↔G.722（重采样）；其余组合不转码，协商失败返回 488。

> 关于 B2BUA 的编码协商：MCS 是 B2BUA，A/B 两条腿各自与 MCS 协商；MCS 把 A 的 offer 编码列表传给 B，再把 B 的 answer 回给 A。正常情况下 B 只能从 A 的列表中选择，因此两端必有共同 codec，**不需要服务器转码**。G.711 转码只作为混合厂商/设备不守规范时的兼容层，并非 SIP 通用要求；纯 SIP 代理不转码，SBC/媒体网关/会议混音才常做。默认应优先做 codec 策略/白名单，无法协商时返回 488；`media.transcode` 默认 `false`，仅作为混合厂商设备的可选兼容层。

### 3.4 录音

- 触发：报警、对讲、按设备/分机策略、手动。
- 路径：RTP → 解码 PCM16 → 双声道 WAV（A 腿左、B 腿右）→ 分段（默认 5 分钟/段）→ 索引。
- 采样率：G.711 8kHz；G.722 16kHz。
- 证据链：每段算 SHA-256，事件表保存 `chain_hash = SHA256(prev_hash + segment_hash)`；提供校验接口。
- 存储：`data/recordings/{yyyy}/{mm}/{dd}/{call-id}-{n}.wav`；默认保留 90 天，按策略清理。
- 容量：单声道 28.8MB/小时，双声道 57.6MB/小时；建议只录报警/对讲，不对所有通话默认录音。

### 3.5 放音与提示音

- 提示音库：WAV/PCM，8k/16k；支持多语言。
- 场景：报警已受理、排队、无人接听、关机、空号、广播开始/结束。
- 播放与 RTP 发送共用 20ms 调度器；支持循环、淡入淡出。

### 3.6 DTMF

- RFC4733/2833 透传（优先）；SIP INFO；带内 Goertzel 检测（开锁码）。
- 开锁码可配置（如 `#`、`*123`），检测后触发开锁适配器。

### 3.7 混音

- 仅广播多音源、会议（2.0）、TTS+背景音时使用。
- 输入：PCM16 单声道，20ms 帧；每源独立抖动缓冲（60~120ms）。
- 混音：饱和加法，防溢出；可选软限幅。
- 输出：按终端编码一次，再扇出。
- 规模：单会话混音源 ≤ 8；会议方数 ≤ 16（2.0）。

### 3.8 视频

- 呼叫内：H.264 透传，不改 payload；SDP 保留 `profile-level-id`、`packetization-mode`。
- 实现：呼叫建立时若 offer 带 `m=video`，额外分配一对视频中继端口，改写 `m=video` 与 `c=`；H.264 RTP 原样转发，不做转码。
- 调度台：Pion PeerConnection，H.264 或 VP8；浏览器不支持时降级为仅音频 + 抓拍。
- 抓拍：优先设备 HTTP 快照 URL；其次 RTSP 取帧（可选模块）；失败则用最近一帧缓存。
- 录像：1.0 只录音频；视频录像 2.0（存储与合规成本高）。

### 3.9 广播扇出

```text
音源（RTP/文件/TTS）
   │ 解码（仅一次）
   ▼
PCM 20ms 帧 ──▶ 编码（按终端编码，最多 2 种）──▶ 打包（每终端独立 SSRC/seq/ts）
                                                   │
                                    ┌──────────────┼──────────────┐
                                    ▼              ▼              ▼
                                 终端1          终端2    ...    终端N
```

- 单播：每终端一路 RTP；500 终端 = 25k pps，memcpy 级开销。
- 组播：分区配置组播地址，服务器只发一份；终端需支持并已配置加入组。
- 失败重试：INVITE 超时重试 1 次；仍失败标记该终端失败，不阻塞整个任务。
- 启动节奏：默认 50 终端/秒，避免注册/信令风暴；500 终端约 10s 全部接入（可配 20~100/s，按设备能力调整）。

### 3.10 质量与调优

- DSCP：EF（46）音频、AF41（34）视频；可配。
- 抖动缓冲仅用于终止媒体场景；中继不缓存。
- 监控：每流丢包/抖动；连续 5s 丢包 > 5% 告警。
- 网卡：UDP 读缓冲 4MB；SO_REUSEPORT（Linux）多队列；Windows 调大 socket buffer。

---

## 4. 报警引擎

### 4.1 事件模型

```json
{
  "id": "alm_01HQ...",
  "site_id": "site-1",
  "device_id": "dev-10086",
  "device_name": "3 号楼东侧报警柱",
  "zone_id": "zone-a",
  "type": "panic",
  "priority": 0,
  "source": "sip",
  "call_id": "abc@mcs",
  "occurred_at": "2026-11-16T10:00:00+08:00",
  "location": {"lat": 31.23, "lng": 121.47, "floor": "1F", "desc": "东门"},
  "media": {"recording_id": null, "snapshot": "/snap/xxx.jpg"},
  "state": "created",
  "timeline": []
}
```

### 4.2 策略解析

匹配顺序：设备 → 设备组 → 区域 → 站点默认。命中即停，支持时间段覆盖（白天/夜间/节假日）。

策略字段：值班组、振铃方式、超时、升级链、是否录音、是否弹屏、通知渠道、优先级、是否自动广播。

### 4.3 值班与排班

- 值班组：成员（分机/WebRTC 坐席/外部号码）、主备、振铃顺序。
- 排班：按周/日期/节假日，支持多班次重叠（交接班）。
- 临时替班：指定时间段替换成员。
- 无人在线：直接进入升级链，并记录"值班组离线"。

### 4.4 振铃与 fork

- 并行：同时呼叫所有在线成员；首个应答获胜。
- 顺序：按顺序呼叫，单个超时（默认 12s）转下一个。
- 渐进：先呼主值班，超时后呼备值班 + 组长。
- 坐席忙：跳过或按优先级抢占（1.0 仅广播可抢占；通话强插 2.0）。

### 4.5 升级链状态机

```text
CREATED ─▶ DISPATCHING ─▶ RINGING ─┬─▶ ANSWERED ─▶ ON_CALL ─▶ ENDED ─▶ PENDING_DISPOSITION ─▶ CLOSED
                                   ├─▶ REJECTED ─▶ 升级/下一级
                                   ├─▶ TIMEOUT(L1) ─▶ 升级 L2（班长/短信/APP）
                                   ├─▶ TIMEOUT(L2) ─▶ 升级 L3（总值班/外部电话）
                                   └─▶ ALL_FAILED ─▶ 事件保持"未接"，持续告警
```

- 每一级记录：开始时间、目标、结果、耗时。
- 升级动作：fork 下一组、发短信/APP/Webhook、播放语音通知、转外部号码（可选）。
- 设备侧在振铃期间听提示音；升级时提示音不中断，避免用户以为断线。
- 未接事件不能自动关闭，必须人工处置或按策略超时归档。

### 4.6 弹屏与实时事件

- 报警创建后 50ms 内推 `alarm.created`（端到端 ≤500ms，含设备与网络）；1s 内未 ACK 则重推。
- 弹屏内容：设备、区域、地图坐标、摄像头（快照/流）、历史 3 条、联系人、预案按钮。
- WebSocket 断线重连后拉取"活动报警"快照，保证不漏。
- 多调度台同时弹屏，接听状态实时同步（`alarm.updated`）。

### 4.7 处置与关闭

- 处置结果：已出警、已联系、误报、测试、转其他部门；可填备注、上传附件。
- 关闭权限：调度员/班长；误报需二次确认。
- 关闭后事件不可改，只能追加备注（审计）。

### 4.8 通知适配

| 渠道 | 协议 | 说明 |
| --- | --- | --- |
| Webhook | HTTP POST + HMAC-SHA256 | 主渠道，对接 alarmserver-go |
| MQTT | QoS1 | 弱网/多订阅方 |
| 短信 | 阿里云/腾讯云 API | 可插拔 |
| 邮件 | SMTP | 报表/低优先级 |
| APP 推送 | 厂商通道 | 2.0 |

- 失败重试：1s/5s/30s/5min/30min，5 次后进死信并告警。
- 幂等：事件 ID + 订阅 ID 去重；接收方按 `event_id` 幂等。

### 4.9 证据链

- 报警事件 ↔ 录音段 ↔ 抓拍 ↔ 处置记录，全部挂在 `alarm_id` 下。
- 录音段哈希链；导出证据包时附带 `manifest.json`（文件、哈希、时间、操作人）。
- 校验接口：`GET /alarms/{id}/evidence/verify` 返回逐段校验结果。

### 4.10 演练/测试

- 测试模式：不触发外部通知、不计入统计、弹屏标记"演练"。
- 演练报告：接听时长、升级次数、处置结果。

### 4.11 与报警平台对接

- MCS → 平台：`alarm.created/updated/closed`、`call.completed`、`device.offline`。
- 平台 → MCS：`alarm.ack`（外部已处理）、`alarm.close`、`broadcast.trigger`（可选）。
- 鉴权：HMAC 签名 + 时间戳 + nonce；可选 mTLS。
- 字段映射可配置（JSONPath），避免平台字段差异导致改代码。

### 4.12 异常场景

| 场景 | 处理 |
| --- | --- |
| 设备报警后掉线 | 事件保留，录音继续到超时；标记"设备异常断开" |
| 重复报警（30s 内同设备） | 合并到同一事件，次数 +1，不重复弹屏 |
| 调度台崩溃 | 事件保持 RINGING，重连后恢复；超时继续升级 |
| 服务重启 | 活动事件落盘，启动后恢复"待处置"；通话不恢复（明确提示） |
| 全值班组离线 | 直接 L2/L3，记录原因 |

---

## 5. 对讲

### 5.1 路由

- 门口机短号 `6xxx` → 按区域/时段路由到值班组；夜间转总值班/手机。
- 主备值班室：主值班 15s 未接转备值班。
- 多门口机共享一个组：按设备 ID 区分弹屏。

### 5.2 视频协商

- 门口机 SDP 带 H.264（`m=video`），MCS 透传；调度台通过 WebRTC 另建一路视频（Pion 转封装，不转码）。
- 若设备只支持私有视频：1.0 降级为音频 + HTTP 快照；2.0 接 RTSP/转码。
- 带宽：720p H.264 约 1~2Mbps/路；16 路约 16~32Mbps，需在容量规划内。

### 5.3 开锁

| 方式 | 适用 | 说明 |
| --- | --- | --- |
| DTMF INFO | 多数门口机 | 通话中发送 `application/dtmf-relay`，失败回退 `application/dtmf`（1.0 已实现） |
| SIP INFO | 部分厂商 | `application/dtmf-relay` 或私有 body |
| HTTP | 支持 API 的设备 | 服务器直接调用设备接口 |

- 模板配置：指令、超时、重试、成功判定（响应/事件）。
- 安全：只有通话中的值班员可开锁；记录操作人、设备、时间、结果；失败告警。
- 防误开：开锁需二次确认（可配）；同一设备 10s 内只能开一次。

### 5.4 抓拍与访客

- 振铃即抓拍（HTTP 快照优先），接听后每秒 1 帧可选。
- 访客记录：时间、门口机、值班员、开锁结果、抓拍、录音。
- 保留期跟随事件策略。

### 5.5 半双工/回声

- 中继模式下回声由终端处理；半双工设备按模板启用"按键讲话"提示。
- 服务端不做 AEC（1.0）；如客户投诉回声，优先换设备/调音量/加终端 AEC。

### 5.6 转手机

- 无人接听 → 转外部号码（SIP 中继）或值班员手机。
- 转接时保持事件关联；外部号码接听也录音（如中继支持）。

---

## 6. 广播引擎

### 6.1 领域模型

- **Zone（分区）**：树形结构，终端可属多个区；分区有默认音量、组播地址、优先级上限。
- **Endpoint（广播终端）**：SIP 账号、型号、能力（编码/组播/自动应答）、在线状态。
- **Source（音源）**：实时 RTP（话机/WebRTC/话筒）、文件、TTS、混音。
- **Session（广播会话）**：一次播放，绑定 zone + source + priority + operator；含逐终端状态。

### 6.2 优先级与抢占

| 级别 | 名称 | 示例 | 行为 |
| --- | --- | --- | --- |
| P0 | 紧急 | 消防/一键紧急 | 抢占一切，禁止被抢占 |
| P1 | 报警 | 报警联动广播 | 抢占 P2~P4 |
| P2 | 实时 | 寻呼/通知 | 抢占 P3/P4 |
| P3 | 定时 | 打铃/课间操 | 抢占 P4 |
| P4 | 背景音乐 | BGM | 最低 |

- 同区只允许一个活动会话；高优先级到达 → 停止低优先级 → 记录被抢占任务与进度 → 高优先级结束后按策略恢复。
- 不同区可并行；并发任务上限 4（MCS-500），但所有会话的广播终端总数不超过授权总量（500）。
- 抢占不跨区，除非任务显式指定"全呼"。

### 6.3 会话生命周期

```text
CREATED → PREPARING（校验权限/终端/音源）→ STARTING（批量 INVITE）
   → RUNNING（RTP 扇出）→ STOPPING（BYE）→ FINISHED
                         └→ PARTIAL_FAILED（部分终端失败）
```

- 启动前预检：终端在线率、编码、组播能力；低于阈值（如 80%）提示确认。
- 运行中：每 5s 统计成功/失败/掉线终端；UI 实时进度。
- 结束：BYE 所有终端；释放端口；写日志。

### 6.4 终端接入

- INVITE 带自动应答头（模板可配）：`Alert-Info: <http://...>;info=alert-autoanswer` 或厂商私有头。
- SDP：G.711A/U 优先；组播模式下 SDP `c=` 为组播地址。
- 批量：令牌桶 50/s；单终端超时 5s，重试 1 次。
- 失败原因归类：离线、忙、拒绝、超时、SDP 失败、无 RTP。

### 6.5 音源

| 音源 | 处理 |
| --- | --- |
| 实时 RTP | 话机/WebRTC 上行；解码为 PCM；可选混音 |
| 文件 | WAV/MP3 → PCM → 循环/单次；支持淡入淡出 |
| TTS | 文本 → PCM（缓存）→ 播放 |
| 混音 | 多源叠加（BGM + TTS 压低 BGM，ducking 可选） |

> 实时寻呼：话机拨分区 `dial` 短号 → MCS 作为 UAS 接听，按主叫 IP 锁存 RTP；PCMA 转 PCMU 后切成 20ms 帧，实时扇出到该分区终端；挂机即停止。WebRTC 麦克风寻呼为 2.0。

### 6.6 调度器

- 任务类型：立即、周计划（cron）、节假日、拨号触发、一键触发、报警联动。
- 时间精度：±1s；任务开始前 5s 预热（校验终端/音源）。
- 冲突：同区按优先级；同优先级按创建时间；低优先级可"顺延"或"丢弃"（策略）。
- 节假日：独立日历；支持调休。
- 失败：记录逐终端结果；整体成功率 < 80% 告警。

### 6.7 TTS

- 适配器接口：`Synthesize(ctx, text, voice, format) ([]byte, error)`。
- 当前实现：本机 **Kokoro-82M**（Apache-2.0），OpenAI 兼容 `POST /v1/audio/speech`；`voice_zh=zf_001`、`voice_en=af_heart`，按文本是否含 CJK 自动选择。
- 输出 24kHz 单声道 WAV → 重采样 8kHz → G.711；结果按 `sha256(voice+text)` 缓存在 `data/audio/tts/`。
- 备选：阿里云/腾讯云/Azure；**1.0 不做离线引擎**（客户无外网时用预生成音频兜底）。
- 缓存：`sha256(text+voice+format)` → 文件；重复文本不重复合成。
- 文本预处理：数字/时间/电话号码读法；长度限制 500 字；敏感词过滤（可配）。
- 失败降级：预置提示音或跳过并告警。

### 6.8 音频库

- 支持上传 PCM16 WAV 或 MP3（≤ 20MB），解码后存原文件与索引；广播时按分区编码为 PCMU 或 Opus。
- 分类：告警音、提示音、音乐、TTS 缓存。
- 试听、重命名、删除（被任务引用时禁止删除）。
- 存储：`data/audio/`；数据库存元数据与哈希。

### 6.9 组播/单播

| 模式 | 适用 | 说明 |
| --- | --- | --- |
| 单播 | 小区（≤ 100 终端）或跨网段 | 兼容性最好，防火墙友好 |
| 组播 | **大区默认**（>100 终端）、同网段 | 服务器发一份；自研终端已确认支持，需统一 IGMP 行为 |

- 组播地址段：`239.255.x.x`，每区一个；TTL 可配（默认 1）。
- 混合：同一任务可对支持组播的终端走组播，其余单播。

### 6.10 音量与控制

- 指令模板：DTMF 序列、SIP INFO body、HTTP API。
- 批量调音量：按区/按型号；失败逐台记录。
- 静音/恢复：设备能力差异大，1.0 只做"音量档位"，不做精细混音。

### 6.11 故障与恢复

| 场景 | 处理 |
| --- | --- |
| 终端离线 | 跳过，记录；任务继续 |
| 终端中途掉线 | 标记失败；可选重连 1 次 |
| 音源中断 | 播放提示音/静音；30s 无数据结束任务 |
| 服务重启 | 活动任务不恢复（明确提示）；定时任务按计划继续 |
| 被高优先级抢占 | 记录进度；结束后按策略恢复（从头/继续） |

---

## 7. 调度台与 WebRTC

### 7.1 架构

```text
浏览器（Vue3）
 ├─ HTTPS：管理/调度 UI
 ├─ WSS /api/v1/ws：事件（报警/通话/设备/广播）
 └─ WSS /sip：SIP over WebSocket
        │
        ▼
MCS：sipgo WS 传输 + Pion PeerConnection
 ├─ 音频：PCMU/PCMA（避免 Opus 转码）
 ├─ 视频：H.264（门口机/摄像头）
 └─ ICE：host + srflx；复杂网络走 TURN（可选）
```

### 7.2 ICE/TURN

- 默认 host + STUN（可配公共 STUN）；企业内网通常直连。
- 跨 NAT/云部署时部署 TURN（coturn 或 Pion TURN），配置 `turn.urls/username/credential`。
- 只走 UDP；ICE 失败提示"请使用 SIP 话机"。

### 7.3 媒体参数

> 1.0 已实现“浏览器观看视频”：活动呼叫页点“视频” → 浏览器发 recvonly H.264 offer → MCS 用 Pion 建 PeerConnection 并 answer → 从该呼叫 A→B 音视频 RTP 中继抓包，分别用 PCMU/PCMA 与 H.264 `TrackLocalStaticRTP` 转发到浏览器；浏览器音频通过 `OnTrack` 反向注入到门口机所在腿。视频回传、TURN 为 2.0。

- 音频：PCMU 优先，PCMA 备选；20ms；`useinbandfec` 不适用 G.711。
- 视频：H.264 baseline/main；分辨率跟随源，不转码。
- DTMF：`telephone-event`。
- 抖动缓冲：浏览器侧；服务端只转发。

### 7.4 事件协议

- 连接：`wss://host/api/v1/ws?token=<jwt>`；心跳 25s。
- 信封：`{"id":"evt_...","type":"alarm.created","ts":"...","data":{...}}`。
- 订阅：登录角色决定可见事件；调度台默认订阅 `alarm.*`、`call.*`、`device.*`、`broadcast.*`。
- 重连：带 `last_event_id`，服务端补发最近 5 分钟事件；超出则全量快照。

### 7.5 断线重连

- WebRTC 断线：自动重连 3 次；失败提示"电话线路不可用"。
- WS 断线：指数退避（1s/2s/5s/10s），恢复后拉快照。
- 页面刷新：恢复登录态与当前视图。

### 7.6 浏览器兼容

- Chrome/Edge 最近两个大版本（首选）。
- Safari 17+：WebRTC 可用，但 G.711/H.264 兼容需实测；不支持时降级为"仅监听 + 用话机通话"。
- 必须 HTTPS（localhost 除外），否则麦克风/摄像头不可用。

### 7.7 安全

- WSS + JWT；Origin 校验；每用户并发 WS ≤ 3。
- 麦克风/摄像头权限由浏览器管理；服务端不录制未经授权的媒体。
- 调度台操作全部审计（接听/广播/开锁）。

---

## 8. 设备接入与配置

### 8.1 型号模板

```yaml
model: "ACME-DoorStation-200"
vendor: "ACME"
config_format: "xml"          # xml | ini | json | url
auto_answer_header: "Alert-Info: <http://127.0.0.1>;info=alert-autoanswer"
codecs: ["PCMA", "PCMU"]
unlock:
  method: "dtmf"              # dtmf | info | http
  code: "#"
  timeout_ms: 3000
volume:
  method: "dtmf"
  up: "*90"
  down: "*91"
snapshot:
  method: "http"
  path: "/snapshot.jpg"
```

### 8.2 配置 URL

- 设备请求：`GET /provision/{mac}.cfg`，Basic 认证（账号=MAC，密码=设备密钥）。
- 模板渲染：`text/template`，变量含 `ext`、`password`、`server`、`port`、`transport`、`zone`、`auto_answer`、`codec`、`volume`、`multicast`。
- 鉴权：设备密钥首次生成后展示一次；支持重置。
- 限流：每 IP 5 次/分钟；异常记录。
- 兼容：DHCP Option 66 / 手动填 URL。

### 8.3 与 `sip_web` 对接

两种模式二选一（按客户现场）：

1. **MCS 主导**：设备向 MCS 拉配置，`sip_web` 只做现场调试；
2. **`sip_web` 主导**：MCS 调 `sip_web` API 写入 SIP 参数（需 `sip_web` 提供接口与鉴权）。

契约（待确认）：`POST {sip_web}/api/device/sip`，字段 `mac`、`server`、`port`、`transport`、`username`、`password`、`display_name`；返回成功/失败。失败重试 + 人工兜底。

### 8.4 摄像头

- ONVIF：WS-Discovery 发现 + GetProfiles/GetStreamUri；凭据加密存储。
- 手动：RTSP URL + 用户名密码。
- 绑定：设备/区域/地图点位；预览用 WebRTC/RTSP 旁路，不占 SIP 媒体。

### 8.5 在线检测

- SIP 注册过期 + OPTIONS 保活；TCP/TLS 连接状态。
- 离线判定：注册过期且 OPTIONS 连续 2 次失败（约 60s）。
- 离线事件：设备名、区域、最后在线、影响（广播/报警路由）。
- 恢复：自动清除；记录时长。

### 8.6 批量导入

- CSV 列：`mac,device_no,name,model,zone,ext,note`。
- 校验：MAC 唯一、设备号唯一、分机号冲突、型号存在。
- 结果：成功/失败逐行报告；失败可下载修正后重传。

### 8.7 自研设备协议约定（MCS Device Profile v1）

设备全部自研，可以把行为固化成协议，避免"每款设备一套适配"。

| 能力 | 约定 | 说明 |
| --- | --- | --- |
| 注册 | Digest（MD5/SHA-256）；`Expires: 3600`，刷新 1800；30s OPTIONS 保活 | `User-Agent` 带型号/固件；`X-MCS-Model`、`X-MCS-Firmware`、`X-MCS-Proto: 1` |
| 报警 | `INVITE 8000` + `X-MCS-Event: alarm` + `X-MCS-Alarm-Type` + `X-MCS-Device-ID` | 5s 内重复按键合并；收到 200 后建媒体；支持服务端放提示音 |
| 自动应答 | 广播 INVITE 带 `Alert-Info: <http://127.0.0.1>;info=alert-autoanswer` | 直接 200 + SDP，不振铃；失败返回 486/488 |
| 组播 | 配置组播地址/端口；收到 INVITE 后 IGMP join；BYE 后 leave | IGMP v2/v3；TTL 默认 1；同区多任务时按会话切换 |
| 开锁 | 默认 DTMF `#`；或 INFO `application/mcs-unlock` | 执行后回 INFO 回执（成功/失败/原因）；10s 防重放 |
| 快照 | HTTP `GET /snapshot.jpg`（Digest/Basic） | JPEG ≤ 200KB；失败返回 5xx |
| 音量 | DTMF `*90`/`*91`；或 INFO `application/mcs-volume` | 档位 0~9；广播中可调 |
| 视频 | H.264 baseline，PT 96~127，`packetization-mode=1`；720p@25 上限 | 不转码；SDP 带 `a=fmtp`；关键帧间隔 ≤ 2s |
| 配置 | 配置 URL 拉取；ETag/版本号；参数变更后重启生效 | 与 `sip_web` 二选一；失败重试 3 次 |
| 升级 | 2.0：HTTPS 下载 + 签名校验 + 双分区回滚 | 1.0 可先手工升级 |

**设备侧验收清单（出厂自检）**：

1. 注册/鉴权/保活/断网重连；
2. 报警 INVITE 头完整、5s 去重；
3. 广播自动应答 ≤ 300ms，组播加入/退出正确；
4. 开锁指令执行并回执；
5. 快照接口可用；
6. 音量指令生效；
7. H.264 码流可被 WebRTC 观看；
8. 固件版本在注册头上报。

> 设备协议版本随 MCS 发布；固件与 MCS 建立兼容矩阵（最低固件版本 + 已验证版本）。

---

## 9. 数据与存储

### 9.1 SQLite

- WAL 模式；`synchronous=NORMAL`；单写者（写队列串行化）。
- 迁移：版本号 + 幂等 SQL；升级前自动备份。
- 表规模：CDR 100 万行 ≈ 200MB；按年归档；查询走索引（时间、主叫、设备）。
- 不把大文件写库：录音/音频/证书存文件，库存路径与哈希。
- M1 已实现：`data/mcs.db` WAL，`devices` / `cdr` / `alarms` 三表；启动加载设备与最近记录，写入走单连接串行化。`config.yaml` 仅在设备表为空时首次导入；媒体索引/定时任务暂用 JSON。

### 9.2 数据目录

```text
data/
├── mcs.db
├── recordings/2026/11/16/...
├── audio/            # 提示音/音乐/TTS 缓存
├── snapshots/
├── certs/
├── logs/
├── backups/
└── license/
```

### 9.3 保留与清理

- CDR：默认 365 天；按年归档为 CSV 后可删。
- 录音：默认 90 天；按事件/设备策略覆盖。
- 事件：365 天；审计：180 天；日志：30 天（可配）。
- 清理任务：每日 03:00；分批删除；磁盘低于 10% 时提前清理并告警。

### 9.4 备份/恢复

- 备份：SQLite 在线备份 API + 文件清单；压缩 zip；可选加密。
- 定时：每日；保留 7 份；支持手动导出到 U 盘/共享目录。
- 恢复：校验版本与许可；停服 → 替换 → 迁移 → 启动 → 健康检查。

---

## 10. 部署与运维

### 10.1 平台矩阵

| 平台 | 优先级 | 形态 | 注意 |
| --- | --- | --- | --- |
| Linux x86_64/arm64 | **P0（1.0 首发）** | deb/rpm/tar + systemd | CAP_NET_BIND_SERVICE、ulimit、SELinux |
| Windows 10/11/Server 2016+ | **P0（首发后 4 周）** | exe + 服务 + 托盘 + 安装包 | 防火墙规则、Defender 排除、UDP 缓冲 |
| Docker | P1 | 单镜像 + compose | host 网络仅 Linux；否则固定端口段 + EXTERNAL_IP |
| macOS 12+ | P1 | pkg + LaunchDaemon | 公证、本地网络权限、开发/演示为主 |
| 树莓派/ARM | P2 | arm64/armv7 tar | 低内存档、录音写外置盘 |

### 10.2 端口

| 端口 | 协议 | 用途 |
| --- | --- | --- |
| 5060 | UDP/TCP | SIP |
| 5061 | TCP/TLS | SIP over TLS |
| 443 | TCP | HTTPS + WSS（SIP/事件） |
| 10000-20000 | UDP | RTP/RTCP |
| 3478 | UDP/TCP | TURN（可选） |
| 9100 | TCP | Prometheus（可选，默认仅本机） |

### 10.3 Windows

- 服务：`x/sys/windows/svc`，服务名 `MCS`；托盘程序做首配与状态。
- 安装：Inno Setup；写防火墙入站规则（SIP/RTP/Web）；创建数据目录；注册服务；启动。
- 卸载：保留数据目录（可选删除），先停服务。
- 日志：`%ProgramData%\MCS\logs`；崩溃转储可选。

### 10.4 Linux

- systemd：`User=mcs`、`AmbientCapabilities=CAP_NET_BIND_SERVICE`、`LimitNOFILE=65535`、`Restart=on-failure`。
- 包：nfpm 生成 deb/rpm；postinst 创建用户/目录/服务。
- 防火墙：文档给出 `firewalld/ufw` 规则；安装脚本可选自动配置。

### 10.5 macOS

- pkg 安装到 `/usr/local/mcs`；LaunchDaemon `com.mcs.server`。
- 签名 + 公证；首次启动本地网络权限提示。
- 不推荐生产大规模；用于演示/分支/小规模。

### 10.6 Docker

```yaml
services:
  mcs:
    image: mcs:1.0
    network_mode: host          # Linux 推荐
    volumes: ["./data:/data"]
    environment:
      MCS_EXTERNAL_IP: "203.0.113.10"
```

- 非 host 模式：映射 5060/udp、5060/tcp、5061/tcp、443/tcp、10000-10100/udp（按容量），配置 `media.external_ip`。
- macOS/Windows Docker Desktop 不支持 host 网络：明确文档，不建议生产。

### 10.7 配置示例

```yaml
server:
  http: ":443"
  sip_udp: ":5060"
  sip_tcp: ":5060"
  sip_tls: ":5061"
  ws_path: "/sip"
media:
  rtp_min: 10000
  rtp_max: 20000
  external_ip: ""
  codecs: ["PCMA", "PCMU", "G722"]
  srtp: "optional"
storage:
  data_dir: "./data"
  recording_retention_days: 90
license:
  file: "./data/license/license.json"
log:
  level: "info"
  format: "json"
```

### 10.8 升级与回滚

- 包签名（Ed25519）；升级前校验版本与依赖。
- 流程：备份 → 停服 → 替换二进制 → 迁移 → 启动 → `/healthz` → 失败自动回滚。
- 灰度：先小客户；保留上一版本二进制。

### 10.9 诊断

- 一键诊断包：系统信息、配置（脱敏）、日志、最近 20 通呼叫 pcap、指标快照、数据库统计。
- 脱敏：密码、密钥、license、token、手机号中间四位。
- 大小限制 200MB；生成进度可见。

### 10.10 可观测

- 日志：JSON，字段 `ts/level/module/call_id/alarm_id/device_id/msg`。
- 指标：注册数、在线率、并发通话、广播任务、RTP 丢包、CPU/内存/磁盘、SQLite 写延迟。
- 呼叫跟踪：按 call-id 查完整信令与媒体统计；可导出 pcap。
- 告警：磁盘、离线率、注册失败率、中继不可用、证书到期。

---

## 11. 安全设计

### 11.1 威胁模型

| 威胁 | 场景 | 对策 |
| --- | --- | --- |
| SIP 扫描/暴力破解 | 公网 5060 暴露 | 失败封禁、限速、TLS、强密码、非标端口可选 |
| 盗打（toll fraud） | 账号泄露后呼外线 | 目的前缀白名单、并发上限、异常呼叫告警、默认禁国际 |
| 窃听 | 内网抓包 | SRTP/DTLS、TLS |
| Web 攻击 | XSS/CSRF/越权 | CSP、CSRF token、RBAC、输入校验 |
| 设备仿冒 | 伪造注册 | 每设备唯一凭据、MAC 绑定（可选）、IP ACL |
| 许可破解 | 改时间/改文件 | 签名、机器指纹、回拨检测 |
| 拒绝服务 | 注册/呼叫洪泛 | 限流、连接数限制、队列丢弃策略 |

### 11.2 SIP 安全

- 默认拒绝匿名/未知来源；中继必须显式配置。
- Digest + 强密码；设备密钥随机 16 位。
- 每账号并发呼叫上限（默认 4）；每 IP 注册/呼叫速率限制。
- 外呼权限：按分机/设备/中继配置号码段；默认只允许内网短号。
- 异常检测：短时间内大量外呼、深夜外呼、超长通话 → 告警/阻断。
- 日志保留呼叫主叫/被叫/结果，便于追溯。

### 11.3 Web 安全

- HTTPS 强制（HSTS）；TLS 1.2+。
- 密码 Argon2id；登录失败 5 次锁 10 分钟；可选 TOTP。
- JWT 短时效（15min）+ Refresh（7 天）；注销即失效。
- RBAC + 数据范围（区域）；所有写操作审计。
- CSP、X-Frame-Options、CSRF token、文件上传类型/大小校验。

### 11.4 媒体与许可

- SRTP 可选强制；密钥只在内存。
- 许可文件签名校验；机器指纹变化需重新激活；宽限期 7 天。
- 升级包签名；未签名包拒绝。

### 11.5 合规

- 审计日志 ≥ 180 天；话单 ≥ 1 年；可导出。
- 录音证据链可校验；导出带清单。
- 不采集与业务无关的个人信息；访客抓拍保留期可配。

---

## 12. 性能与容量

### 12.1 测算（MCS-500 满配）

| 项目 | 计算 | 结果 |
| --- | --- | --- |
| 通话 RTP | 250 路 × 2 方向 × 50 pps | 25k pps |
| 广播 RTP | 500 终端 × 50 pps | 25k pps |
| 视频 | 16 路 × 1.5 Mbps | 约 24 Mbps |
| 合计峰值 | 50k pps + 视频 | 约 100 Mbps（含头开销） |
| CPU | 中继为主，录音/广播扇出为辅 | 4 vCPU 起步，8 vCPU 推荐 |
| 内存 | 呼叫状态 + 缓冲 + 缓存 | 1~2 GB |
| 录音磁盘 | 100 路并发 × 8h × 57.6MB/h | ≈ 46 GB/天（按需录音可大幅降低） |
| 数据库 | CDR 100 万行 | ≈ 200 MB |

### 12.2 调优

- UDP socket buffer 4MB；Linux `rmem_max/wmem_max`。
- RTP 写队列 32 包；满则丢最旧，绝不阻塞。
- SQLite 批量提交（CDR 100 条/批或 1s）；WAL checkpoint 定时。
- 广播扇出使用 `sendmmsg`（Linux）或批量写；Windows 单包发送。
- GC：`GOGC=100` 起步，大内存机器可调；避免每包分配（缓冲池）。

### 12.3 压测计划

| 场景 | 工具 | 目标 |
| --- | --- | --- |
| 500 注册 | 自研 Go 压测端 | 50/s 注册，10s 内完成，无丢失 |
| 250 并发通话 | sipp + 媒体回放 | 60 分钟稳定，丢包 < 0.1% |
| 500 终端广播 | 模拟终端 | 10s 内接入，RTP 连续 |
| 报警风暴 | API/SIP 混合 | 60 次/分钟，弹屏 P95 ≤ 500ms |
| 72h 浸泡 | 80% 负载 | 无重启、无内存增长、无句柄泄漏 |

---

## 13. 测试方案

### 13.1 分层

- 单元测试：解析、路由、状态机、策略、模板。
- 集成测试：注册→呼叫→录音→CDR；报警→弹屏→关闭；广播→终端。
- 端到端：真实设备 + 浏览器调度台。
- 非功能：性能、稳定性、安全、兼容性。

### 13.2 sipp 场景

1. 注册/鉴权/过期/刷新；
2. 基本呼叫/挂断/CANCEL；
3. 报警 fork：多分支、首个应答、其余 CANCEL；
4. 升级链：全部超时 → 下一级；
5. 保持/转接/REFER；
6. 广播 INVITE + 自动应答 + RTP；
7. DTMF 开锁；
8. 异常：重复 INVITE、乱序、缺 ACK。

### 13.3 设备矩阵

| 类别 | 型号 |
| --- | --- |
| 自家报警设备 | 一键报警柱/报警箱（需提供清单） |
| 门口机 | 主流 SIP 门口机 2~3 款 |
| 广播终端 | SIP 音箱/广播网关 2~3 款 |
| 话机 | Yealink / Grandstream / Fanvil 各 1 |
| 软电话 | Linphone / Zoiper / MicroSIP |

### 13.4 浏览器

- Chrome/Edge 最新两个版本；Safari 17+ 抽测。
- Playwright 覆盖登录、接听、广播、开锁、报警处置。

### 13.5 安全测试

- SIP：sipvicious 扫描、弱口令、注册洪泛。
- Web：OWASP ZAP、越权、CSRF、上传。
- 媒体：SRTP 协商失败降级行为。

### 13.6 验收

- 产品方案第 11.4 节；UAT 场景 S1~S6 全部通过。

---

## 14. 风险与对策

| # | 风险 | 影响 | 对策 |
| --- | --- | --- | --- |
| 1 | 自研设备固件版本碎片化（同型号不同批次） | 高 | 建立固件最低版本 + 型号模板；注册上报型号/固件；兼容矩阵回归 |
| 2 | 广播终端自动应答/组播行为不一致 | 中 | 自研固件统一自动应答头与 IGMP 行为；出厂自检 |
| 3 | 视频带宽/多路并发 | 中 | H.264 透传；720p 上限；16 路容量压测 |
| 4 | NAT/单通 | 高 | 锁存/对称 RTP/保活；云端 external_ip；现场 Checklist |
| 5 | WebRTC 兼容/HTTPS | 中 | Chrome/Edge 优先；文档强制 HTTPS；TURN 兜底 |
| 6 | 广播 500 终端启动慢 | 中 | 分批 50/s；组播；预检；进度可见 |
| 7 | 录音磁盘爆 | 中 | 默认只录报警/对讲；保留策略；磁盘告警 |
| 8 | 功能蔓延到 PBX | 高 | 1.0 范围锁定；PBX 进 2.0 |
| 9 | Windows 服务/防火墙（第二平台） | 中 | 安装包自动化；首配向导；Linux 首发验证后再移植 |
| 10 | 许可被绕过 | 低 | 签名/指纹/回拨检测；法律条款 |

---

## 15. 工程计划（WBS）

| 工作包 | 人周 | 对应阶段 |
| --- | --- | --- |
| SIP 接入（传输/注册/鉴权/路由/NAT） | 8 | M1 |
| B2BUA 呼叫（fork/升级/保持/转接） | 9 | M1~M2 |
| 媒体（RTP/SRTP/录音/放音/DTMF/扇出） | 12 | M1~M4 |
| 报警引擎（策略/值班/升级/通知/证据） | 10 | M2 |
| 对讲（视频/开锁/访客/转手机） | 7 | M3 |
| 广播（分区/优先级/调度/TTS/音乐） | 12 | M4 |
| 设备（模板/配置/状态/摄像头） | 6 | M1/M3 |
| 调度台/WebRTC | 8 | M3 |
| Web 管理/API/RBAC/i18n | 10 | M1~M5 |
| 数据/话单/备份/保留 | 5 | M1/M5 |
| 平台（许可/升级/诊断/监控） | 7 | M5 |
| 测试/压测/安全/文档/试点 | 14 | 全程 |
| 缓冲 | 8 | — |
| **合计** | **116 人周 ≈ 27 人月** | 42 周 |

> 116 人周 ÷ 3 人 ≈ 39 周，加上评审/等待/试点支持，日历 42 周；预算按 30~36 人月（含测试与支持）。

---

## 16. 待定技术决策（ADR）

| # | 决策 | 选项 | 建议 |
| --- | --- | --- | --- |
| 1 | 广播默认模式 | 单播 / 组播 | **大区默认组播**（终端已确认支持），小区单播兜底 |
| 2 | TTS | 在线 / 离线 | **本机 Kokoro-82M + 缓存**；离线放 2.0 |
| 3 | 视频 | 透传 / 转码 | **标准 H.264/SIP 透传，不做转码**（已确认） |
| 4 | 数据库 | SQLite / PostgreSQL | 1.0 SQLite；2.0 视规模 |
| 5 | 首发平台 | Linux / Windows | **Linux 首发**；Windows 同代码，晚 4 周 |
| 6 | 许可绑定 | 机器指纹 / 网卡 | 指纹组合，允许 1 次迁移 |
| 7 | 记录范围 | 全量 / 按需 | 默认报警+对讲，普通通话不录 |
| 8 | 手机接警 | H5 / App | 2.0；1.0 用手机浏览器 WebRTC |

---

## 附：与产品方案的对应

- 功能编号（A01~G12）见 [MCS 产品方案](MCS-产品方案.md) 第 5 节；
- 接口、事件、数据库表见 [MCS 接口与数据模型](MCS-接口与数据模型.md)；
- 初期的路线与预算评估见 [可行性评估与总体方案](可行性评估与总体方案.md)。

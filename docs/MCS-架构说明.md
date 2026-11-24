# MCS 架构说明

> 本文面向需要理解、维护或集成 MCS 的研发与运维人员，描述**当前代码实际实现**的架构。
> 产品定位、路线图和未实现能力见[产品方案](MCS-产品方案.md)与[技术方案](MCS-技术方案.md)；
> 接口字段以[API 参考](MCS-API参考.md)和代码为准。

| 项目 | 内容 |
| --- | --- |
| 文档版本 | v1.0 |
| 对应代码 | `0.1.0-dev`（2026-11） |
| 读者 | 后端研发、终端/平台集成、部署运维 |
| 关联文档 | [README](../README.md) · [技术方案](MCS-技术方案.md) · [API 参考](MCS-API参考.md) · [配置参考](MCS-配置参考.md) · [部署手册](../deploy/README.md) |

> **与《技术方案》的关系**：`docs/MCS-技术方案.md` 是早期设计与规划稿，包含尚未实现的能力，
> 部分选型（`chi`、`zerolog`、Vue 3、固定 RTP 端口池等）也与当前代码不同。
> 本文以当前代码为准；两者冲突时，以本文和源码为准。规划与实现的差异汇总见[§18.2](#182-规划稿与当前实现的差异)。

**建议阅读路线**

- 先看全貌：[§0](#0-一分钟了解) → [§2](#2-部署拓扑与运行形态) → [§3](#3-进程内分层)
- 改呼叫/媒体：[§6](#6-sip-接入层) → [§7](#7-呼叫内核b2bua) → [§8](#8-媒体面)
- 接终端/平台：[§6](#6-sip-接入层) · [§10](#10-广播与实时寻呼) · [§13](#13-对外接口与事件)
- 部署运维：[§12](#12-数据与持久化) · [§16](#16-配置构建与部署) · [§17](#17-安全模型)

**目录**

- 总览：[0](#0-一分钟了解) · [1](#1-设计取舍) · [2](#2-部署拓扑与运行形态) · [3](#3-进程内分层) · [4](#4-代码组织) · [5](#5-启动与生命周期)
- 核心：[6](#6-sip-接入层) · [7](#7-呼叫内核b2bua) · [8](#8-媒体面) · [9](#9-报警业务)
- 业务：[10](#10-广播与实时寻呼) · [11](#11-对讲增强) · [12](#12-数据与持久化) · [13](#13-对外接口与事件) · [14](#14-web-控制台)
- 运维：[15](#15-并发可靠性与可观测性) · [16](#16-配置构建与部署) · [17](#17-安全模型) · [18](#18-已知边界与演进方向) · [19](#19-附录)

---

## 0. 一分钟了解

MCS 是一套面向报警运营、园区、校园、医院、工地等场景的**私有化融合通信服务器**。
它把三类业务收敛到同一个进程、同一套 SIP 呼叫内核上：

| 业务 | 入口 | 核心动作 | 产出 |
| --- | --- | --- | --- |
| 报警 | 终端拨 `8xxx`（默认 `8000`） | 按值班级别顺序呼叫，超时自动取消并升级到下一级 | 报警事件、通话录音、话单、控制台弹屏 |
| 对讲 | 终端拨分机号 | B2BUA 双向语音，可选 H.264 视频；支持开锁、抓拍、浏览器观看 | 实时通话、WebRTC 视频窗、DTMF 开锁 |
| 广播 | 控制台、定时任务、话机拨分区短号 | TTS / 音频库 / 实时寻呼 → 分区内 SIP 终端 | 逐终端投递结果、活动广播状态 |

**一句话架构**：

```text
单进程 Go 服务
  = sipgo（SIP 注册/对话/B2BUA）
  + 自研 RTP 中继与媒体处理
  + 报警/广播/寻呼/对讲业务状态机
  + Pion（SRTP、WebRTC）
  + SQLite + JSON + 文件
  + net/http + go:embed 控制台
```

关键事实（2026-11，随代码演进）：

| 维度 | 当前实现 |
| --- | --- |
| 部署形态 | 单进程、单二进制，无外部数据库/消息队列/反向代理依赖 |
| 代码规模 | 约 6.7k 行 Go（不含测试）、2.5k 行测试，38 个 `Test` 函数 |
| 协议 | SIP UDP / SIP TLS、RTP/RTCP、SRTP（SDES）、HTTP/HTTPS、WebSocket、WebRTC |
| 存储 | SQLite（WAL）+ JSON 索引 + 录音/音频/缓存文件 |
| 前端 | 单个 `index.html`，`go:embed` 进二进制，无前端构建步骤 |
| 平台 | Linux / Windows / macOS 交叉编译；Linux systemd，Windows 原生服务 |
| 产物大小 | 约 24–25 MB（含 Pion 与纯 Go SQLite） |

---

## 1. 设计取舍

当前实现不是“大而全”的通信平台，而是针对中小规模私有化部署做的**模块化单体**。
理解下面几组取舍，就能理解后面的模块边界。

| 取舍 | 当前做法 | 原因与代价 |
| --- | --- | --- |
| **Relay-first** | 两端编码一致时只转发 RTP，不解码、不混音、不做 AEC；只有录音、广播、寻呼、WebRTC 才终止或注入媒体 | CPU 低、音质无损、故障面小；混合编码需显式转码，服务器侧音频能力有限 |
| **单进程单二进制** | SIP、HTTP、业务、媒体、数据库全部在一个进程内；前端嵌入二进制 | 现场部署简单；无法水平扩展，进程重启会丢失实时状态 |
| **实时态放内存** | 注册、活动通话、活动广播、分页会话、控制台会话都在内存 | 路径短、延迟低；重启后终端需重新注册，通话中断 |
| **持久态放 SQLite/文件** | 设备、话单、报警、TTS 设置进 SQLite；录音、音频库、定时任务、TTS 缓存进文件 | 无外部依赖、可备份；SQLite 单写者，写库失败不阻断通话 |
| **配置与数据库分工** | 拓扑（端口、分区、报警级别）在 `config.yaml`；设备、TTS 设置可在控制台改并落库 | 运维可调整；配置只读一次，除 TTS 外不支持热更新 |
| **薄控制台** | 原生 HTML/CSS/JS 单文件，REST 拉快照 + WebSocket 推事件 | 无 Node 构建、易嵌入；组件化和前端工程能力弱 |
| **显式装配** | `main` 里手工构造依赖，`net/http` 标准 `ServeMux`，无 DI/框架 | 链路清晰、易排障；`sipserver` 包会随功能增长而变大 |

这些取舍共同决定了：**`internal/sipserver` 是系统核心，`internal/api` 与 `internal/webui` 是薄接入层，`internal/store` 负责状态与持久化。**

---

## 2. 部署拓扑与运行形态

### 2.1 拓扑

```text
     报警柱 / 门口机 / SIP 音箱 / IP 话机                浏览器（值班台 / 调度台）
                 │  SIP UDP 5060 / SIP TLS 5061              │ HTTP 8080 / HTTPS 8443
                 │  RTP/RTCP 动态 UDP                        │ WebSocket / WebRTC
                 ▼                                           ▼
      ┌──────────────────────────────────────────────────────────────────┐
      │                            MCS 单进程                             │
      │                                                                  │
      │  sipgo：注册、对话、B2BUA        net/http：REST、WS、静态控制台     │
      │  媒体：RTP 中继、SRTP、录音       业务：报警、广播、寻呼、对讲、TTS   │
      └───────────────┬──────────────────────────────┬───────────────────┘
                      │                              │
              SQLite  mcs.db                  data/ 文件
        （设备 / 话单 / 报警 / 设置）    （录音 / 音频库 / TTS 缓存 / 证书 / 定时任务）
                      │
          HTTP 出站 ──┴──> 外部 TTS（OpenAI 兼容 / MiniMax，可选）
                      └──> 设备 HTTP 抓拍地址（可选）
```

- 所有服务同机部署；TTS 可以是本机 Kokoro，也可以是公网 OpenAI 兼容服务或 MiniMax。
- 不依赖 MySQL、Redis、RabbitMQ、Nginx；TTS 和抓拍是仅有的可选外部 HTTP 依赖。
- SIP 终端和浏览器是两类完全不同的接入面，但共用同一个业务/媒体内核。

### 2.2 监听端口与网络要求

| 用途 | 默认地址 | 协议 | 说明 |
| --- | --- | --- | --- |
| SIP | `0.0.0.0:5060` | UDP | 终端注册、呼叫、广播信令 |
| SIP over TLS | 空（可配 `0.0.0.0:5061`） | TLS over TCP | 可选，TLS 1.2+ |
| 控制台 HTTP | `127.0.0.1:8080` | HTTP | 默认只绑本机；生产可改 `0.0.0.0` 或放反向代理后 |
| 控制台 HTTPS | 空（可配 `0.0.0.0:8443`） | HTTPS | 可选，TLS 1.2+；证书为空时复用 SIP TLS 证书 |
| RTP / RTCP | 动态高位 UDP | UDP | 每个 RTP relay 一个 UDP socket（音频/视频各一对），每个广播会话一个；**没有固定端口段** |
| WebRTC | 动态 UDP | ICE / DTLS / SRTP | 仅 host candidate，未配置 STUN/TURN |
| 出站 HTTP | — | HTTP/HTTPS | TTS 合成、设备抓拍 |

> **防火墙提示**：因为 RTP 端口是动态的，Linux 防火墙需要放行高位 UDP；Windows 安装脚本会给
> `mcs.exe` 增加动态 RTP 入站规则。若现场安全策略不允许大范围 UDP，需要后续实现固定 RTP 端口段。

开发脚本会改写端口，避免和本机已有服务冲突：

| 脚本 | SIP | HTTP | HTTPS | 数据目录 |
| --- | --- | --- | --- | --- |
| `make run` | `config.yaml` | `config.yaml` | 按配置 | `./data` |
| `make run-https` | `127.0.0.1:15060` | `127.0.0.1:38084` | `127.0.0.1:38443` | `./data` |
| `make run-lan` | `0.0.0.0:5060` | `0.0.0.0:18080` | `0.0.0.0:18443` | `./data-lan` |

### 2.3 运行形态

| 形态 | 启动方式 | 典型用途 |
| --- | --- | --- |
| 开发 HTTP | `make run` | 本机 SIP 软电话、REST 调试 |
| 开发 HTTPS | `make run-https` | 浏览器 WebRTC/麦克风需要安全上下文 |
| LAN 真机 | `make run-lan` | 真实终端联调，自动探测 LAN IP 写入 `advertise` |
| Linux 生产 | systemd 单元 `mcs.service` | 现场服务器，`/opt/mcs` + `/etc/mcs` + `/var/lib/mcs` |
| Windows 生产 | 原生 Windows Service + Inno Setup 安装包 | 现场 Windows Server/PC |
| macOS | 直接运行二进制或开发脚本 | 开发、演示；无正式安装包 |

> `advertise` 必须填**设备能访问到的地址**。写 `127.0.0.1` 时只能本机软电话使用；
> 局域网真机必须写服务器 LAN IP，否则会出现单通/无声音。

---

## 3. 进程内分层

### 3.1 分层视图

```text
┌─────────────────────────────────────────────────────────────────────────┐
│ 接入层    sipgo：SIP UDP / TLS          net/http：HTTP / HTTPS / WebSocket │
├─────────────────────────────────────────────────────────────────────────┤
│ 会话层    REGISTER / Digest · INVITE 对话 · B2BUA · SDP 改写              │
├─────────────────────────────────────────────────────────────────────────┤
│ 业务层    报警升级 · 对讲/开锁/抓拍 · 广播 · 实时寻呼 · 定时广播 · TTS      │
├─────────────────────────────────────────────────────────────────────────┤
│ 媒体层    RTP/RTCP 中继 · SRTP(SDES) · G.711 · 录音 · WebRTC tap          │
├─────────────────────────────────────────────────────────────────────────┤
│ 数据层    内存 Store · SQLite · JSON · 文件系统                           │
├─────────────────────────────────────────────────────────────────────────┤
│ 支撑      config · slog · eventBus · scheduler · webui(embed)             │
└─────────────────────────────────────────────────────────────────────────┘
```

这不是严格的包边界：`internal/sipserver` 同时承载会话、业务和媒体，以减少跨包接口和生命周期传递。
随着功能增长，这个包会变大；当前仍选择“先清晰、后可拆”的模块化单体。

### 3.2 包依赖

```text
cmd/mcs
  ├── internal/config
  ├── internal/store ────────────┐
  ├── internal/sipserver ── internal/tts
  └── internal/api ── internal/sipserver
          ├── internal/store
          └── internal/webui
```

依赖方向是单向的：

- `store` 不依赖 `sipserver` / `api`；
- `tts` 是独立 HTTP 客户端，不依赖业务包；
- `webui` 只负责嵌入静态文件；
- `api` 通过 `sipserver` 暴露的门面方法操作呼叫、广播、媒体库、TTS、定时任务，同时直接读取设备/话单/报警存储。

### 3.3 核心抽象

| 抽象 | 位置 | 职责 |
| --- | --- | --- |
| `Server` | [`internal/sipserver/server.go`](../internal/sipserver/server.go) | SIP 栈、注册、配置、各业务管理器的总入口 |
| `call` | [`internal/sipserver/call.go`](../internal/sipserver/call.go) | 一通 B2BUA 通话：A/B 腿、媒体中继、录音、报警升级 |
| `relay` | [`internal/sipserver/media.go`](../internal/sipserver/media.go) | 单腿 RTP/RTCP 转发、SRTP 加解密、转码、统计 |
| `recorder` | [`internal/sipserver/media.go`](../internal/sipserver/media.go) | 双声道 WAV 录音 |
| `mediaTap` | [`internal/sipserver/webrtc.go`](../internal/sipserver/webrtc.go) | 把 SIP RTP 扇出到浏览器 PeerConnection |
| `broadcastSession` / `broadcastManager` | [`internal/sipserver/broadcast.go`](../internal/sipserver/broadcast.go) | 广播会话、终端扇出、优先级抢占 |
| `scheduleStore` | [`internal/sipserver/schedule.go`](../internal/sipserver/schedule.go) | 定时广播的 cron 与 JSON 持久化 |
| `mediaLibrary` | [`internal/sipserver/media_library.go`](../internal/sipserver/media_library.go) | 音频库索引与文件 |
| `eventBus` | [`internal/sipserver/events.go`](../internal/sipserver/events.go) | 进程内发布/订阅，供 WebSocket 推送 |
| `Store` / `DB` / `CDRStore` / `AlarmStore` | [`internal/store/`](../internal/store/) | 设备与注册状态、SQLite、话单、报警 |
| `api.Server` | [`internal/api/server.go`](../internal/api/server.go) | REST、WebSocket、会话鉴权、静态资源 |
| `tts.Client` | [`internal/tts/client.go`](../internal/tts/client.go) | OpenAI 兼容 / MiniMax TTS 与缓存 |

---

## 4. 代码组织

### 4.1 目录

```text
cmd/mcs/                服务入口、启动装配、Windows 服务适配
cmd/uasim/              开发用 SIP 批量注册模拟器（不属于产品）
internal/config/        YAML 配置、默认值、校验
internal/store/         设备/注册状态、话单、报警、SQLite
internal/sipserver/     SIP、呼叫、媒体、报警、广播、寻呼、WebRTC、TTS 胶水、定时、音频库
internal/tts/           OpenAI 兼容 / MiniMax TTS 客户端与缓存
internal/api/           REST / WebSocket、会话鉴权、请求日志中间件
internal/webui/         go:embed 控制台（index.html）
deploy/                 systemd、Windows 服务、Inno Setup、诊断脚本
docs/                   产品/技术/接口/配置/部署/测试文档
```

### 4.2 包职责与规模

| 包 | 非测试代码约 | 职责 | 关键文件 |
| --- | ---: | --- | --- |
| `cmd/mcs` | 267 行 | 入口、flag、日志、依赖装配、监听启动、优雅关闭、Windows 服务 | [`main.go`](../cmd/mcs/main.go)、[`service_windows.go`](../cmd/mcs/service_windows.go) |
| `internal/config` | 242 行 | `config.yaml` 解析、默认值、跨字段校验 | [`config.go`](../internal/config/config.go) |
| `internal/store` | 698 行 | 设备与注册状态、话单环形缓存、报警存储、SQLite schema | [`store.go`](../internal/store/store.go)、[`sqlite.go`](../internal/store/sqlite.go)、[`cdr.go`](../internal/store/cdr.go)、[`alarm.go`](../internal/store/alarm.go) |
| `internal/sipserver` | 4,286 行 | SIP 会话、B2BUA、媒体、报警、广播、寻呼、WebRTC、定时、音频库 | 见 §4.3 |
| `internal/api` | 810 行 | 36 条 REST 路由、WebSocket、控制台登录、日志中间件 | [`server.go`](../internal/api/server.go)、[`auth.go`](../internal/api/auth.go)、[`middleware.go`](../internal/api/middleware.go) |
| `internal/tts` | 239 行 | TTS HTTP 客户端、中英文音色选择、SHA-256 缓存 | [`client.go`](../internal/tts/client.go) |
| `internal/webui` | 19 行 + HTML | `go:embed` 单页控制台 | [`webui.go`](../internal/webui/webui.go)、[`index.html`](../internal/webui/index.html) |
| `cmd/uasim` | 136 行 | 开发联调：批量注册、自动刷新、注销 | [`main.go`](../cmd/uasim/main.go) |

> 行数为 2026-11 的静态统计，仅用于判断模块体量，不构成代码质量指标。

### 4.3 `sipserver` 内部文件地图

| 文件 | 职责 |
| --- | --- |
| [`server.go`](../internal/sipserver/server.go) | 构造 sipgo UA/Server/Client/DialogUA，注册回调，REGISTER/OPTIONS，TLS 监听 |
| [`call.go`](../internal/sipserver/call.go) | B2BUA 主流程：目标解析、A/B 腿、SDP 改写、升级、挂断、话单、开锁 |
| [`media.go`](../internal/sipserver/media.go) | RTP/RTCP relay、首包锁存、G.711 转码、立体声录音、G.711 编解码 |
| [`sdp.go`](../internal/sipserver/sdp.go) | SDP 解析、端口/连接地址/crypto 改写 |
| [`srtp.go`](../internal/sipserver/srtp.go) | SDES `a=crypto` 解析、密钥生成、Pion SRTP Context |
| [`digest.go`](../internal/sipserver/digest.go) | RFC 7616 子集 Digest 校验、无状态 nonce |
| [`broadcast.go`](../internal/sipserver/broadcast.go) | 分区、广播会话、终端 INVITE、RTP 扇出、优先级抢占 |
| [`paging.go`](../internal/sipserver/paging.go) | 话机拨分区号的实时寻呼，RTP 注入广播会话 |
| [`schedule.go`](../internal/sipserver/schedule.go) | 5 段 cron、定时任务 JSON、5 秒调度器 |
| [`media_library.go`](../internal/sipserver/media_library.go) | 音频库上传、索引、下载、删除 |
| [`audio.go`](../internal/sipserver/audio.go) | WAV/MP3 解码、重采样、µ-law 帧化 |
| [`opus.go`](../internal/sipserver/opus.go) | Opus 20ms 帧编码 |
| [`webrtc.go`](../internal/sipserver/webrtc.go) | 浏览器 offer/answer、视频/音频 tap、麦克风回传 |
| [`events.go`](../internal/sipserver/events.go) | 进程内事件总线与事件类型 |

### 4.4 改代码去哪里

| 要改什么 | 先看 |
| --- | --- |
| SIP 注册、Digest、监听端口 | [`server.go`](../internal/sipserver/server.go)、[`digest.go`](../internal/sipserver/digest.go) |
| 呼叫流程、报警升级顺序 | [`call.go`](../internal/sipserver/call.go) |
| RTP 转发、转码、录音 | [`media.go`](../internal/sipserver/media.go) |
| SDP 端口/crypto 改写 | [`sdp.go`](../internal/sipserver/sdp.go)、[`srtp.go`](../internal/sipserver/srtp.go) |
| 广播扇出、优先级、组播 | [`broadcast.go`](../internal/sipserver/broadcast.go) |
| 实时寻呼 | [`paging.go`](../internal/sipserver/paging.go) |
| 定时广播 | [`schedule.go`](../internal/sipserver/schedule.go) |
| TTS 供应商/音色/缓存 | [`internal/tts/client.go`](../internal/tts/client.go)、[`server.go`](../internal/sipserver/server.go) |
| 浏览器视频/麦克风 | [`webrtc.go`](../internal/sipserver/webrtc.go)、[`index.html`](../internal/webui/index.html) |
| REST / WebSocket | [`internal/api/server.go`](../internal/api/server.go) |
| 数据表与持久化 | [`internal/store/sqlite.go`](../internal/store/sqlite.go) |
| 控制台页面 | [`internal/webui/index.html`](../internal/webui/index.html) |

---

## 5. 启动与生命周期

### 5.1 装配顺序

```text
config.Load(configPath)
        │
        ├── newLogger(-log)
        ├── auth.secret 为空？→ 生成 32 字节随机数
        ├── MkdirAll(data_dir)
        ├── store.OpenDB(data/mcs.db) → WAL + schema migrate
        ├── TTS 设置：DB settings["tts"] 覆盖 YAML
        ├── store.NewWithDB(cfg.Devices, db)
        ├── store.NewCDRStoreDB(2000, db)
        ├── store.NewAlarmStoreDB(1000, db)
        ├── sipserver.New(store, auth, cdr, alarms, opts)
        ├── api.New(...).Handler()
        ├── sipSrv.StartScheduler(ctx)
        ├── go sipSrv.Serve(SIP UDP)
        ├── 可选：go sipSrv.ServeTLS(SIP TLS)
        ├── 可选：go HTTPS server
        └── go HTTP server
```

关键点：

1. **设备导入只发生一次**：如果 SQLite `devices` 表为空，才从 `config.yaml` 的 `devices` 导入；
   之后设备以数据库为准。
2. **TTS 配置可持久化**：启动时先读 YAML，再用 SQLite `settings` 里的 `tts` JSON 覆盖；
   控制台保存后，下次启动仍然生效。
3. **认证 secret**：`auth.secret` 为空时生成随机值并打警告；重启后旧 nonce 失效，终端重新注册即可。
4. **监听失败即退出**：任一 SIP/HTTP/HTTPS 监听器异常都会取消根 context，日志记录后进程退出。
5. **可选监听器**：SIP TLS / HTTPS 只在配置了地址和证书时启动；缺证书会打警告并跳过。

### 5.2 命令行

| 参数 | 默认 | 说明 |
| --- | --- | --- |
| `-config` | `config.yaml` | YAML 配置路径；缺失时给出 `cp config.example.yaml config.yaml` 提示 |
| `-log` | 空 | 可选日志文件，同时仍输出到 stdout |
| `-service` | `false` | 以 Windows 服务模式运行（仅 Windows 构建有效） |
| `-version` | `false` | 打印版本后退出 |

### 5.3 关闭

- 捕获 `SIGINT` / `SIGTERM`（Windows 服务由 SCM Stop/Shutdown 触发取消）。
- HTTP 和 HTTPS 使用 5 秒超时 `Shutdown`。
- 关闭 sipgo server 与 UA。
- 关闭 SQLite。

> 当前**没有“通话排空”流程**：关闭时不会主动结束活动通话、不会等待录音收尾。
> 极端情况下，正在进行的通话可能来不及完成录音/话单写入；生产升级建议在低话务时段进行。

---

## 6. SIP 接入层

### 6.1 sipgo 栈

`internal/sipserver.New` 创建四个对象：

| 对象 | 用途 |
| --- | --- |
| `sipgo.UserAgent` | SIP 栈身份，User-Agent 为 `MCS/0.1` |
| `sipgo.Server` | 接收注册与呼叫请求，注册 `OnRegister` / `OnOptions` / `OnInvite` / `OnAck` / `OnBye` / `OnCancel` / `OnInfo` / `OnNoRoute` |
| `sipgo.Client` | 作为 UAC 发起 B 腿 INVITE、广播 INVITE、BYE、INFO |
| `sipgo.DialogUA` | 对话层辅助，Contact 固定为 `sip:mcs@<advertise>` |

监听方式：

- `Serve`：UDP，默认 `0.0.0.0:5060`；
- `ServeTLS`：TCP + TLS 1.2+，可选；
- 没有启用明文 TCP SIP / SIP over WebSocket。

响应中一般带 `Server: MCS/0.1`；`OPTIONS` 返回 `Allow: INVITE, ACK, CANCEL, BYE, REGISTER, OPTIONS, INFO`；
未知方法返回 `405 Method Not Allowed`。

### 6.2 注册与 Digest 鉴权

```text
终端                                      MCS
 │ REGISTER（无 Authorization）             │
 │────────────────────────────────────────>│ 401 + WWW-Authenticate
 │ REGISTER + Authorization: Digest ...    │
 │────────────────────────────────────────>│ 校验 nonce(HMAC) + Digest response
 │<────────────────────────────────────────│ 200 OK + Contact + Expires
```

实现细节：

- 分机号取 `To` 的 user 部分，回退到 `From`。
- 无 `Authorization`：返回 `401`，`WWW-Authenticate: Digest realm=..., nonce=..., algorithm=MD5, qop="auth"`。
- 未知分机：`403 Forbidden`。
- 密码错误：再次 `401` 挑战。
- `nonce` 是**无状态**的：`base64(ts + ":" + hex(HMAC-SHA256(secret, ts)))`，TTL 5 分钟；
  服务重启后只要 `auth.secret` 不变，旧 nonce 在 TTL 内仍有效。
- `DigestResponse` 支持 MD5、MD5-sess、SHA-256、SHA-256-sess，`qop=auth`；挑战广告的是 MD5。
- 没有 `Contact` 的 REGISTER 视为查询，返回 `200`（当前不返回绑定列表）。
- `Expires: 0` 或 `Contact;expires=0`：注销，清空 Contact/Source/Expires，发 `device.unregistered`。
- 正常注册：记录 Contact、REGISTER 源地址、User-Agent、`X-MCS-Model`、`X-MCS-Firmware`、过期时间，
  发 `device.registered`。
- TTL 取 `Expires` 或 Contact 的 `expires` 参数，缺省 3600 秒。
- 在线判定：`enabled && contact != "" && expires_at > now`，是**计算值**，不是独立的在线表。

> 注册状态只在内存中。MCS 重启后所有设备显示离线，直到终端重新 REGISTER；
> 设备本身（ext/name/password/zone/snapshot）已经持久化在 SQLite。

### 6.3 INVITE 分流

```text
INVITE
  │
  ├─ 目标号 == 某个分区 dial？ ──────────────> 实时寻呼（paging.go）
  │
  ├─ 目标号以 "8" 开头？ ───────────────────> 报警 B2BUA（call.go）
  │
  ├─ 目标号是在线分机（且不是主叫自己）？ ──> 普通对讲 B2BUA（call.go）
  │
  └─ 其他 ──────────────────────────────────> 404 Not Found
```

- 报警识别规则是 `to == alarm.number || strings.HasPrefix(to, "8")`，即**所有 8 开头的号码**都会进入报警流程，
  不只是配置里的 `8000`。
- 无 SDP body 或无音频 `m=` 行：`488 Not Acceptable Here`。
- 有效 INVITE 先 `dua.ReadInvite` 建立服务端对话，再回 `100 Trying`，然后进入 `bridge`。
- **处理函数会一直阻塞在 `bridge` 直到通话结束**：sipgo 在 handler 返回时会结束 server transaction，
  为了让后续 ACK/BYE/CANCEL 仍能匹配，桥接流程必须在 handler 内运行。
- 当前 INVITE/BYE/CANCEL **没有 Digest 鉴权**，见[§17](#17-安全模型)。

### 6.4 地址修正与 NAT 基础

现场常见问题：终端位于 VPN/TUN、软电话把 Contact 写成 `127.0.0.1`、SDP `c=` 写成 `0.0.0.0`。
MCS 做了两类“latching 式”修正：

| 场景 | 处理 |
| --- | --- |
| Contact 的 host 是 loopback / `0.0.0.0` / `198.18.0.0/15`，而 REGISTER 源地址是真实地址 | 用 REGISTER 源 IP/端口替换 Contact 的 host/port |
| SDP `c=IN IP4` 是 loopback / 未指定 / `198.18.0.0/15`，而信令源地址是真实地址 | 用信令源 IP 替换媒体地址 |
| 正常 Contact / SDP | 保持原值 |
| 出站 Contact / SDP | 统一使用 `server.advertise`（host + port） |

RTP 侧还有一层**首包锁存**：relay 初始对端来自 SDP，收到包后会用实际源地址覆盖；
后续仍跟随最新源地址。这能解决部分 NAT 场景，但也意味着媒体端口没有源认证，见[§17](#17-安全模型)。

> MCS 不是完整 SBC：SIP 侧没有 ICE/TURN/STUN、DNS SRV、拓扑隐藏或 SIP 注册代理集群。

---

## 7. 呼叫内核（B2BUA）

### 7.1 什么是 B2BUA

MCS 不是简单的 SIP 代理，而是 **Back-to-Back User Agent**：

- **A 腿**：主叫终端 → MCS（服务端对话，`call.a`）；
- **B 腿**：MCS → 被叫终端（客户端对话，`call.b`）；
- 两条腿各自协商 SDP，MCS 在中间改写端口、地址和 SRTP 密钥；
- 媒体由 MCS 的 relay 转发，信令和媒体都可以独立终止。

这样做的直接收益：录音、报警升级、广播、WebRTC 观看、开锁都能在服务器侧完成，
而不需要终端之间直接互通。

### 7.2 `call` 对象

| 分组 | 主要字段 | 说明 |
| --- | --- | --- |
| 标识 | `id`、`aCallID`、`bCallID` | `id` 形如 `mcs-<unixnano>-<seq>`；B 腿为 `<id>-b<attempt>` |
| 对话 | `a`、`b` | A 腿 `DialogServerSession`，B 腿 `DialogClientSession` |
| 媒体 | `relayA`、`relayB`、`videoA`、`videoB`、`audioTap`、`videoTap`、`rec` | 音频/视频各一对 relay，录音器，WebRTC tap |
| 编码 | `codecA`、`codecB`、`audioPT` | 两侧协商出的 G.711 编码与 A 腿 payload type |
| 状态 | `startAt`、`answerAt`、`result`、`to` | 通话时间线、结果、当前被叫 |
| 报警 | `targets`、`attempt`、`level`、`alarmID` | 升级链、当前尝试、级别、关联报警事件 |
| 生命周期 | `ctx`、`cancel`、`once`、`stateMu` | 取消、终结一次、字段并发保护 |
| 反向引用 | `server` | 访问 store、cdr、alarms、日志、事件总线 |

`callRegistry` 用 `map[string]*call` 同时按 A 腿和 B 腿 Call-ID 索引；列表时按指针去重。
BYE/CANCEL 到达时，先按 Call-ID 找 call，再区分是 A 腿还是 B 腿。

对外可见的通话状态只有 `ringing`（未接听）和 `answered`（已接听），由 `ActiveCalls` 根据 `answerAt` 推导；
内部还有 `result`、`attempt`、`level` 等字段。通话结束后 call 从 registry 移除，最终状态落在 CDR 和报警事件里。

### 7.3 一通报警呼叫的时序

下面以 `1002` 拨 `8000`，第一级 `1001` 超时、升级到 `1003` 为例：

```text
1002                MCS                          1001（一级）        1003（二级）
 │ INVITE 8000       │                               │                  │
 │──────────────────>│ resolveTargets                │                  │
 │<── 100 Trying ────│ alarm.created                │                  │
 │                   │ INVITE + 改写 SDP             │                  │
 │                   │──────────────────────────────>│                  │
 │                   │<──────── 180/183 + SDP ───────│                  │
 │<── 180/183 ───────│                               │                  │
 │                   │  一级超时（示例 15s）          │                  │
 │                   │  CANCEL ─────────────────────>│                  │
 │                   │  alarm.escalated              │                  │
 │                   │  INVITE ──────────────────────────────────────>│
 │                   │<────────────────────────────── 200 + SDP ───────│
 │<── 200 + SDP ─────│  ACK                          │  ACK             │
 │                   │                               │                  │
 │════════════════ RTP A ═══ relay ═══ RTP B ═══════════════════════════│
 │                   │  录音 tap / RTP 计数 / 话单     │                  │
 │ BYE               │                               │                  │
 │──────────────────>│  BYE ────────────────────────────────────────>│
 │                   │  CDR + call.ended + alarm.updated                │
```

实际代码里，`bridge` 依次尝试 `targets`：

1. 若目标号是报警号，先创建报警事件并推送 `alarm.created`；
2. 对每个目标：分配 relay/recorder → 改写 SDP → 向 B 腿发 INVITE；
3. B 腿的 `100` 不转发，`180/183` 转发给 A 腿，并改写 provisional SDP 中的媒体端口；
4. B 腿 `200` 后：解析 answer、建立 SRTP、启动 relay/tap、向 A 腿回 `200 + SDP`、向 B 腿发 ACK；
5. 等待通话结束（A 腿 BYE / B 腿 BYE / CANCEL / 目标失败）；
6. 失败则 `cleanupAttempt`，尝试下一个目标；成功则阻塞到 `ctx.Done()`；
7. 全部失败：回 `480 Temporarily Unavailable`，如果最后一次是忙则回 `486 Busy Here`。

### 7.4 每条腿的建立细节

| 步骤 | A 腿（主叫） | B 腿（被叫） |
| --- | --- | --- |
| SDP 来源 | INVITE body | MCS 用 A 的 offer 改写后发给 B |
| 媒体端口 | relay A 的 UDP 端口 | relay B 的 UDP 端口 |
| 连接地址 | `server.advertise` 的 host | `server.advertise` 的 host |
| SRTP | A 的 `a=crypto` 解密 A→MCS；MCS 生成新密钥写入回给 A 的 SDP，用于 MCS→A | MCS 生成新密钥写入发给 B 的 SDP，用于 MCS→B；B 的 `a=crypto` 解密 B→MCS |
| 视频 | 有 `m=video` 才分配视频 relay | B 不接受视频时，回给 A 的 `m=video` 端口置 0 |
| 应答 | 把 B 的 answer 改写后回给 A | 收到 B 的 200 后 ACK |

关键设计：

- **默认不转码**。A/B 各自与 MCS 协商，正常情况下 B 会从 A 的 offer 中选择共同编码，所以直接转发 RTP。
- `media.transcode: true` 时才在 G.711 A-law/µ-law 不一致时转换 payload 并归一化 PT（0 或 8）。
  这只是一个兼容层，不是通用转码器。
- **视频按 H.264 设计，原样透传**。MCS 不解析 H.264、不转码、不转封装；浏览器观看时由 Pion 的 H.264 `TrackLocalStaticRTP` 转发。
- **SRTP 按腿独立密钥**。MCS 解密 A 腿后，用 B 腿的密钥重新加密；两腿密钥不共享。
- 如果 A 腿是 SAVP 而 B 腿不是，或 B 腿返回的 SAVP 与 A 腿不匹配，该次尝试直接失败，继续升级下一个目标。

### 7.5 结束与话单

`call.finish` 由 `sync.Once` 保证只执行一次：

```text
关闭 relay/tap/recorder
  → 生成录音 WAV（若有音频）
  → 计算时长（从 answerAt 到 now）
  → 写 CDR（内存 ring + SQLite）
  → emit call.ended
  → 更新报警状态（answered → pending，否则 unanswered；closed 保持 closed）
  → 从 callRegistry 移除
  → cancel ctx
```

结果值：

| 场景 | CDR `result` | 报警状态 |
| --- | --- | --- |
| 正常接听并挂断 | `answered` | `pending`（待处置） |
| 振铃超时 / `408` / `480` | `no_answer` | `unanswered` |
| `486` / `600` | `busy` | `unanswered` |
| `603` | `rejected` | `unanswered` |
| 主叫 CANCEL | `canceled` | `unanswered` |
| 其他失败 | `failed` | `unanswered` |

CDR 字段：`id`、`call_id`、`from`、`to`、`start_at`、`answer_at`、`end_at`、`duration_s`、`result`、`recording`。
`recording` 是 `/recordings/yyyy/mm/dd/<call-id>.wav` 形式的相对 URL。

### 7.6 通话中的控制面

| 能力 | 入口 | 实现 |
| --- | --- | --- |
| 远程开锁 | `POST /api/v1/intercom/{call_id}/unlock` | 已接听通话中，向门口机所在腿发 SIP INFO `application/dtmf-relay`（`Signal=<code>`、`Duration=200`），失败时回退 `application/dtmf` |
| 浏览器视频/音频 | `POST /api/v1/webrtc/offer` | 见[§11.1](#111-webrtc-视频窗) |
| 抓拍 | `GET /api/v1/devices/{ext}/snapshot` | 服务端代理设备配置的 HTTP 快照地址，见[§11.3](#113-抓拍代理) |
| 来电 INFO | SIP `INFO` | 统一回 `200 OK`，当前不处理业务语义 |

开锁的“门口机”判定：如果主叫分机以 `6` 开头，认为门口机在 A 腿；否则认为在 B 腿。
开锁码默认 `#`，控制台允许输入 `#`、`*`、`#123` 等；具体门口机是否接受由设备决定。

---

## 8. 媒体面

### 8.1 relay 模型

每个 relay（每条腿的音频/视频各一个）使用独立的 UDP socket：

```text
 1002 ──RTP──> [relay A] ──RTP──> [relay B] ──RTP──> 1001
                   │                       │
                   ├─ tap → recorder A     ├─ tap → recorder B
                   ├─ tap → audioTap ──────┼──────> 浏览器（WebRTC）
                   └─ tap → videoTap ──────┘

 浏览器麦克风 ──RTP──> relay（门口机所在腿）──> 门口机
```

一个 `relay` 的核心行为：

1. `newRelay` 绑定 `0.0.0.0:0`，得到一个动态 UDP 端口；
2. `setPeer` 用 SDP 的媒体地址初始化对端；
3. `startWithTap` 启动一个读循环：
   - 读 UDP；
   - 用实际源地址覆盖 `peer`（latching）；
   - 若配置了 SRTP，先解密；
   - RTCP 直接转发，RTP 计数；
   - 可选 `onPacket`（视频 tap）和 `onPayload`（录音）；
   - 需要转码时转换 payload/PT；
   - 加密（若目的腿是 SRTP）后写到目的 relay 的 peer。
4. 关闭时关闭 UDP socket。

**没有**抖动缓冲、乱序重排、丢包隐藏、混音、AEC、音量调节或视频关键帧缓存。
MCS 假设终端和网络能提供可接受的 RTP 流；它只做转发和必要的录制/注入。

### 8.2 音频处理

| 场景 | 处理 |
| --- | --- |
| 普通 B2BUA 通话 | G.711 PCMU/PCMA 原样转发；`media.transcode=true` 时才做 A/U 转换与 PT 归一化 |
| 录音 | G.711 解码为 PCM16LE，左声道 A 腿、右声道 B 腿，8kHz 立体声 WAV |
| TTS / 音频库广播 | WAV/MP3 解码 → 重采样到 8kHz → µ-law 160 字节帧；或重采样到 48kHz → Opus 20ms 帧 |
| 实时寻呼 | 话机 RTP → PCMA 转 PCMU → 拆成 160 字节帧 → 注入广播会话 |
| 浏览器麦克风 | 浏览器 RTP → 改写 PT/SSRC → 直接写到门口机所在 relay 的 peer |
| 其他 SIP 编码 | RTP 可以透传，但录音/转码/浏览器侧不保证；G.722/G.729 未实现 |

G.711 工具在 [`media.go`](../internal/sipserver/media.go) 和 [`audio.go`](../internal/sipserver/audio.go)：
µ-law/A-law 编解码、PCM16 重采样、WAV 头、20ms 帧化。

### 8.3 SRTP / SRTCP

- 只实现 **SDES**（SDP `a=crypto`），支持：
  - `AES_CM_128_HMAC_SHA1_80`
  - `AES_CM_128_HMAC_SHA1_32`
- 每条腿生成独立密钥：MCS 用 A 腿的密钥解密 A→MCS，用另一把密钥加密 MCS→A；
  B 腿同理，因此两腿的密钥材料不共享。
- SRTCP 跟随对应腿解密/加密后转发。
- 不实现 SIP 终端的 DTLS-SRTP；WebRTC 的 DTLS-SRTP 由 Pion 自动处理。
- 广播和实时寻呼固定使用明文 RTP（`RTP/AVP`），不支持 SRTP。
- SDES 密钥在 SDP 中传输，**生产必须启用 SIP over TLS**，否则密钥随信令明文暴露。

### 8.4 录音

- 每通 B2BUA 通话创建一个 `recorder`，在 `data/recordings/.tmp/` 下写两个 raw 文件：
  `<call-id>.a.raw`（A 腿）和 `<call-id>.b.raw`（B 腿）。
- relay 收到 payload 后解码为 PCM16LE 追加写入，写操作有互斥锁。
- 通话结束时按 320 字节块交错合并为 8kHz、16-bit、双声道 WAV：
  **左声道 = A 腿，右声道 = B 腿**。
- 最终路径：`data/recordings/yyyy/mm/dd/<call-id>.wav`，URL 为 `/recordings/...`。
- 两个 raw 都为空则不生成文件；只有一个有数据时另一声道补静音。
- 当前**不分段、不做哈希链、不做视频录像、不自动清理**；录音格式按 G.711 解码，非 G.711 音频不保证可听。

### 8.5 视频

- 视频 relay 与音频 relay 结构相同，只是不录音、不做 payload 转换。
- 呼叫建立时，A 的 offer 有 `m=video` 且端口非 0，就分配一对视频 relay；
  B 的 answer 也接受视频时，才真正启用；否则回给 A 的 `m=video` 端口置 0，并关闭视频 relay。
- H.264 RTP 原样转发；SDP 中的 `a=fmtp`、`packetization-mode` 等行保留。
- 浏览器观看通过 `mediaTap` 把 SIP 视频 RTP 写给 Pion 的 `TrackLocalStaticRTP`；
  浏览器回传视频未实现。
- 非 H.264 的 SIP 视频可以透传，但 WebRTC 观看端固定按 H.264 处理，不保证兼容。

---

## 9. 报警业务

### 9.1 触发与事件

- 目标号为 `alarm.number`（默认 `8000`）或以 `8` 开头，进入报警流程。
- 只有解析到至少一个可呼叫目标时才创建报警事件；否则直接 `404`。
- 事件字段：`id`、`call_id`、`from`、`to`、`alarm_number`、`device_name`、`zone`、
  `state`、`level`、`level_name`、`answered_by`、时间线、`recording`、`test`。
- 事件同时写内存 ring（最多 1000 条）和 SQLite；启动时从 SQLite 回填最近的记录。

### 9.2 目标解析与升级策略

配置示例：

```yaml
alarm:
  number: "8000"
  levels:
    - name: "值班员"
      members: ["1001"]
      timeout_s: 15
    - name: "备勤"
      members: ["1003"]
      timeout_s: 20
```

算法（[`call.go`](../internal/sipserver/call.go) 的 `resolveTargets`）：

1. 按 `levels` 数组顺序遍历，数组顺序就是升级顺序；
2. 每级按 `members` 顺序遍历；
3. 跳过主叫自己、离线设备、已经出现过的分机（跨级去重）；
4. 每个目标带上级别下标、级别名、该级 `timeout_s`（默认 20 秒）；
5. 如果一个可用目标都没有，退化为“第一个在线的非主叫设备”，级别 0、名称“默认值班”、20 秒；
6. `bridge` 按目标顺序**串行**尝试：一个失败/超时后再试下一个。

> **当前不是“同振”**：同一级的多个成员也是逐个呼叫，不是同时振铃。
> 没有排班、节假日、并行同振、Webhook/短信通知、可视化策略编辑；全部失败通常回 `480`，
> 如果最后一次尝试是忙则回 `486`。

### 9.3 状态机

```text
  create ──> ringing ──接听──> answered ──通话结束──> pending ──处置──> closed
                │                    │
                │                    └──通话中处置────────────────────────> closed
                │
                └──未接听结束──> unanswered

  升级时仍保持 ringing，只更新 level / to
```

| 状态 | 含义 |
| --- | --- |
| `ringing` | 已创建，正在呼叫某一级；升级时仍保持 `ringing`，只更新 level/to |
| `answered` | 某一级接听；记录 `answered_by`、`answered_at` |
| `pending` | 通话已结束且接听过，等待值班员填写处置结果 |
| `unanswered` | 通话结束但无人接听 |
| `closed` | 已处置关闭；终态，后续通话结束不会覆盖 |

关闭接口：`POST /api/v1/alarms/{id}/close`，请求体 `{ "disposition": "...", "note": "..." }`；
`disposition` 为空时默认 `other`。关闭会发 `alarm.closed` 事件。

### 9.4 推送与弹屏

- 创建：`alarm.created`
- 升级：`alarm.escalated`（level、level_name、to、device_name、zone）
- 接听：`alarm.answered`（answered_by）
- 通话结束：`alarm.updated`（state、result、recording）
- 关闭：`alarm.closed`（完整 Alarm 对象）

控制台收到 `alarm.created` / `alarm.escalated` 后显示红色告警条、播放提示音，并刷新列表。
测试弹屏：`POST /api/v1/alarm/test` 创建一条 `test=true` 的模拟报警，不建立真实通话，也不产生录音。

---

## 10. 广播与实时寻呼

### 10.1 分区模型

分区来自 `config.yaml` 的 `zones`，**启动时构建，运行时不支持增删改**：

| 字段 | 说明 |
| --- | --- |
| `id` | 唯一分区 ID |
| `name` | 显示名，默认等于 ID |
| `dial` | 话机拨此短号直接对该分区实时寻呼，全局唯一 |
| `codec` | `pcmu`（默认）或 `opus`；整个分区统一 |
| `members` | 分机列表；省略表示启动时的全部设备 |
| `mode` | `unicast`（默认）或 `multicast` |
| `multicast` | 组播地址，如 `239.255.10.1:5004`；组播模式必填 |

注意：

- 设备的 `zone` 字段只是展示文本，**不决定广播分区成员**；成员由 `zones[].members` 的 ext 列表决定。
- `members` 省略时取 `sipserver.New` 时刻 store 中的全部设备；之后新增的设备不会自动加入。
- 在线数按分机实时计算，广播只向在线终端发起 INVITE。
- 终端侧必须实现的 `Alert-Info`、SDP、单播/组播行为见[广播终端接入](MCS-广播终端接入.md)。
- 分区配置错误（重复 ID、重复 dial、非法 codec/mode、组播缺地址）会在配置加载阶段报错。

### 10.2 广播会话生命周期

```text
StartBroadcast
  │ 校验 zone/text/media/priority
  │ 选在线终端
  │ 同步准备音频（TTS 合成或文件解码/编码）
  │ 分配 RTP UDP socket
  │ 注册到 broadcastManager（必要时抢占旧会话）
  ▼
starting ── invite 全部终端（并发，默认上限 16）──┐
  │                                              │
  ├─ 没有任何终端应答 ───────────────────────────> failed
  │
  └─ 有终端应答 ──> running ── 20ms 发送帧 ──> finished
                        │                        │
                        ├─ 部分终端失败 ────────> partial
                        ├─ API 停止 ────────────> stopped
                        └─ 被更高优先级顶替 ────> preempted
```

关键行为：

- `StartBroadcast` 会**同步完成 TTS 合成/音频准备**，再返回 `202` 和 `starting` 状态的会话；
  实际终端 INVITE 和放音在后台 goroutine 中执行。
- 每个终端一个 INVITE，带 `Alert-Info` 和 `X-MCS-Event` / `X-MCS-Zone` / `X-MCS-Broadcast-ID` /
  `X-MCS-Priority` / `X-MCS-Codec` / `X-MCS-Source` 头；默认自动应答头为
  `<http://127.0.0.1>;info=alert-autoanswer`。终端需识别该头并自动应答，不能振铃等待人工接听。
- 终端 INVITE 并发受 `broadcast.max_concurrency` 限制（默认 16），整体受 `invite_timeout_s` 限制
  （默认 5 秒，单终端另有 5 秒上限）。
- 终端必须按 SDP 协商的编码应答：PCMU 或 Opus；编码不符会记为失败。
- 会话结束时向所有仍活跃的终端发 BYE，关闭 UDP socket，发 `broadcast.finished`。
- 管理器保留最近 200 个会话用于历史查询；`/broadcast/active` 只返回活动会话。

### 10.3 音频源管线

```text
 文本 ──> TTS HTTP ──> WAV/MP3 ─┐
 音频库 ──> 文件 ───────────────┼──> 解码/重采样 ──> µ-law 160B 或 Opus 960 样点 ──> 20ms ticker ──> RTP
 话机 RTP ──> liveCh ───────────┘
```

| 源 | 处理 |
| --- | --- |
| TTS | `tts.Client.Synthesize` → WAV/MP3；缓存键为 `SHA-256(provider + model + voice + text)` |
| 音频库 | 读取上传文件；支持 PCM16 WAV 或 MP3；解码后重采样 |
| 实时寻呼 | 话机 RTP 经 PCMA→PCMU 与 160 字节分帧后进入 `liveCh`，由广播发送循环取用 |

- PCMU：重采样到 8kHz，编码为 µ-law，每帧 160 字节（20ms），PT 0，时间戳步进 160。
- Opus：重采样到 48kHz，20ms/960 样点编码（32kbps、complexity 5），PT 111，时间戳步进 960。
- 实时寻呼固定使用 PCMU，即使分区配置为 Opus。
- 实时寻呼在无新帧时发送 `0xFF` 静音帧，保持 RTP 节奏；`liveCh` 满时丢帧，优先低延迟。

### 10.4 单播与组播

| 模式 | SDP `c=` / 端口 | 发送行为 | 终端要求 |
| --- | --- | --- | --- |
| `unicast` | MCS 的 `advertise` host + 会话 UDP 端口 | 对每个活跃终端复制一份 RTP | 正常 SIP 自动应答终端 |
| `multicast` | 分区配置的组播地址 + 端口 | 只向组播组发一份 RTP | 终端需加入对应组播组并自动应答 |

- 两种模式都仍然逐终端发 SIP INVITE；组播只改变媒体发送方式。
- 组播代码路径已实现，但未在真实交换机/音箱上验证。
- 没有 RTCP、重传、逐终端补发或丢包统计；`delivered` 表示成功建立媒体腿的数量。

### 10.5 优先级与抢占

| 数值 | 含义 | 典型来源 |
| ---: | --- | --- |
| 0 | 紧急 | 消防/紧急广播 |
| 1 | 高 | 重要通知 |
| 2 | 实时 | 实时寻呼 |
| 3 | 通知（默认） | TTS/音频库/定时广播 |
| 4 | 背景 | 背景音乐 |

- **数值越小优先级越高**。
- 每个分区同一时刻只允许一个活动广播。
- 新会话的优先级 `<=` 当前会话时，会顶替当前会话（同优先级也是后到者生效），旧会话进入 `preempted`。
- 新会话优先级 `>` 当前会话时，`StartBroadcast` 返回 `ErrZoneBusy`，API 映射为 `409`。
- 实时寻呼固定以优先级 2 启动广播会话，因此参与同一套抢占规则。

### 10.6 实时寻呼

```text
话机 1001                  MCS                        分区终端
  │ INVITE 5001             │                            │
  │────────────────────────>│ findZoneByDial             │
  │                         │ StartLiveBroadcast(prio=2) │
  │<── 100 Trying ──────────│                            │
  │                         │ INVITE + Alert-Info ──────>│
  │<── 200 + recvonly SDP ──│                            │
  │════ RTP ═══════════════>│ 读 RTP → PCMA→PCMU → 160B   │
  │                         │ ── 20ms 广播 RTP ─────────>│
  │ BYE                     │                            │
  │────────────────────────>│ BYE ──────────────────────>│
```

实现要点：

- 分区 `dial` 匹配发生在报警/普通呼叫之前，所以寻呼短号优先。
- 话机 INVITE 必须有音频 SDP；MCS 回 `recvonly` 的 PCMU/PCMA SDP。
- MCS 只接收来自**信令源 IP** 的 RTP（不校验端口），按 PT 处理：
  PCMA 解码为 PCM 再编码为 PCMU；PCMU 直接使用；其他 PT 丢弃。
- payload 按 160 字节切块，不足补 `0xFF` 静音，推入 `liveCh`。
- 话机挂机（BYE）、CANCEL、分区广播结束都会停止寻呼。
- 寻呼不录音、不支持 SRTP；每个寻呼会话独立处理，没有跨会话混音。

### 10.7 定时广播

- 存储：`data/broadcast/schedules.json`，原子写（临时文件 + rename）。
- 字段：`id`、`name`、`zone_id`、`text`、`voice`、`media_id`、`priority`、`cron`、`enabled`、
  `last_run`、`next_run`、`created_at`。
- cron 使用标准 5 段（分 时 日 月 周），按**服务器本地时区**解释。
- 调度器每 5 秒检查一次；到期时先推进 `next_run`，再异步 `StartBroadcast`。
- 启动失败只记录日志，不自动重试；下一次按 cron 继续。
- 控制台和 API 支持增删改、启停；定时任务重启后保留。

### 10.8 音频库

- 目录：`data/audio/library/`，索引 `index.json`，音频文件 `<id>.<wav|mp3>`。
- 支持 PCM16 WAV 或 MP3；API 单文件上限 20MB；上传时解码校验并计算时长。
- 列表按创建时间倒序；支持下载、删除；广播时按 `media_id` 读取并走同一套音频管线。
- 索引写失败会回滚内存列表和文件；没有缩略图、转码副本或版权管理。

---

## 11. 对讲增强

### 11.1 WebRTC 视频窗

浏览器观看/对讲的流程：

```text
浏览器                                     MCS
  │ getUserMedia(audio) + addTransceiver(video, recvonly)
  │ createOffer / setLocalDescription / 等 ICE 完成
  │ POST /api/v1/webrtc/offer {call_id, sdp}
  │───────────────────────────────────────>│ 查 call，要求已接听且协商了视频
  │                                         │ 创建 Pion PeerConnection
  │                                         │ AddTrack: H.264 视频、PCMU/PCMA 音频
  │                                         │ SetRemoteDescription(offer) / CreateAnswer
  │<───────────────────────────────────────│ 返回 answer SDP
  │ setRemoteDescription(answer)
  │<══════════ 视频 RTP（A 腿 videoTap）═══│
  │<══════════ 音频 RTP（A 腿 audioTap）═══│
  │══════════ 麦克风 RTP ══════════════════>│ pumpBrowserAudio → 门口机所在腿
```

约束：

- 只对**已接听且协商了视频**的通话开放；纯音频通话返回错误。
- Pion 使用空 `Configuration{}`，没有 STUN/TURN，只有 host candidate；适合同一局域网/本机。
- 浏览器页面必须运行在 `https://` 或 `localhost` 才能使用麦克风。
- 视频固定为 H.264（`packetization-mode=1`）；音频固定为 PCMU/PCMA，避免 Opus 转码。
- 浏览器视频是**只收**；音频是双向。
- 浏览器麦克风的 RTP 会改写为 A 腿 payload type、SSRC 固定 `0x4d4353`，直接写入门口机所在 relay；
  **不做 G.711 转码**。如果 A/B 两腿编码不一致，麦克风回传可能异常。
- **媒体 tap 固定在 A 腿 → MCS 方向**：浏览器看到/听到的是主叫侧音视频。报警柱/门口机主动呼叫值班台时
  符合预期；值班台主动呼叫门口机时，视频窗看到的是值班台侧，麦克风仍回传到门口机所在腿。
  若需要始终看门口机，需要按设备角色选择 tap 方向。
- PeerConnection 进入 failed/closed/disconnected 时移除 tap 并关闭。

### 11.2 远程开锁

- 入口：`POST /api/v1/intercom/{call_id}/unlock`，请求体 `{ "code": "#" }`。
- 只允许已接听的通话；未接听返回 `409`。
- 判定门口机所在腿：主叫分机以 `6` 开头 → A 腿；否则 → B 腿。
- 目标 URI 优先取该腿 Contact；发 in-dialog `INFO`：
  - `Content-Type: application/dtmf-relay`
  - body：`Signal=<code>\r\nDuration=200\r\n`
- 非 200 时回退 `Content-Type: application/dtmf`，body 为裸码。
- 成功后发 `intercom.unlock` 事件；失败返回 `409`。
- 只支持 DTMF INFO；HTTP 开锁、厂商私有指令、门磁状态未实现。

### 11.3 抓拍代理

- 入口：`GET /api/v1/devices/{ext}/snapshot`。
- 设备表里的 `snapshot` 必须是 `http://` 或 `https://` 地址。
- MCS 用 5 秒超时的 HTTP 客户端代理请求，最多读取 5MB；返回 `image/*`，缺失时按 `image/jpeg`。
- 不缓存（`Cache-Control: no-store`），不做图片解码/缩放，不校验返回内容是否真的是图片。
- 快照地址由管理员配置，服务端会访问任意 http(s) 目标，存在 SSRF 面；生产应限制设备网段和 URL 白名单。

---

## 12. 数据与持久化

### 12.1 数据分布

| 数据 | 主存储 | 内存 | 重启后 |
| --- | --- | --- | --- |
| 设备定义（ext/name/password/zone/snapshot） | SQLite `devices` | `store.Store` | 保留 |
| 注册状态（Contact/Source/Expires） | 无 | `store.Store` | **丢失**，终端需重新注册 |
| 话单 CDR | SQLite `cdr` | ring 2000 条 | 保留 |
| 报警事件 | SQLite `alarms`（JSON 列） | ring 1000 条 | 保留 |
| 活动通话 | 无 | `callRegistry` | 丢失，通话中断 |
| 活动广播 | 无 | `broadcastManager` | 丢失 |
| 实时寻呼 | 无 | `paging` map | 丢失 |
| 通话录音 | `data/recordings/` WAV | 无 | 保留 |
| 音频库 | `data/audio/library/` + `index.json` | `mediaLibrary` | 保留 |
| TTS 缓存 | `data/audio/tts/` | 无 | 保留 |
| 定时广播 | `data/broadcast/schedules.json` | `scheduleStore` | 保留 |
| TTS 设置 | SQLite `settings` | `Server.ttsCfg` | 保留 |
| 控制台会话 | 无 | `api.Server.sessions` | **丢失**，需重新登录 |

### 12.2 SQLite

- 驱动：`modernc.org/sqlite`（纯 Go，无 CGO），`database/sql`。
- 连接：`SetMaxOpenConns(1)`，WAL，`busy_timeout=5000`。
- 启动时执行幂等 `CREATE TABLE IF NOT EXISTS`，写入 `schema_meta.schema_version = 1`。
- 没有独立的迁移框架；后续 schema 变更需要手工兼容旧库。

| 表 | 关键列 | 说明 |
| --- | --- | --- |
| `cdr` | `id` PK、`call_id`、`from_ext`、`to_ext`、`device_id`、`start_at`、`answer_at`、`end_at`、`duration_s`、`result`、`recording` | 话单；`start_at` 有索引 |
| `alarms` | `id` PK、`started_at`、`state`、`data` | `data` 是完整 Alarm JSON；`started_at` 有索引 |
| `devices` | `ext` PK、`name`、`password`、`zone`、`snapshot`、`enabled`、`updated_at` | 设备定义；密码明文 |
| `settings` | `key` PK、`value` | 目前用于 `tts` JSON |
| `schema_meta` | `key` PK、`value` | schema 版本 |

> CDR/报警的写库错误当前被忽略（`_ = db.Save...`），通话/事件不会因落库失败而中断。
> 生产应监控数据库写入与磁盘空间；内存 ring 只是最近窗口，不是完整历史。

### 12.3 数据目录

```text
data/
├── mcs.db                         # SQLite：devices / cdr / alarms / settings
├── mcs.db-wal / mcs.db-shm        # WAL 运行时文件
├── recordings/
│   ├── .tmp/                      # 通话中的 a.raw / b.raw
│   └── yyyy/mm/dd/<call-id>.wav   # 双声道录音
├── audio/
│   ├── tts/                       # TTS 缓存（文件名为 SHA-256）
│   └── library/
│       ├── index.json             # 音频库索引
│       └── med-*.wav / *.mp3      # 音频文件
├── broadcast/
│   └── schedules.json             # 定时广播
└── dev-tls/                       # 开发脚本生成的自签证书（生产证书通常在 /etc/mcs）
```

### 12.4 设备与 TTS 的真源规则

- **设备**：`config.yaml devices` 只在 SQLite `devices` 表为空时导入一次。
  之后控制台新增/修改/删除直接写数据库；改 YAML 不会覆盖已有设备。
- **TTS**：启动时以 YAML 为默认值，如果 SQLite `settings["tts"]` 存在则整体覆盖；
  控制台保存后写回 SQLite，立即生效，重启后保留。
- **分区/报警级别/端口**：只来自 YAML，运行时不支持热更新；修改后需重启。

### 12.5 保留、备份与恢复

- 话单和报警在 SQLite 中**没有自动清理/保留策略**，会持续增长；内存 ring 只限制展示窗口。
- 录音、TTS 缓存、音频库**没有自动清理**。
- 备份建议：停止服务后复制 `data/`（至少 `mcs.db`、`recordings/`、`audio/`、`broadcast/`），
  或使用 SQLite 在线备份方式，确保包含 `-wal` / `-shm` 或用 `VACUUM INTO`。
- 恢复：把数据目录放回 `storage.data_dir`，启动后设备/话单/报警/定时任务自动加载。
- 配置和数据库都可能包含明文密码/API Key，备份文件需按敏感数据保护。

---

## 13. 对外接口与事件

### 13.1 REST

- 使用 Go 1.22+ 的 `net/http.ServeMux` 方法路由，共 **36 条 API 路由**，另加 `/recordings/` 和 `/` 静态资源。
- 所有响应 `Content-Type: application/json`（静态文件除外），`Cache-Control: no-store`。
- 列表统一 `{ "items": [...], "total": N }`；单对象直接返回对象。
- 错误统一 `{ "code": "...", "message": "..." }`，例如 `VALIDATION_FAILED`、`NOT_FOUND`、
  `UNAUTHORIZED`、`BROADCAST_FAILED`、`TTS_FAILED`、`SNAPSHOT_FAILED`。

| 分组 | 路径 | 代表接口 |
| --- | --- | --- |
| 登录 | `/api/v1/login`、`/logout`、`/session` | 控制台 Cookie 会话 |
| 设备 | `/api/v1/devices`、`/devices/{ext}`、`/registrations` | 列表、新增、修改、删除、在线状态 |
| 通话 | `/api/v1/calls/active`、`/intercom/{call_id}/unlock` | 活动通话、开锁 |
| WebRTC | `/api/v1/webrtc/offer` | 浏览器 offer/answer |
| 抓拍 | `/api/v1/devices/{ext}/snapshot` | 设备 HTTP 快照代理 |
| 报警 | `/api/v1/alarms`、`/alarms/{id}`、`/alarms/{id}/close`、`/alarm/test` | 列表、详情、处置、测试弹屏 |
| 话单 | `/api/v1/cdr?limit=` | 最近话单（最多 500） |
| 广播 | `/api/v1/broadcast/zones`、`/active`、`/sessions`、`/start`、`/stop` | 分区、活动/历史、TTS/音频广播 |
| 音频库 | `/api/v1/media`、`/media/{id}`、`/media/{id}/download` | 上传、列表、删除、下载 |
| 定时 | `/api/v1/broadcast/schedules`、`/schedules/{id}` | 增删改查、启停 |
| TTS | `/api/v1/tts/config`、`/tts/test` | 读取/保存配置、试合成 |
| 系统 | `/api/v1/healthz`、`/system/info`、`/ws` | 健康检查、版本/运行信息、事件流 |

> `docs/openapi.yaml` 是机器可读规范，但可能滞后；新增/变更接口时以
> [`internal/api/server.go`](../internal/api/server.go) 为准，并同步 OpenAPI。

### 13.2 控制台鉴权

- 由 `auth.console_password` 是否为空决定是否启用；用户名默认 `admin`。
- 启动时对密码做 `bcrypt(SHA-256(password))`，登录时对输入做同样处理再比较；
  用户名用常量时间比较。
- 登录成功生成 32 字节随机 token（hex），存在内存 map，TTL 12 小时。
- Cookie：`mcs_session`，`HttpOnly`、`SameSite=Strict`、`Path=/`；
  直接 TLS 访问时带 `Secure`。反向代理终止 TLS 时可能没有 `Secure`（因为 `r.TLS` 为空）。
- 中间件保护 `/api/` 和 `/recordings/`；以下路径例外：
  `/api/v1/login`、`/logout`、`/session`、`/healthz`。
- 静态控制台页面本身不要求登录，但页面加载后会调用 `/api/v1/session`，未登录时弹出登录框。
- 没有 RBAC、多用户、登录失败锁定、审计日志、CSRF token；CSRF 主要依赖 `SameSite=Strict`。
- 会话只在内存，重启后全部失效。

### 13.3 WebSocket 事件

- 端点：`GET /api/v1/ws`，使用 `coder/websocket`。
- 连接建立后订阅进程内 `eventBus`，只读推送，不接受客户端消息。
- 消息格式：

```json
{
  "type": "alarm.created",
  "ts": "2026-11-19T18:00:00+08:00",
  "data": { }
}
```

| 事件 | 触发 | `data` 主要内容 |
| --- | --- | --- |
| `device.registered` | 注册成功 | ext、contact、source、model、firmware |
| `device.unregistered` | 注销 | ext、source |
| `call.incoming` | 收到 INVITE | CallView（call_id、from、to、state、started_at） |
| `call.answered` | B 腿接听 | call_id、from、to、ts |
| `call.ended` | 通话结束 | 完整 CDR |
| `alarm.created` | 报警事件创建 | alarm_id、call_id、from、to、alarm_number、device_name、zone、level |
| `alarm.escalated` | 升级到下一目标 | alarm_id、call_id、level、level_name、to、device_name、zone |
| `alarm.answered` | 报警被接听 | alarm_id、call_id、answered_by、ts |
| `alarm.updated` | 报警通话结束 | alarm_id、call_id、state、result、recording |
| `alarm.closed` | 处置关闭 | 完整 Alarm |
| `broadcast.started` | 终端 INVITE 完成且有活跃终端 | BroadcastView |
| `broadcast.finished` | 广播结束/失败/停止/抢占 | BroadcastView |
| `intercom.unlock` | 开锁 INFO 发送成功 | call_id、to、code、result |

事件总线是**尽力而为**：

- 每个订阅者一个 64 缓冲的 channel；
- 发布时非阻塞写入，满了直接丢弃该事件；
- 没有持久化、没有重放、没有 ACK；
- 控制台只用 WS 处理报警弹屏，其余状态靠每 2 秒的 REST 轮询兜底。

### 13.4 服务端门面

`api` 通过 `sipserver.Server` 的公开方法操作业务，主要分为：

| 能力 | 方法 |
| --- | --- |
| 事件 | `Subscribe` |
| 通话 | `ActiveCalls`、`UnlockCall`、`HandleWebRTCOffer` |
| 报警 | `CloseAlarm`、`EmitTestAlarm` |
| 广播 | `BroadcastZones`、`StartBroadcast`、`StopBroadcast`、`BroadcastSessions` |
| 音频库 | `ListMedia`、`AddMedia`、`DeleteMedia`、`MediaData` |
| 定时 | `ListSchedules`、`AddSchedule`、`UpdateSchedule`、`DeleteSchedule` |
| TTS | `TTSConfig`、`SetTTS`、`TTSClient`、`MediaTranscode` |
| 生命周期 | `Serve`、`ServeTLS`、`StartScheduler`、`Close` |

设备、话单、报警的读取由 `api` 直接访问 `store`，没有再包一层 service。

---

## 14. Web 控制台

控制台是 [`internal/webui/index.html`](../internal/webui/index.html)，约 53KB 的单文件，通过 `go:embed` 打进二进制：

- 原生 HTML/CSS/JS，无框架、无构建步骤、无外部 CDN；
- 登录弹窗 + 左侧导航 + 顶部状态栏 + 内容区；
- 窄屏自动切为顶部导航；
- 页面按 hash 切换，刷新后保持当前 tab。

| 页面 | 内容 |
| --- | --- |
| 总览 | 设备在线数、活动通话、报警数、快捷操作、运行状态 |
| 报警 | 报警事件列表、处置 |
| 通话与对讲 | 活动通话、视频窗、开锁、抓拍、最近话单/录音 |
| 广播 | 分区选择、TTS/音频库广播、活动广播、停止 |
| 设备管理 | 新增/删除设备、在线状态、型号/固件、来源地址 |
| 音频库 | 上传 WAV/MP3、列表、删除、按库广播 |
| 定时任务 | cron 新增、启停、删除 |
| TTS 设置 | provider/base_url/model/voice、测试合成 |
| 系统信息 | 版本、运行时长、设备数、分区、接口清单 |

数据刷新策略：

- 启动后 `setInterval(refresh, 2000)`，每 2 秒并行拉取设备、活动通话、话单、报警、活动广播；
- WebSocket 连接 `/api/v1/ws`，断开后 3 秒重连；
- 收到 `alarm.created` / `alarm.escalated` 时显示告警条并播放提示音，然后触发一次刷新；
- 其他事件不直接驱动 UI，靠轮询保证最终一致；
- 视频窗按需调用 `/api/v1/webrtc/offer`，关闭时停止麦克风轨道。

取舍：单文件让部署极简，也意味着没有组件化、路由库、状态管理和前端测试；后续若要复杂化，再引入构建链。

---

## 15. 并发、可靠性与可观测性

### 15.1 并发单元

| 组件 | 并发模型 | 同步/注意事项 |
| --- | --- | --- |
| HTTP/HTTPS | `net/http` 每连接 goroutine，handler 并发 | `api.Server.sessions` 用 `sessMu` 保护 |
| SIP 监听 | sipgo 监听器 + 每个请求一个 handler | `handleInvite` 内阻塞执行整通桥接 |
| 通话 | 一个桥接 goroutine（SIP handler）+ 挂断/取消产生的短 goroutine | `stateMu` 保护共享字段，`once` 保证 `finish` 一次，`ctx` 取消 |
| RTP relay | 每个 relay 一个读循环；视频通话最多 4 个 | `peer` 原子指针、`rxRTP/txRTP` 原子计数、SRTP 发送用 `txMu` |
| 录音 | 由 relay 读循环回调写入 | `recorder.mu` 串行化写；收尾在 `finish` 中执行 |
| 广播 | 每个会话一个 `run` goroutine + 受信号量限制的终端 INVITE goroutine | 会话状态用 `mu`；管理器用 `mu` |
| 实时寻呼 | 每个寻呼一个读 RTP goroutine | `pagingMu` 保护 map |
| 定时广播 | 一个 5 秒 ticker goroutine | `scheduleStore.mu` |
| 事件总线 | 发布者在业务 goroutine 中同步发布 | `mu` 复制订阅者列表；订阅 channel 非阻塞写 |
| WebSocket | 每个连接一个写循环 | 读侧不消费业务消息 |
| SQLite | 单连接，调用方 goroutine 直接读写 | `MaxOpenConns(1)` 串行化写 |

### 15.2 状态与锁规则

- `call.stateMu` 是 `RWMutex`：短临界区只保护字段读写，不在持锁时做网络 I/O。
- `call.finish` 用 `sync.Once`，保证录音/话单/报警更新只做一次。
- `call.ctx` 是取消信号：CANCEL、挂断/结束、清理失败尝试等路径会触发；桥接循环和 B 腿等待都监听它。
- `broadcastSession.mu` 保护状态、端点列表和计数；发送循环读取端点时短暂加锁复制地址。
- `eventBus.publish` 先复制订阅者列表再发送，避免持锁写 channel。
- `store.Store` 用 `RWMutex` 保护设备 map；注册/注销/查询都在锁内完成，持久化写库在设备 upsert 中同步执行。
- `callRegistry` 用 `RWMutex`；列表按指针去重，避免 A/B 两个 Call-ID 重复返回同一通呼叫。

### 15.3 失败隔离

- 单通呼叫失败只影响该 call：`cleanupAttempt` 关闭本次 relay/recorder，继续下一目标或结束。
- 单个广播终端 INVITE/应答失败只记录该端点错误，不影响其他端点；全部失败会话为 `failed`，部分失败为 `partial`。
- TTS 失败会让 `StartBroadcast` 返回错误（HTTP 502），不会创建会话。
- SQLite 写失败不阻断通话；但错误被忽略，需要外部监控。
- 事件总线满时丢弃事件，不阻塞业务；控制台轮询兜底。
- SIP/HTTP 监听器异常会取消根 context，进程退出；没有自动重启（交给 systemd/Windows 服务）。
- 没有全局 panic recover；某个 goroutine panic 会导致进程退出，依赖服务管理器重启。

### 15.4 质量属性

| 属性 | 现状 | 说明 |
| --- | --- | --- |
| 性能 | relay-first，正常通话不解码；广播 20ms 定时发送 | 未做压测/容量基准；CPU 主要在录音解码、TTS/音频编码、SRTP |
| 扩展性 | 单进程纵向扩展 | 无集群、无共享状态；SQLite 单写者；呼叫数/媒体端口无硬上限 |
| 可用性 | 服务管理器自动重启 | 重启丢失注册/活动通话；无 HA/主备 |
| 可维护性 | 模块化单体 + 38 个测试 | `sipserver` 较大；接口以门面方法为主 |
| 可观测性 | slog 文本日志、HTTP 请求日志、RTP 计数、WS 事件 | 无 Prometheus/OpenTelemetry/pprof/pcap/诊断包 |
| 安全 | Digest 注册、可选 TLS、SDES-SRTP、控制台会话 | INVITE 无鉴权、单管理员、无审计，见 §17 |

### 15.5 可观测性现状

- 日志：`slog` 文本格式，输出 stdout，可选 `-log` 文件；HTTP 请求日志走标准库 `log`（stderr）。
- 关键日志字段：`call_id`、`from`、`to`、`ext`、`source`、`level`、`zone`、`broadcast id`、`error`。
- 通话结束时打印 `a_rx`、`a_tx`、`b_rx`、`b_tx` 的 RTP 包计数。
- `GET /api/v1/healthz` 只返回 `{"status":"ok"}`。
- `GET /api/v1/system/info` 返回版本、uptime、设备数、在线数、是否转码。
- 没有指标端点、链路追踪、pprof、抓包或一键诊断包；现场排障主要靠日志和 `tcpdump`。

---

## 16. 配置、构建与部署

### 16.1 配置分区

`config.yaml` 在启动时读取一次；除 TTS 外不支持热更新。

| 段 | 内容 | 运行时可改 |
| --- | --- | --- |
| `server` | SIP/HTTP/HTTPS 地址、域名、`advertise`、TLS 证书 | 否 |
| `storage` | `data_dir` | 否 |
| `auth` | SIP realm/secret、控制台用户名/密码 | 否 |
| `alarm` | 报警短号、分级值班与超时 | 否 |
| `media` | `transcode` 开关 | 否 |
| `broadcast` | 自动应答头、INVITE 超时、并发上限 | 否 |
| `tts` | provider、base_url、api_key、model、音色、超时 | **是**（控制台写 DB，立即生效） |
| `zones` | 分区、成员、编码、单播/组播、寻呼短号 | 否 |
| `devices` | 初始设备；仅首次导入 | 设备本身可通过控制台改 |

完整字段和默认值见[配置参考](MCS-配置参考.md)与 [`config.example.yaml`](../config.example.yaml)。

### 16.2 构建

| 目标 | 产物/行为 |
| --- | --- |
| `make build` | `bin/mcs`，版本由 `-ldflags -X main.version` 注入 |
| `make uasim` | `bin/uasim` 开发模拟器 |
| `make test` | `go test ./...` |
| `make run` | 构建并以 `config.yaml` 启动 |
| `make run-https` | 自签证书 + HTTPS 开发实例 |
| `make run-lan` | 探测 LAN IP、独立 `data-lan`、绑定 0.0.0.0 |
| `make dev-cert` | 生成 `data/dev-tls` 自签证书 |
| `make linux` | Linux amd64/arm64 |
| `make windows` | Windows amd64 |
| `make darwin` | macOS arm64/amd64 |
| `make dist` | 一次产出全部平台 |
| `make package` | Linux tar 包（二进制 + 示例配置 + systemd/安装脚本） |
| `make fmt` / `make vet` | 格式化 / 静态检查 |

- 跨平台目标均使用 `CGO_ENABLED=0` 交叉编译；SQLite 使用纯 Go 实现，因此不依赖目标机 libc。
- 前端已 `go:embed`，构建不需要 Node。
- 单文件约 24–25MB（Pion + SQLite + Opus/MP3 解码器）。

### 16.3 部署

| 平台 | 方式 | 关键路径 |
| --- | --- | --- |
| Linux | systemd | `/opt/mcs/mcs`、`/etc/mcs/config.yaml`、`/var/lib/mcs/data` |
| Windows | 原生服务 `MCS` + Inno Setup | `C:\Program Files\MCS\mcs.exe`、`C:\ProgramData\MCS\data`、`mcs.log` |
| macOS | 直接运行/开发脚本 | 无正式安装包 |

Linux 单元做了基本加固：`NoNewPrivileges`、`ProtectSystem=strict`、`ProtectHome`、`PrivateTmp`、
`ReadWritePaths=/var/lib/mcs`、仅保留 `CAP_NET_BIND_SERVICE`。Windows 安装脚本会创建服务、写配置、
放行 5060/5061/8080/8443 并给 `mcs.exe` 加动态 RTP UDP 入站规则。

详细步骤、证书和 Windows 排查见[部署手册](../deploy/README.md)与
[Windows 安装说明](../deploy/README-Windows.md)。

### 16.4 TLS / HTTPS / SRTP 配置

| 目标 | 配置 |
| --- | --- |
| SIP over TLS | `server.sip_tls` + `server.tls_cert` + `server.tls_key`；可在网页“系统设置”修改 |
| HTTPS 控制台 | `server.https` + `server.https_cert`/`https_key`（可复用 SIP 证书） |
| SRTP | 终端侧启用 `RTP/SAVP` + `a=crypto`；MCS 自动协商，无需额外开关 |
| WebRTC | 需要 HTTPS 或 `localhost` 才能用麦克风；Pion 自动 DTLS-SRTP |

0.11.0 起：
- B 腿会保留注册 Contact/REGISTER transport 的 `transport=tls`，出站 INVITE 使用 TLS；
- `tls_ca` 用于信任终端私有 CA，`tls_insecure` 仅用于内网自签测试；
- 网页可配置 `srtp_mode`：`auto` / `required` / `off`；
- `required` 只作用于点对点/报警呼叫，广播、寻呼、会议仍是明文 RTP。

> SRTP 的 SDES 密钥在 SDP 中；如果 SIP 信令是明文 UDP，密钥也会明文传输。
> 生产要求 SRTP 时必须同时启用 SIP over TLS。

---

## 17. 安全模型

### 17.1 信任边界

| 边界 | 认证/加密 | 当前现状与风险 |
| --- | --- | --- |
| SIP REGISTER | Digest（MD5 挑战，支持 SHA-256 校验） | 较好；但密码在配置/DB 中明文 |
| SIP INVITE / BYE / CANCEL / INFO | **无 Digest** | 能访问 5060 的主机可伪造呼叫、挂断、寻呼、广播；生产必须靠网络 ACL/SBC |
| SIP 信令 | 可选 TLS 1.2+ | 默认 UDP 明文；SRTP 密钥随 SDP 暴露 |
| RTP/SRTP | SDES-SRTP 可选，按腿独立密钥 | 媒体源地址可被后续包覆盖（latching），无源认证；广播/寻呼明文 |
| 控制台页面 | 无（静态页） | 页面本身可加载，但 API 受会话保护 |
| 控制台 API | 可选 Cookie 会话（bcrypt + 12h token） | 默认 HTTP、默认只绑 localhost；无 RBAC/审计/锁定 |
| WebSocket | 复用控制台会话 | 同 API；事件可能包含设备/通话信息 |
| SQLite/配置文件 | 文件权限 | 设备密码、TTS API Key、控制台密码（配置中）明文 |
| 出站抓拍 | 无 | 管理员可配置任意 http(s) URL，存在 SSRF 面 |
| 出站 TTS | API Key/HTTPS（取决于供应商） | API Key 存 SQLite settings，明文 |

### 17.2 生产加固清单

1. **设置 `auth.console_password`**，不要把控制台裸露在公网；默认只绑 `127.0.0.1`。
2. **设置固定、随机的 `auth.secret`**；不要留空（留空会每次启动随机，nonce 重启失效）。
3. **启用 SIP TLS**，尤其是使用 SRTP 时；证书用正式 CA 或让终端信任自签证书。
4. **限制 SIP 5060 的来源**：只允许终端网段；或在前面放 SBC/防火墙做 INVITE 鉴权。
5. **修改默认设备密码**；限制 `mcs.db` 与备份文件的文件权限。
6. **不要直接暴露动态 RTP 端口到公网**；现场网络应使用私网/VLAN。
7. **给控制台加 HTTPS**（内置或反向代理），并在代理层限制访问来源。
8. **定期备份** `config.yaml` 与 `data/`；备份文件按敏感数据管理。
9. **限制抓拍 URL** 到设备网段，避免把 MCS 当作任意内网 HTTP 代理。
10. 关注[§18](#18-已知边界与演进方向)中的 INVITE 鉴权、RBAC、固定 RTP 端口等后续项。

### 17.3 明确没有的能力

- 没有多用户/RBAC/角色权限、操作审计、登录失败锁定、密码轮换。
- 没有 SIP 呼叫权限策略（黑白名单、时段、呼出路由）。
- 没有 TLS 客户端证书、mTLS、SIPS 强制。
- 没有对广播/寻呼/报警的端到端鉴权。
- 没有敏感配置加密（KMS/Secret Manager）。

---

## 18. 已知边界与演进方向

### 18.1 当前边界

| 领域 | 当前边界 | 影响/建议 |
| --- | --- | --- |
| SIP 呼叫鉴权 | INVITE/BYE/CANCEL 不做 Digest | 内网可信部署；公网必须加 SBC/ACL，后续补鉴权 |
| 注册持久化 | 注册状态只在内存 | 重启后终端需重新注册；可考虑持久化/快速重注册 |
| RTP 端口 | 动态端口，无固定段 | 防火墙难收敛；后续加可配置端口池 |
| NAT/公网 | 无 ICE/TURN/STUN（SIP），WebRTC 也只有 host candidate | 跨公网/复杂 NAT 不可用；需 TURN 和媒体公网地址 |
| 编码 | G.711 为基线，可选 A/U 转码；无 G.722/G.729；SIP Opus 不转码 | 混合厂商可能单通；广播 Opus 仅终端方向 |
| SRTP | 仅 SDES；SIP 终端无 DTLS-SRTP | 需要 DTLS-SRTP 的终端无法接入；广播/寻呼明文 |
| 报警策略 | 顺序升级、无排班/并行同振/Webhook | 值班体验有限；后续做策略引擎 |
| 广播 | 组播未真机验证；无 RTCP/重传/逐终端重试 | 现场需验证交换机与音箱；失败终端只记录 |
| 对讲 | 浏览器视频只收；无 TURN、无视频录像、无视频回传 | 远程调度受限；后续补 TURN/录像 |
| 容量 | 无压测/硬上限；SQLite 单写者 | 大并发需评估；后续加指标和容量测试 |
| 运维 | 无 metrics/tracing/pprof/pcap/诊断包 | 排障靠日志；后续补可观测性 |
| 数据保留 | 话单/报警/录音无自动清理 | 磁盘会增长；后续加保留策略 |
| Windows | 服务代码与安装脚本已实现，实机覆盖有限；安装包未签名 | 首次部署在目标机复核；生产建议签名 |
| 前端 | 单文件、无组件化/前端测试 | 功能增长后维护成本上升 |

### 18.2 规划稿与当前实现的差异

| 《技术方案》中的规划 | 当前实现 |
| --- | --- |
| `chi` HTTP 框架 | `net/http` 标准 `ServeMux` |
| `zerolog` JSON 日志 | `log/slog` 文本日志 + 标准库 `log` |
| Vue 3 + Element Plus + ECharts | 单文件原生 HTML/CSS/JS，`go:embed` |
| `internal/transport`、`sipcore`、`registrar`、`call`、`media` 等细分包 | 集中在 `internal/sipserver`，按文件分模块 |
| 固定 RTP 端口池 `10000-20000` | 动态端口（`0.0.0.0:0`） |
| SIP 终端 DTLS-SRTP | 仅 SDES-SRTP；WebRTC 由 Pion 做 DTLS-SRTP |
| 多用户/RBAC/审计/封禁 | 单管理员 + 内存会话 |
| Prometheus、pprof、pcap、诊断包 | 未实现 |
| 报警并行同振/排班/Webhook | 顺序升级，无并行/排班/Webhook |
| 视频录像/回传/TURN | 未实现；浏览器只收视频 |
| 设备型号模板/自动配置 | 未实现；设备字段由控制台/配置维护 |

> 这些差异不是“文档错误”，而是 M1 范围收敛的结果。规划稿保留为后续路线图。

### 18.3 建议的演进顺序

1. **呼叫鉴权与网络边界**：INVITE/BYE 的 Digest 或 ACL、控制台 HTTPS、默认密码治理。
2. **可运维性**：Prometheus 指标、结构化日志、pprof、固定 RTP 端口段、诊断包。
3. **报警策略**：并行同振、值班表/节假日、Webhook/短信、可视化管理。
4. **媒体能力**：DTLS-SRTP、G.722/Opus 策略、TURN、视频录像/回传。
5. **多用户与审计**：RBAC、登录失败锁定、操作审计、配置加密。
6. **容量与高可用**：压测基线、SQLite 写入优化、主备/集群（若业务需要）。
7. **代码结构**：当 `sipserver` 继续增长时，按 call/media/broadcast 拆包，保持当前门面接口。

---

## 19. 附录

### 19.1 术语

| 术语 | 含义 |
| --- | --- |
| AOR | Address of Record，SIP 用户标识（这里通常就是分机号） |
| UAC / UAS | SIP 客户端 / 服务端 |
| B2BUA | Back-to-Back User Agent，MCS 分别与主叫、被叫建立对话 |
| A 腿 / B 腿 | 主叫→MCS / MCS→被叫 |
| CDR | Call Detail Record，话单 |
| SDES | SDP 中通过 `a=crypto` 携带 SRTP 密钥的方式 |
| SRTP / SRTCP | 安全 RTP / 安全 RTCP |
| DTLS-SRTP | 用 DTLS 握手协商 SRTP 密钥（WebRTC 使用） |
| ICE / STUN / TURN | WebRTC 的连通性协商 / 地址发现 / 中继 |
| Latching | 收到媒体包后用实际源地址覆盖 SDP 对端地址 |
| Zone | 广播分区 |
| Live paging | 话机拨分区短号，实时对分区讲话 |
| Alert-Info | SIP 头，广播 INVITE 用它触发终端自动应答 |

### 19.2 关键文件索引

| 文件 | 说明 |
| --- | --- |
| [`cmd/mcs/main.go`](../cmd/mcs/main.go) | 启动、监听、优雅关闭 |
| [`internal/sipserver/server.go`](../internal/sipserver/server.go) | SIP 栈、注册、配置、门面方法 |
| [`internal/sipserver/call.go`](../internal/sipserver/call.go) | B2BUA、报警升级、开锁 |
| [`internal/sipserver/media.go`](../internal/sipserver/media.go) | RTP relay、录音、G.711 |
| [`internal/sipserver/broadcast.go`](../internal/sipserver/broadcast.go) | 广播会话、扇出、抢占 |
| [`internal/sipserver/paging.go`](../internal/sipserver/paging.go) | 实时寻呼 |
| [`internal/sipserver/webrtc.go`](../internal/sipserver/webrtc.go) | WebRTC 视频/音频桥 |
| [`internal/api/server.go`](../internal/api/server.go) | REST/WS 路由 |
| [`internal/store/sqlite.go`](../internal/store/sqlite.go) | SQLite schema 与读写 |
| [`internal/webui/index.html`](../internal/webui/index.html) | 控制台单页 |
| [`config.example.yaml`](../config.example.yaml) | 完整配置示例 |

### 19.3 测试与验证

- 运行：`go test ./...`；并发相关包可用 `go test -race ./internal/sipserver ./internal/api ./internal/store ./internal/tts`。
- 覆盖：Digest、注册/注销、B2BUA 呼叫/录音/升级、广播扇出/抢占、实时寻呼、视频中继、WebRTC 音视频、SRTP、SDP 改写、定时任务、SQLite、控制台登录/设备 CRUD/抓拍、TTS 客户端。
- 真实 Kokoro 端到端测试需要 `MCS_REAL_TTS=1` 和本机 TTS 服务。
- 未在真实报警柱/门口机/SIP 音箱、真实组播网络、生产 Windows/Linux 主机上做完整验收；
  详细边界见[测试与兼容性](MCS-测试与兼容性.md)。

### 19.4 文档版本

| 版本 | 日期 | 说明 |
| --- | --- | --- |
| v1.0 | 2026-11 | 首版：按当前代码梳理部署、分层、呼叫/媒体/报警/广播/寻呼/数据/接口/安全与边界 |

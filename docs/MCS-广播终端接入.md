# MCS 广播终端接入规范

本文面向自研 SIP 广播音箱/终端，说明 MCS 广播的协议流程和终端必须实现的行为。

## 1. 总体原理

MCS 广播不是让终端之间互相通话，而是：

```text
TTS / WAV → 8k PCMU 20ms 帧 → MCS RTP
                                  │
                    ┌─────────────┴─────────────┐
                    │ 单播：逐终端 RTP            │
                    │ 组播：一个 RTP 流到组播组   │
                    └─────────────┬─────────────┘
                                  │
                          SIP INVITE + Alert-Info
                                  │
                         终端自动应答并播放
```

1. MCS 读取分区 `zones` 的成员，只选择**在线/已注册**终端；
2. 对每个终端发送带 `Alert-Info` 的 INVITE；
3. 终端根据 `Alert-Info` 自动应答；
4. MCS 按 SDP 把 PCMU RTP 发给终端（单播）或组播组；
5. 播放结束/停止/抢占时 MCS 发送 BYE。

## 2. 注册要求

终端先按普通 SIP 分机注册：

```text
SIP 服务器：<MCS LAN IP>:5060（UDP）
账号：1004
密码：pass-1004
```

MCS 只会向 `online=true` 的成员发起广播 INVITE。

## 3. 广播 INVITE

MCS 发出的 INVITE 关键字段：

```text
INVITE sip:1004@mcs.local SIP/2.0
From: <sip:broadcast@mcs.local>;tag=...
To: <sip:1004@mcs.local>
Contact: <sip:mcs@<MCS IP>:5060>
Alert-Info: <http://127.0.0.1>;info=alert-autoanswer
X-MCS-Event: broadcast
X-MCS-Zone: a
X-MCS-Broadcast-ID: bcs-...
X-MCS-Priority: 3
X-MCS-Codec: pcmu
X-MCS-Source: tts
Content-Type: application/sdp
```

| 头 | 说明 |
| --- | --- |
| `Alert-Info` | 自动应答触发头，默认含 `info=alert-autoanswer` |
| `X-MCS-Event` | 固定 `broadcast`，设备可据此识别广播 |
| `X-MCS-Zone` | 分区 ID |
| `X-MCS-Broadcast-ID` | 本次广播会话 ID |
| `X-MCS-Priority` | 0 紧急 / 1 高 / 2 实时 / 3 通知 / 4 背景 |
| `X-MCS-Codec` | `pcmu` 或 `opus`，与 SDP 一致 |
| `X-MCS-Source` | `tts` / `media` / `live` |

设备固件建议同时判断 `X-MCS-Event=broadcast` **或** `Alert-Info` 中的
`alert-autoanswer`，避免只依赖其中一个头。

`Alert-Info` 默认值来自 `broadcast.auto_answer`，可配置。  
**终端必须识别 `info=alert-autoanswer` 并自动应答**，不能振铃等待人工接听。

## 3.1 设备端处理流程（伪代码）

```text
on INVITE:
    if X-MCS-Event == "broadcast" or Alert-Info contains "alert-autoanswer":
        codec = X-MCS-Codec or parse SDP (pcmu/opus)
        if codec not supported: 488
        send 200 OK with recvonly SDP (real IP + port, codec)
        if SDP c= is multicast:
            join multicast group/port
        start RTP receiver
    else:
        normal call handling

on BYE:
    stop playback, leave multicast, release RTP port
```

MCS 是 `sendonly`，设备不需要向 MCS 发送广播 RTP。

## 4. MCS 的 SDP（单播）

```text
v=0
o=- 1 1 IN IP4 <MCS IP>
s=MCS Broadcast
c=IN IP4 <MCS IP>
t=0 0
m=audio <MCS RTP 端口> RTP/AVP 0
a=rtpmap:0 PCMU/8000
a=sendonly
```

终端 200 OK 的 SDP 至少包含：

```text
c=IN IP4 <终端 IP>
m=audio <终端接收端口> RTP/AVP 0
a=rtpmap:0 PCMU/8000
a=recvonly
```

MCS 会：

- 按终端 200 OK 的 `c=` + `m=audio` 端口发送 RTP；
- 不要求终端发送 RTP；
- 不支持 PCMA/G.722/Opus 广播，**必须是 PCMU（G.711 µ-law）**；
- 音频为 8kHz、20ms/包、160 字节 payload。

## 5. 组播模式

分区可以在网页 **业务 → 广播 → 广播分区管理** 中配置，保存后立即生效；也可以在 `config.yaml` 中作为首次初始值。

网页配置字段与下面 YAML 等价：

```yaml
zones:
  - id: "all"
    name: "全区"
    members: ["1004"]
    mode: "multicast"
    multicast: "239.255.10.1:5004"
```

MCS 的 SDP `c=`/`m=` 指向组播地址和端口，并只发送一份 RTP。

终端要求：

- 收到广播 INVITE 后仍要自动应答；
- 加入 SDP 中的组播组和端口；
- 关闭组播回环/IGMP 问题；
- 交换机开启 IGMP snooping，否则组播可能泛洪或终端收不到；
- 终端不需要向组播组发送 RTP。

> 组播路径代码已实现，但尚未在真实交换机和真实音箱上验证。建议先测单播，再测组播。

## 5.1 Opus 编码

分区可配置：

```yaml
zones:
  - id: "speaker"
    name: "Opus 音箱"
    members: ["1004"]
    mode: "unicast"
    codec: "opus"
```

MCS 的 SDP 变为：

```text
m=audio <MCS RTP 端口> RTP/AVP 111
a=rtpmap:111 opus/48000/2
a=fmtp:111 minptime=10;useinbandfec=1
a=sendonly
```

终端要求：

- 支持 **Opus 48kHz**，20ms 帧（960 samples）；
- 200 OK 的 SDP 声明 Opus；
- 能解码 MCS 发来的 Opus RTP；
- 整个分区统一使用 Opus，不能混用只支持 PCMU 的终端。

PCMU 仍是默认编码；Opus 只对显式 `codec: "opus"` 的分区生效。音源（TTS/WAV/MP3）会先解码为 PCM，再编码为 Opus。

## 6. 服务端配置

```yaml
broadcast:
  auto_answer: "<http://127.0.0.1>;info=alert-autoanswer"
  invite_timeout_s: 5
  max_concurrency: 16

zones:
  - id: "speaker"
    name: "测试音箱"
    members: ["1004"]
    mode: "unicast"
```

测试音箱建议单独建一个只包含 1004 的分区，避免广播时把 1001/1002/1003 全部呼起来。

添加/修改 `zones` 后需要重启 MCS。

## 6.1 编码选择

- `X-MCS-Codec: pcmu` / SDP `PCMU/8000`：默认；
- `X-MCS-Codec: opus` / SDP `opus/48000/2`：分区配置 `codec: "opus"`；
- 设备必须按 SDP 协商结果解码，不能固定只解 PCMU；
- 整个分区统一编码，不能在一个分区里混用只支持 PCMU 和只支持 Opus 的终端。

## 7. TTS 与音频库

- TTS：MCS 调 `tts.base_url`（默认 `http://127.0.0.1:8399`），合成 WAV 后重采样 8k 并编码 PCMU；
- 音频库：上传 PCM16 WAV 或 MP3（≤20MB），MCS 解码后按分区编码为 PCMU 或 Opus；
- 如果 TTS 服务没启动，页面“TTS 广播”会 502；此时可先用音频库“播放音频”。

## 8. 终端侧自测清单

- [ ] 注册成功，控制台显示在线；
- [ ] 收到带 `Alert-Info` 的 INVITE 后自动 200，不响铃；
- [ ] 200 OK 的 `c=` 是终端真实 IP，`m=audio` 是真实接收端口；
- [ ] 按分区 `codec` 协商 PCMU（PT 0，8k/20ms）或 Opus（PT 111，48k/20ms）；
- [ ] 单播能听到 TTS/音频；
- [ ] 组播能加入 239.255.10.1:5004 并播放；
- [ ] 收到 BYE 后停止播放；
- [ ] 广播期间再次收到高优先级广播时能处理抢占（MCS 会 BYE 旧会话）。

## 9. 常见失败

| 现象 | 原因 |
| --- | --- |
| 终端振铃不自动接 | 没识别 `Alert-Info`，或 `info` 值不匹配 |
| 接通但无声 | 终端协商的不是 PCMU，或 200 OK 的端口/`c=` 不对 |
| 只有部分终端有声 | 只处理了第一路/单播地址错误 |
| 组播无声 | 终端没入组、交换机 IGMP、组播地址不一致 |
| TTS 广播 502 | 8399 TTS 服务未启动 |
| 广播把值班分机也呼起来 | 分区 members 配置过大，建议单独测试分区 |

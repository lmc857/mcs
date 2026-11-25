# MCS 配置参考

配置文件为 YAML，默认 `config.yaml`。未填写的字段使用下列默认值；`config.example.yaml` 可直接复制。

## server

| 字段 | 默认 | 说明 |
| --- | --- | --- |
| `sip_udp` | `0.0.0.0:5060` | SIP UDP 监听 |
| `sip_tls` | 空 | 可选 SIP over TLS 监听，如 `0.0.0.0:5061` |
| `tls_cert` | 空 | SIP TLS 证书 PEM；`sip_tls` 非空时必填 |
| `tls_key` | 空 | SIP TLS 私钥 PEM |
| `tls_ca` | 空 | 可选，信任的终端 CA PEM；用于 MCS 出站 TLS 校验 |
| `tls_insecure` | `false` | 仅内网测试：跳过终端 SIP TLS 证书校验 |
| `srtp_mode` | `auto` | `auto` / `required` / `off`；required 仅作用于点对点/报警呼叫 |
| `http` | `127.0.0.1:8080` | 控制台 HTTP |
| `https` | 空 | 可选控制台 HTTPS，如 `0.0.0.0:8443` |
| `https_cert` | 空 | 控制台证书；空则复用 `tls_cert` |
| `https_key` | 空 | 控制台私钥；空则复用 `tls_key` |

以上 TLS/HTTPS/SRTP 字段均可在网页“系统设置”中修改，保存后重启生效。
| `domain` | `mcs.local` | SIP 域/Realm 展示 |
| `advertise` | `127.0.0.1:5060` | 写进 Contact/SDP 的设备可达地址；真机必须改成服务器 LAN IP |

> `advertise` 的端口要和终端实际访问的 SIP 端口一致。只启用 TLS 时通常应写 `host:5061`。

## webrtc

| 字段 | 默认 | 说明 |
| --- | --- | --- |
| `ice_servers` | 空 | STUN/TURN 列表 |
| `ice_servers[].urls` | 空 | 例如 `stun:stun.example.com:3478`、`turn:turn.example.com:3478?transport=udp` |
| `ice_servers[].username` | 空 | TURN 用户名 |
| `ice_servers[].credential` | 空 | TURN 密码 |
| `public_ip` | 空 | MCS 公网 IP；服务器在 NAT 后时发布 host candidate 使用 |

```yaml
webrtc:
  ice_servers:
    - urls: ["stun:stun.example.com:3478"]
    - urls: ["turn:turn.example.com:3478?transport=udp"]
      username: "mcs"
      credential: "secret"
  public_ip: "203.0.113.10"
```

这些字段也可以在网页“系统设置”修改：

- STUN 地址；
- TURN 地址、用户名、密码；
- WebRTC 公网 IP。

TURN 需要使用标准 coturn/Pion TURN 服务；MCS 不内置 TURN 服务。

## storage

| 字段 | 默认 | 说明 |
| --- | --- | --- |
| `data_dir` | `./data` | 数据目录：`mcs.db`、录音、音频库、证书、定时任务 |

目录结构：

```text
data/
├── mcs.db                 # SQLite：devices / cdr / alarms
├── recordings/            # 通话录音 WAV
├── audio/tts/             # TTS 缓存
├── audio/library/         # 音频库 + index.json
└── broadcast/schedules.json
```

## auth

| 字段 | 默认 | 说明 |
| --- | --- | --- |
| `realm` | `mcs` | SIP Digest Realm |
| `secret` | 空 | Digest nonce 密钥；空则每次启动随机 |
| `console_user` | `admin`（密码非空时） | 控制台用户名 |
| `console_password` | 空 | 非空启用 Cookie 登录；生产必须设置 |

## alarm

| 字段 | 默认 | 说明 |
| --- | --- | --- |
| `number` | `8000` | 报警短号 |
| `levels` | 空 | 顺序升级策略，数组顺序即优先级 |
| `levels[].name` | `Level N` | 级别名称 |
| `levels[].members` | 空 | 该级值班分机 |
| `levels[].timeout_s` | `20` | 无人接听超时，超时后取消并呼下一级 |

## media

| 字段 | 默认 | 说明 |
| --- | --- | --- |
| `transcode` | `false` | 是否对两端不同 G.711 编码做 PCMU/PCMA 转码。默认关闭：按 SDP 协商结果透传，便于暴露设备编码问题。 |

## registration

UDP 注册没有连接状态，断电/断网后只能等租期到期才会显示离线。

| 字段 | 默认 | 说明 |
| --- | --- | --- |
| `default_expires_s` | `300` | 设备未带 Expires 时使用的租期 |
| `max_expires_s` | `300` | 服务器接受的最大租期；设备给更长时按此值返回 |
| `heartbeat.enabled` | `false` | 启用 SIP OPTIONS 心跳，快速检测断电/离线 |
| `heartbeat.interval_s` | `30` | 心跳间隔，最小 5 秒 |
| `heartbeat.timeout_s` | `5` | 单次 OPTIONS 超时；不能大于间隔 |
| `heartbeat.failures` | `3` | 连续失败次数，达到后标记离线，1-10 |

```yaml
registration:
  default_expires_s: 300
  max_expires_s: 300
  heartbeat:
    enabled: true
    interval_s: 30
    timeout_s: 5
    failures: 3
```

设备应在租期内重新 REGISTER；超过租期后网页自动显示离线。

## broadcast

| 字段 | 默认 | 说明 |
| --- | --- | --- |
| `auto_answer` | `<http://127.0.0.1>;info=alert-autoanswer` | 广播 INVITE 的 `Alert-Info` |
| `invite_timeout_s` | `5` | 每个广播终端 INVITE 超时 |
| `max_concurrency` | `16` | 广播 INVITE 并发 |

## tts

| 字段 | 默认 | 说明 |
| --- | --- | --- |
| `provider` | `openai` | `openai`（OpenAI/千问/本地 Kokoro 兼容）或 `minimax` |
| `base_url` | `http://127.0.0.1:8399` | OpenAI 兼容根地址或 MiniMax 完整 t2a_v2 地址 |
| `api_key` | 空 | 公网服务密钥；也可在控制台 TTS 设置页保存（DB 覆盖 YAML） |
| `model` | 空 | OpenAI/Qwen/MiniMax 模型名；本地 Kokoro 可留空 |
| `voice_zh` | `zf_001` | 中文音色 |
| `voice_en` | `af_heart` | 英文音色 |
| `timeout_s` | `30` | 合成超时 |

## zones

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `id` | 是 | 唯一分区 ID |
| `name` | 否 | 默认等于 ID |
| `dial` | 否 | 实时寻呼短号，如 `5001`；全局唯一 |
| `codec` | 否 | 广播编码：`pcmu`（默认）或 `opus`；整个分区统一 |
| `members` | 否 | 分机列表；省略表示全部设备 |
| `mode` | 否 | `unicast`（默认）或 `multicast` |
| `multicast` | 组播时必填 | 如 `239.255.10.1:5004` |

## devices

> 仅当 SQLite `devices` 表为空时从配置导入；之后以数据库为准。Web 控制台新增/删除设备会写库。

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `ext` | 是 | 分机号，唯一 |
| `name` | 是 | 显示名 |
| `password` | 是 | SIP Digest 密码；数据库中以明文保存，生产需保护 DB 文件 |
| `zone` | 否 | 区域文本 |
| `snapshot` | 否 | 设备 HTTP 抓拍地址 |

## 完整示例

见 [config.example.yaml](../config.example.yaml)。

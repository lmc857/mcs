# MCS 测试与兼容性

> 当前代码 `0.14.2`。本文记录自动化测试覆盖、已验证环境与已知兼容边界，不是第三方认证报告。

## 1. 自动化测试

运行：

```bash
go test ./...
go test -race ./internal/sipserver ./internal/api ./internal/store ./internal/tts
```

| 包/测试 | 覆盖 |
| --- | --- |
| `internal/sipserver/digest_test.go` | MD5/SHA-256 Digest、错误 nonce/密码 |
| `audio_test.go` | WAV 解析、重采样、G.711 编解码、PCMU↔PCMA 转码 |
| `mp3_opus_test.go` | MP3 解码、Opus 编码/解码、Opus 广播 SDP、MP3 音频库 |
| `register_test.go` | 401 → Digest → 200、注销、过期 |
| `call_test.go` | B2BUA 呼叫、RTP 双向往返、录音 WAV、超时升级、DTMF 开锁 |
| `broadcast_test.go` | TTS 扇出、优先级抢占、音频库/文件广播 |
| `paging_test.go` | 话机拨分区号实时寻呼、RTP 转发、挂机停止 |
| `video_test.go` | H.264 双向中继、WebRTC 音视频接收、浏览器麦克风回传 |
| `srtp_test.go` | SDES SDP 解析/改写、双向 SRTP、SRTCP 转发 |
| `srtp_policy_test.go` | `auto/required/off` SRTP 策略 |
| `tls_transport_test.go` | Contact/REGISTER transport 保留，B 腿 Request 使用 TLS |
| `tls_outbound_test.go` | MCS 出站 B 腿真实 TLS INVITE/200 |
| `sdp_test.go` | 音视频 SDP 端口/连接地址改写 |
| `schedule_test.go` | cron 解析、到期推进、JSON 持久化 |
| `real_tts_test.go` | `MCS_REAL_TTS=1` 时调用真实 Kokoro |
| `internal/config/settings_test.go` | 服务器设置、TLS/SRTP、STUN/TURN URL/凭据/公网 IP 校验 |
| `internal/store/sqlite_test.go` | CDR/报警/设备写库、重启恢复、schema 幂等 |
| `internal/api/server_test.go` | WebSocket 报警、登录/Cookie、设备 CRUD/改密、抓拍代理 |
| `internal/tts/client_test.go` | OpenAI 兼容认证/模型、MiniMax hex 音频、缓存、错误响应 |

## 2. 已验证环境

- macOS arm64，Go 1.24.3；
- SIP 测试 UAC/UAS 基于 sipgo，运行在 127.0.0.1 不同端口；
- WebRTC 测试客户端基于 Pion，与本机 MCS 建立 ICE/DTLS；
- 真实 Kokoro-82M（`http://127.0.0.1:8399`）中文 TTS 已验证；
- SQLite、SRTP、TLS/HTTPS 均为本机冒烟测试；
- SIP TLS 入站、HTTPS、出站 B 腿 TLS、SRTP 策略已做自动化/本机冒烟验证。

**未验证**：真实报警柱/门口机/SIP 音箱、真实组播网络、Windows 主机、Linux 主机 systemd 实机、跨公网 NAT。

## 3. 终端兼容矩阵

| 能力 | 要求 |
| --- | --- |
| 注册 | RFC 3261 REGISTER + Digest MD5（服务端也校验 SHA-256 响应） |
| 呼叫 | INVITE/100/180/200/ACK/BYE/CANCEL；Contact 可达 |
| 音频 | G.711 µ-law / A-law（PCMU/PCMA），8kHz，20ms 包 |
| 视频 | H.264，SDP `m=video`，payload 透传 |
| 自动应答 | 广播 INVITE 携带 `Alert-Info`（默认 `<http://127.0.0.1>;info=alert-autoanswer`） |
| 实时寻呼 | 终端可向 MCS 发送 RTP；分区 `dial` 唯一 |
| SRTP | `RTP/SAVP` + `a=crypto`，AES_CM_128_HMAC_SHA1_80/32 |
| SIP TLS | 终端信任 MCS 证书或允许自签 |
| 视频对讲 | H.264 + 可选 `snapshot` HTTP 地址；开锁接受 DTMF INFO |
| 浏览器 | Chrome/Edge（H.264）；WebRTC 需 `localhost` 或 HTTPS |

## 4. 已知不兼容/限制

- G.722/G.729/Opus SIP 终端：不转码；WebRTC 侧优先 PCMU/PCMA。
- SIP 终端 DTLS-SRTP：未实现；仅 SDES-SRTP。
- mTLS/客户端证书：未实现。
- 广播/寻呼/会议 SRTP：未实现，仍为明文 RTP。
- 组播：代码支持，但未在真实交换机/音箱上验证。
- 固定 RTP 端口段：未实现，RTP 使用动态端口；云安全组需放行 UDP 高位端口。
- Windows 安装：原生 SCM 服务已支持；真实 Windows 主机仍需现场验证。
- 浏览器视频回传/视频录像：未实现。
- STUN/TURN 使用外部标准服务；MCS 不内置 TURN 服务。
- 单管理员、无 RBAC/登录失败锁定/操作审计。

## 5. 升级前检查

1. 备份 `data/mcs.db` 与 `config.yaml`；
2. 运行 `go test ./...`；
3. 先在测试机验证话机注册、呼叫、SRTP、HTTPS；
4. 再替换生产二进制并重启。

# MCS API 参考（当前实现）

> 适用版本：`0.1.0-dev` 当前代码。
> 文中只列 **已经实现** 的接口；规划中的接口见 [MCS-接口与数据模型.md](MCS-接口与数据模型.md)，不要直接对接规划稿。
> 机器可读版本：[openapi.yaml](openapi.yaml)。

## 1. 基础

| 项 | 说明 |
| --- | --- |
| Base URL | `http://<host>:8080` 或 `https://<host>:8443` |
| 前缀 | 除静态页面/录音外均为 `/api/v1` |
| 编码 | JSON（`Content-Type: application/json`）；上传为 `multipart/form-data` |
| 时间 | RFC 3339，例如 `2026-11-18T13:39:49+08:00` |
| 鉴权 | 默认关闭；配置 `auth.console_password` 后启用 Cookie 会话 |
| Cookie | `mcs_session`，HttpOnly；HTTPS 下自动 `Secure` |

启用登录后：

- `POST /api/v1/login`、`GET /api/v1/session`、`GET /api/v1/healthz` 公开；
- 其余 `/api/*` 与 `/recordings/*` 需要有效 Cookie，否则 `401`。

### 通用错误

```json
{"code":"VALIDATION_FAILED","message":"..."}
```

| HTTP | code | 含义 |
| --- | --- | --- |
| 400 | `VALIDATION_FAILED` | 参数/JSON/SDP 不合法 |
| 401 | `UNAUTHORIZED` | 未登录或会话过期 |
| 404 | `NOT_FOUND` | 对象不存在 |
| 409 | `BROADCAST_FAILED` / `UNLOCK_FAILED` | 分区忙、开锁失败等冲突 |
| 413 | `TOO_LARGE` | 上传超过 20MB |
| 502 | `BROADCAST_FAILED` / `SNAPSHOT_FAILED` | 下游 TTS/设备失败 |
| 503 | `BROADCAST_FAILED` | 分区无在线终端 |

## 2. 登录与会话

### POST `/api/v1/login`

```json
{"username":"admin","password":"change-me"}
```

成功 `200` + `Set-Cookie: mcs_session=...`：

```json
{"status":"ok","user":"admin"}
```

失败 `401`。

### POST `/api/v1/logout`

删除当前会话，`200 {"status":"ok"}`。

### GET `/api/v1/session`

```json
{"auth_required":true,"logged_in":true,"user":"admin"}
```

未启用登录时 `auth_required=false`、`logged_in=true`。

## 2.5 TTS 设置

### GET `/api/v1/tts/config`

返回不含 API Key：

```json
{"provider":"openai","base_url":"http://127.0.0.1:8399","model":"",
 "voice_zh":"zf_001","voice_en":"af_heart","timeout_s":30,"has_api_key":false}
```

### PUT `/api/v1/tts/config`

```json
{"provider":"openai","base_url":"https://api.openai.com/v1","model":"tts-1",
 "voice_zh":"alloy","voice_en":"alloy","timeout_s":30,
 "api_key":"sk-...","clear_api_key":false}
```

- `api_key` 为空表示保留原密钥；
- `clear_api_key=true` 清除；
- 保存后立即重建 TTS 客户端，无需重启；
- 同时写入 SQLite `settings.tts`，优先于 `config.yaml`。

### POST `/api/v1/tts/test`

用当前配置合成一句“TTS 测试”，成功：

```json
{"status":"ok","bytes":51501}
```

失败 `502 TTS_FAILED`。

## 3. 设备

设备字段（`DeviceView`）：

```json
{
  "ext":"1001","name":"值班室","zone":"A 区",
  "online":true,"contact":"<sip:1001@192.168.1.20:5060>",
  "source":"192.168.1.20:5060","user_agent":"Linphone",
  "model":"","firmware":"","snapshot":"","enabled":true,
  "expires_at":"2026-11-18T14:00:00+08:00",
  "last_seen":"2026-11-18T13:50:00+08:00"
}
```

### GET `/api/v1/devices`

```json
{"items":[...],"total":3,"online":2}
```

### POST `/api/v1/devices`

```json
{"ext":"1009","name":"东门门口机","password":"pass-1009","zone":"东门","snapshot":"http://192.168.1.60/snapshot.jpg"}
```

`201` 返回设备视图。`ext` 唯一；新设备 `password` 必填。

### PATCH `/api/v1/devices/{ext}`

字段均可选：`name`、`password`、`zone`、`snapshot`；`password` 为空表示不改。

### DELETE `/api/v1/devices/{ext}`

`200 {"status":"deleted"}`；注册中的设备会立即从内存移除，下次 REGISTER 失败。

### GET `/api/v1/devices/{ext}/snapshot`

代理设备配置的 HTTP 快照地址，返回 `image/*` 或 `404/502`。

## 4. 通话与对讲

### GET `/api/v1/calls/active`

```json
{"items":[{"call_id":"mcs-...","from":"1002","to":"1001","state":"answered","started_at":"...","video":true}],"total":1}
```

`state`：`ringing` / `answered`。

### POST `/api/v1/intercom/{call_id}/unlock`

```json
{"code":"#"}
```

向门口机发送 DTMF INFO（优先 `application/dtmf-relay`，失败回退 `application/dtmf`）。成功 `200 {"status":"ok"}`；通话不存在/未接通/设备拒绝返回 `409`。

## 5. 话单

### GET `/api/v1/cdr?limit=50`

`limit` 1~500，默认 50。返回 `{"items":[CDR...],"total":N}`，最新在前。

CDR 字段：

```json
{"id":"...","call_id":"...","from":"1002","to":"1001","device_id":"",
 "start_at":"...","answer_at":"...","end_at":"...","duration_s":12,
 "result":"answered","recording":"/recordings/2026/11/18/....wav"}
```

录音 URL 需登录（启用鉴权时）。

## 6. 报警

### POST `/api/v1/alarm/test`

```json
{"from":"1003","to":"8000"}
```

模拟一条测试报警，`202 {"status":"accepted"}`。

### GET `/api/v1/alarms?limit=50`

`limit` 1~500，最新在前。

Alarm 字段：

```json
{"id":"alm-...","call_id":"...","from":"1003","to":"8000","alarm_number":"8000",
 "device_name":"东门门口机","zone":"东门","state":"ringing","level":0,"level_name":"值班员",
 "answered_by":"","started_at":"...","answered_at":"","ended_at":"","closed_at":"",
 "disposition":"","note":"","recording":"","test":false,"last_update_at":"..."}
```

### GET `/api/v1/alarms/{id}`

单条；不存在 `404`。

### POST `/api/v1/alarms/{id}/close`

```json
{"disposition":"已出警","note":"现场确认"}
```

关闭报警并返回更新后的 Alarm。

## 7. 广播

### GET `/api/v1/broadcast/zones`

```json
{"items":[{"id":"a","name":"A 区","dial":"5001","codec":"pcmu","mode":"unicast",
 "members":["1001"],"online":1}],"total":1}
```

### POST `/api/v1/broadcast/start`

```json
{"zone_id":"a","text":"请各班注意","voice":"zf_001","priority":2}
```

或使用音频库：

```json
{"zone_id":"a","media_id":"med-...","priority":3}
```

- `priority`：0 紧急、1 高、2 实时、3 通知、4 背景；默认 3。
- 成功 `202` 返回 `BroadcastView`：

```json
{"id":"bcs-...","zone_id":"a","zone_name":"A 区","state":"starting",
 "source":"tts","codec":"pcmu","priority":2,"text":"...","voice":"zf_001",
 "started_at":"...","total":1,"delivered":0,"active":0,"failed":0}
```

- `409` 分区被更高优先级占用；`503` 无在线终端；`502` TTS/媒体失败。

### POST `/api/v1/broadcast/stop`

```json
{"zone_id":"a"}
```

停止当前分区广播，`200` 返回最终状态；无活动广播 `404`。

### GET `/api/v1/broadcast/active`

只返回活动中的广播会话。

### GET `/api/v1/broadcast/sessions`

返回内存中的全部广播会话（含已结束，按开始时间倒序）。

### 定时广播

| 方法 | 路径 |
| --- | --- |
| GET | `/api/v1/broadcast/schedules` |
| POST | `/api/v1/broadcast/schedules` |
| PATCH | `/api/v1/broadcast/schedules/{id}` |
| DELETE | `/api/v1/broadcast/schedules/{id}` |

新增：

```json
{"name":"上课铃","zone_id":"a","cron":"0 8 * * 1-5","text":"上课时间到了","priority":3,"enabled":true}
```

也可用 `media_id` 代替 `text`。cron 为 5 段：分 时 日 月 周，按服务器本地时间执行。

更新字段：`name`、`text`、`voice`、`media_id`、`priority`、`cron`、`enabled`。

## 8. 音频库

### GET `/api/v1/media`

```json
{"items":[{"id":"med-...","name":"上课铃","format":"wav","size":151244,"duration_ms":3150,"created_at":"..."}],"total":1}
```

### POST `/api/v1/media`

`multipart/form-data`：

- `file`：PCM16 WAV 或 MP3，≤20MB；
- `name`：可选，默认文件名。

成功 `201` 返回 MediaItem。

### DELETE `/api/v1/media/{id}`

`200 {"status":"deleted"}`。

### GET `/api/v1/media/{id}/download`

返回 `audio/wav` 附件。

## 9. WebRTC 音视频

### POST `/api/v1/webrtc/offer`

浏览器 `RTCPeerConnection` 的 offer：

```json
{"call_id":"mcs-...","sdp":"v=0\r\n..."}
```

返回：

```json
{"type":"answer","sdp":"v=0\r\n..."}
```

- 只对带视频的活动呼叫有效；
- 音频为 sendrecv（浏览器麦克风对讲），视频为 recvonly；
- 无 TURN，跨 NAT 可能失败。

浏览器端还需要：

```js
pc.addTransceiver('video', {direction:'recvonly'});
// 麦克风：getUserMedia + pc.addTrack，或 recvonly 降级
```

## 10. WebSocket

### GET `/api/v1/ws`

协议升级为 WebSocket。每条消息：

```json
{"type":"alarm.created","ts":"2026-11-18T13:50:00+08:00","data":{...}}
```

事件类型：

| type | 触发 | data 关键字段 |
| --- | --- | --- |
| `device.registered` | 设备注册 | ext, contact, source, model, firmware |
| `device.unregistered` | 注销/过期 | ext, source |
| `call.incoming` | 呼叫进入 | call_id, from, to |
| `call.answered` | 接通 | call_id, from, to |
| `call.ended` | 结束 | call_id, from, to, result, duration_s, recording |
| `alarm.created` | 报警产生 | Alarm 字段 |
| `alarm.escalated` | 升级到下一级 | Alarm 字段 |
| `alarm.answered` | 报警被接听 | alarm_id, call_id, answered_by |
| `alarm.updated` | 报警状态变化 | Alarm 字段 |
| `alarm.closed` | 报警关闭 | Alarm 字段 |
| `intercom.unlock` | 开锁已发送 | call_id, to, code, result |
| `broadcast.started` | 广播开始 | BroadcastView |
| `broadcast.finished` | 广播结束 | BroadcastView |

连接需要登录 Cookie（启用鉴权时）。断线由前端 3 秒重连。

## 11. 系统与静态资源

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/v1/healthz` | 公开健康检查 `{"status":"ok"}` |
| GET | `/api/v1/system/info` | 名称、版本、uptime、设备总数/在线 |
| GET | `/recordings/...` | 录音 WAV 文件，需登录 |

## 12. curl 示例

```bash
# 登录并保存 Cookie
curl -c /tmp/mcs.cookie -X POST http://127.0.0.1:8080/api/v1/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"change-me"}'

# 设备
curl -b /tmp/mcs.cookie http://127.0.0.1:8080/api/v1/devices

# TTS 广播
curl -b /tmp/mcs.cookie -X POST http://127.0.0.1:8080/api/v1/broadcast/start \
  -H 'Content-Type: application/json' \
  -d '{"zone_id":"a","text":"测试广播","priority":2}'

# 关闭报警
curl -b /tmp/mcs.cookie -X POST http://127.0.0.1:8080/api/v1/alarms/alm-xxx/close \
  -H 'Content-Type: application/json' \
  -d '{"disposition":"测试","note":""}'
```

## 13. 未实现（不要对接）

以下仅在规划稿中：分页/筛选、设备模板、CSV 导入导出、远程重启、批量升级、角色/RBAC、审计查询、录像下载、ONVIF/RTSP、消防联动、TURN。

实现状态以本文件与 [使用手册](MCS-使用手册.md) 为准。

> **规划稿**：本文包含大量尚未实现的 API/数据模型。当前版本请以 [MCS-API参考.md](MCS-API参考.md) 和 [MCS-使用手册.md](MCS-使用手册.md) 为准。

# MCS 接口与数据模型

| 项目 | 内容 |
| --- | --- |
| 文档版本 | v0.1（待评审） |
| 日期 | 2026-11-16 |
| 关联 | [MCS 产品方案](MCS-产品方案.md) · [MCS 技术方案](MCS-技术方案.md) |
| 基础路径 | `/api/v1` |
| 规范 | REST/JSON + WebSocket + Webhook + MQTT；OpenAPI 3.1 |

---

## 1. 通用约定

### 1.1 请求与响应

- JSON UTF-8；字段 `snake_case`；时间 RFC3339（带时区）。
- ID 使用带前缀的 ULID：`dev_`、`alm_`、`evt_`、`bct_`、`rec_`、`usr_`。
- 分页：`?page=1&page_size=20&sort=-created_at`；响应：

```json
{
  "items": [],
  "total": 128,
  "page": 1,
  "page_size": 20
}
```

- 写操作支持 `Idempotency-Key`（24h 去重），用于报警触发、广播启动等。
- 长任务（备份、升级、诊断）返回 `202 + task_id`，通过 `GET /tasks/{id}` 查询进度。

### 1.2 鉴权

```http
POST /api/v1/auth/login
Content-Type: application/json

{"username": "admin", "password": "******"}
```

```json
{
  "access_token": "eyJ...",
  "refresh_token": "eyJ...",
  "expires_in": 900,
  "user": {"id": "usr_01", "name": "admin", "role": "super_admin"}
}
```

- 后续请求：`Authorization: Bearer <access_token>`。
- 刷新：`POST /auth/refresh`；注销：`POST /auth/logout`（refresh 失效）。
- WebSocket 不放在 URL 里传长期 token：先 `POST /auth/ws-ticket` 取 60 秒一次性 ticket，再 `wss://.../ws?ticket=...`。
- 权限：RBAC 菜单 + 操作 + 数据范围（区域）；越权返回 `403`。

### 1.3 错误

```json
{
  "code": "DEVICE_OFFLINE",
  "message": "设备离线，无法发起呼叫",
  "details": {"device_id": "dev_10086"},
  "trace_id": "tr_01HQ..."
}
```

| HTTP | code 示例 | 说明 |
| --- | --- | --- |
| 400 | `VALIDATION_FAILED` | 参数校验 |
| 401 | `AUTH_INVALID` / `TOKEN_EXPIRED` | 未登录/过期 |
| 403 | `FORBIDDEN` / `LICENSE_LIMIT` | 无权限/超授权 |
| 404 | `NOT_FOUND` | 资源不存在 |
| 409 | `CONFLICT` / `ALARM_STATE_INVALID` | 状态冲突 |
| 429 | `RATE_LIMITED` | 限流 |
| 503 | `DEVICE_OFFLINE` / `BROADCAST_BUSY` | 设备/广播状态 |
| 500 | `INTERNAL` | 服务端错误（带 trace_id） |

---

## 2. REST 接口

### 2.1 认证与用户

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/auth/login` | 登录 |
| POST | `/auth/refresh` | 刷新 token |
| POST | `/auth/logout` | 注销 |
| POST | `/auth/ws-ticket` | 一次性 WS ticket |
| GET | `/me` | 当前用户与权限 |
| GET/POST | `/users` | 用户列表/创建 |
| PATCH/DELETE | `/users/{id}` | 修改/删除 |
| POST | `/users/{id}/reset-password` | 重置密码 |
| GET/POST | `/roles` | 角色 |
| PATCH | `/roles/{id}` | 权限配置 |

### 2.2 设备与模板

> M1 已实现 `GET/POST /devices`、`PATCH/DELETE /devices/{ext}`（SQLite 持久化）；导入/导出、模板、远程重启等仍为规划。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/devices` | 列表；筛选 `zone_id/status/model_id/q` |
| POST | `/devices` | 创建设备（自动生成分机与密码） |
| GET/PATCH/DELETE | `/devices/{id}` | 详情/修改/删除 |
| POST | `/devices/import` | CSV 批量导入（返回逐行结果） |
| GET | `/devices/export` | 导出 CSV |
| POST | `/devices/{id}/reboot` | 远程重启（模板支持时） |
| POST | `/devices/{id}/test-call` | 测试呼叫 |
| GET | `/devices/{id}/config` | 预览配置 |
| POST | `/devices/{id}/credentials/reset` | 重置设备密钥/密码 |
| GET/POST | `/device-models` | 型号模板 |
| PATCH/DELETE | `/device-models/{id}` | 修改/删除 |

创建设备：

```json
{
  "device_no": "10086",
  "name": "3 号楼东侧报警柱",
  "model_id": "mdl_acme_pillar",
  "zone_id": "zone_a",
  "mac": "AA:BB:CC:DD:EE:FF",
  "ext": "10086",
  "note": ""
}
```

### 2.3 区域、分机、中继、拨号规则

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET/POST | `/zones` | 区域树 |
| PATCH/DELETE | `/zones/{id}` | 修改/删除（有设备时禁止删） |
| GET/PUT | `/zones/{id}/members` | 区域终端列表（设备/分机） |
| GET/POST | `/extensions` | 分机 |
| PATCH/DELETE | `/extensions/{id}` | 修改/删除 |
| GET/POST | `/trunks` | SIP 中继 |
| PATCH/DELETE | `/trunks/{id}` | 修改/删除 |
| GET/POST | `/dial-rules` | 拨号规则 |
| PATCH/DELETE | `/dial-rules/{id}` | 修改/删除 |

### 2.4 报警

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET/POST | `/alarm/policies` | 报警策略 |
| PATCH/DELETE | `/alarm/policies/{id}` | 修改/删除 |
| GET/POST | `/duty-groups` | 值班组 |
| PATCH/DELETE | `/duty-groups/{id}` | 修改/删除 |
| GET/PUT | `/duty-groups/{id}/members` | 成员与顺序 |
| GET/POST | `/duty-schedules` | 排班 |
| GET | `/alarms` | 事件列表（`state/zone/from/to/q`） |
| GET | `/alarms/{id}` | 详情 + 时间线 |
| POST | `/alarms/{id}/ack` | 接听/受理 |
| POST | `/alarms/{id}/note` | 追加备注 |
| POST | `/alarms/{id}/close` | 关闭（处置结果） |
| POST | `/alarms/{id}/forward` | 转交 |
| POST | `/alarms/{id}/callback` | 回拨（2.0） |
| GET | `/alarms/{id}/evidence` | 证据包清单 |
| GET | `/alarms/{id}/evidence/verify` | 哈希链校验 |
| POST | `/alarm/test` | 模拟报警（演练） |
| GET | `/alarm/stats` | 统计（区域/类型/时段） |

关闭事件：

```json
{
  "disposition": "dispatched",
  "note": "已通知保安到场，现场无异常",
  "attachments": []
}
```

### 2.5 对讲与访客

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/intercom/call` | 从调度台发起对讲（`device_id`） |
| POST | `/intercom/{call_id}/unlock` | 远程开锁（DTMF INFO） |
| POST | `/webrtc/offer` | 浏览器 WebRTC 双向音频 + 视频接收 offer/answer |
| GET | `/devices/{ext}/snapshot` | 设备 HTTP 抓拍代理 |
| POST | `/intercom/{call_id}/snapshot` | 抓拍 |
| GET | `/visitor-logs` | 访客记录 |
| GET | `/unlock-logs` | 开锁记录 |
| GET/POST | `/cameras` | 摄像头 |
| PATCH/DELETE | `/cameras/{id}` | 修改/删除 |
| GET | `/cameras/{id}/snapshot` | 抓拍图片 |
| GET | `/cameras/{id}/stream` | 预览地址（WebRTC/RTSP） |

开锁请求需带 `call_id`，服务端校验通话中且操作人有权限。

### 2.6 广播

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/broadcast/zones` | 分区与在线数（M0） |
| POST | `/broadcast/start` | TTS 广播（zone_id/text/voice/priority） |
| POST | `/broadcast/stop` | 停止分区广播 |
| GET | `/broadcast/active` | 活动广播 |
| GET | `/broadcast/sessions` | 广播历史 |
| GET/POST | `/broadcast/tasks` | 广播任务（立即/定时，规划） |
| PATCH/DELETE | `/broadcast/tasks/{id}` | 修改/删除 |
| POST | `/broadcast/tasks/{id}/start` | 立即开始 |
| POST | `/broadcast/tasks/{id}/stop` | 停止 |
| GET | `/broadcast/sessions` | 活动/历史会话 |
| GET | `/broadcast/sessions/{id}` | 会话详情（逐终端状态） |
| POST | `/broadcast/page` | 实时寻呼（WebRTC/话机） |
| GET/POST | `/media` | 音频库（上传 multipart） |
| DELETE | `/media/{id}` | 删除（被引用时拒绝） |
| POST | `/tts/preview` | TTS 试听 |
| GET/POST | `/broadcast/schedules` | 定时计划（cron/节假日） |
| PATCH/DELETE | `/broadcast/schedules/{id}` | 修改/删除 |
| GET | `/broadcast/logs` | 广播日志 |

创建广播任务：

```json
{
  "name": "上课铃",
  "type": "scheduled",
  "zone_ids": ["zone_teaching_a"],
  "source": {"type": "media", "media_id": "med_bell_01"},
  "priority": 3,
  "schedule": {"cron": "0 8 * * 1-5", "holiday_skip": true},
  "volume": 80,
  "enabled": true
}
```

实时寻呼：

```json
{"zone_ids": ["zone_a"], "priority": 2, "duration_max_s": 300}
```

响应返回 `session_id` 与 WebRTC 上行参数；结束后 `POST /broadcast/sessions/{id}/stop`。

### 2.7 通话与录音

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/calls/active` | 活动通话 |
| GET | `/calls/{id}` | 呼叫详情（信令/媒体统计） |
| POST | `/calls/{id}/hangup` | 挂断 |
| POST | `/calls/{id}/hold` | 保持 |
| POST | `/calls/{id}/resume` | 恢复 |
| POST | `/calls/{id}/transfer` | 转接（`target`） |
| GET | `/cdr` | 话单（`from/to/caller/callee/result`） |
| GET | `/cdr/{id}` | 详情 |
| GET | `/recordings` | 录音列表 |
| GET | `/recordings/{id}/download` | 下载（鉴权 + 审计） |
| DELETE | `/recordings/{id}` | 删除（需权限） |

### 2.8 系统与运维

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/system/info` | 版本、运行时长、平台 |
| GET/PATCH | `/system/network` | 网卡/IP/端口 |
| GET/PATCH | `/system/sip` | SIP 参数、TLS |
| GET/PATCH | `/system/media` | 端口段、编码、SRTP |
| GET | `/system/health` | 健康检查（供探针） |
| GET | `/system/license` | 许可信息 |
| POST | `/system/license` | 上传许可 |
| GET | `/system/logs` | 日志查询（分页/级别/模块） |
| GET | `/system/metrics` | Prometheus 文本 |
| POST | `/system/backup` | 创建备份（异步） |
| GET | `/system/backups` | 备份列表 |
| POST | `/system/restore` | 恢复 |
| POST | `/system/diagnostics` | 生成诊断包（异步） |
| POST | `/system/upgrade` | 上传升级包 |
| GET | `/tasks/{id}` | 异步任务进度 |
| GET | `/audit-logs` | 审计日志 |

### 2.9 Webhook 订阅

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET/POST | `/webhooks` | 订阅 |
| PATCH/DELETE | `/webhooks/{id}` | 修改/删除 |
| POST | `/webhooks/{id}/test` | 发送测试事件 |
| GET | `/webhooks/{id}/deliveries` | 投递记录 |

```json
{
  "url": "https://alarm.example.com/hooks/mcs",
  "secret": "whsec_xxx",
  "events": ["alarm.created", "alarm.closed", "device.offline", "broadcast.finished"],
  "enabled": true
}
```

---

## 3. WebSocket 事件

### 3.1 连接

```text
POST /api/v1/auth/ws-ticket  → {"ticket": "wt_...", "expires_in": 60}
wss://host/api/v1/ws?ticket=wt_...
```

客户端发送：

```json
{"type": "subscribe", "topics": ["alarm.*", "call.*", "device.*", "broadcast.*"]}
```

服务端事件信封：

```json
{
  "id": "evt_01HQ...",
  "type": "alarm.created",
  "ts": "2026-11-16T10:00:00.123+08:00",
  "data": {}
}
```

心跳：客户端每 25s 发 `{"type":"ping"}`，服务端回 `pong`。

### 3.2 事件类型

| 事件 | 触发 | 关键字段 |
| --- | --- | --- |
| `device.registered` | 设备上线 | device_id, ext, ip, ts |
| `device.offline` | 离线 | device_id, last_online, reason |
| `device.updated` | 配置变更 | device_id, changes |
| `call.incoming` | 来电 | call_id, from, to, direction |
| `call.ringing` | 振铃 | call_id, branches |
| `call.answered` | 接通 | call_id, answered_by |
| `call.ended` | 结束 | call_id, duration, result |
| `alarm.created` | 报警创建 | alarm 对象 |
| `alarm.ringing` | 开始振铃 | alarm_id, targets |
| `alarm.answered` | 接听 | alarm_id, user_id |
| `alarm.escalated` | 升级 | alarm_id, level, target |
| `alarm.updated` | 状态变化 | alarm_id, state |
| `alarm.closed` | 关闭 | alarm_id, disposition |
| `broadcast.started` | 广播开始 | session_id, zone_ids |
| `broadcast.progress` | 进度 | session_id, ok, failed, total |
| `broadcast.finished` | 结束 | session_id, duration, stats |
| `intercom.unlock` | 开锁结果 | device_id, call_id, result |
| `system.health` | 健康变化 | cpu, mem, disk, regs |
| `license.warning` | 容量/到期 | usage, limit |

`alarm.created` 示例：

```json
{
  "id": "evt_01HQ1",
  "type": "alarm.created",
  "ts": "2026-11-16T10:00:00.123+08:00",
  "data": {
    "alarm_id": "alm_01HQ1",
    "device_id": "dev_10086",
    "device_name": "3 号楼东侧报警柱",
    "zone_id": "zone_a",
    "type": "panic",
    "priority": 0,
    "state": "created",
    "location": {"lat": 31.23, "lng": 121.47, "desc": "东门"},
    "snapshot": "/api/v1/cameras/cam_01/snapshot",
    "occurred_at": "2026-11-16T10:00:00+08:00"
  }
}
```

---

## 4. Webhook

### 4.1 请求

```http
POST /hooks/mcs HTTP/1.1
Content-Type: application/json
X-MCS-Event: alarm.created
X-MCS-Delivery: dlv_01HQ...
X-MCS-Timestamp: 1763258400
X-MCS-Signature: v1=5f2c...（hex）

{"event_id":"evt_01HQ1","event":"alarm.created","ts":"2026-11-16T10:00:00.123+08:00","data":{}}
```

签名：`HMAC-SHA256(secret, timestamp + "." + raw_body)`；接收方校验时间戳 ±300s，防重放。

### 4.2 重试

- 成功：HTTP 2xx（5s 超时）。
- 失败：1s、5s、30s、5min、30min 重试；5 次后进死信。
- 连续 100 次失败自动停用并告警。
- 投递记录保留 7 天；支持手动重放。

### 4.3 事件

`alarm.created`、`alarm.answered`、`alarm.escalated`、`alarm.closed`、`call.completed`、`device.offline`、`broadcast.finished`、`license.warning`。

---

## 5. MQTT

- 连接外部 Broker（EMQX/Mosquitto），支持 TLS 与用户名密码；只发布，不订阅（1.0）。
- QoS 1；`retain=false`；消息体与 Webhook 的 `data` 一致。

| Topic | 说明 |
| --- | --- |
| `mcs/{site_id}/alarm/created` | 报警 |
| `mcs/{site_id}/alarm/closed` | 关闭 |
| `mcs/{site_id}/device/status` | 上下线 |
| `mcs/{site_id}/broadcast/state` | 广播状态 |
| `mcs/{site_id}/call/completed` | 话单 |

---

## 6. 数据库表

> 1.0 使用 SQLite；以下为逻辑模型，PostgreSQL 可平移。敏感字段（密码/密钥）用 AES-GCM 加密存储，另存哈希用于校验。

### 6.1 设备与区域

| 表 | 关键字段 | 说明 |
| --- | --- | --- |
| `zones` | id, parent_id, name, path, type, sort, broadcast_mode, multicast_addr, multicast_port | 区域树；`path` 便于子树查询；组播配置 |
| `device_models` | id, vendor, model, config_format, template, auto_answer_header, codecs, unlock_method, unlock_code, volume_up, volume_down, snapshot_url | 型号模板 |
| `devices` | id, device_no, name, model_id, zone_id, mac, ext, sip_ha1, sip_password_enc, ip, status, last_online, firmware, proto_version, capabilities_json, config_version, enabled | 设备；注册上报固件/协议版本 |
| `extensions` | id, number, display_name, type, device_id, zone_id, enabled | 分机（含 WebRTC 坐席） |
| `trunks` | id, name, host, port, transport, username, password_enc, auth_mode, prefix, caller_id, enabled | 中继 |
| `dial_rules` | id, priority, pattern, match_type, action, target, caller_scope, time_condition_id, enabled | 拨号规则 |
| `cameras` | id, name, device_id, zone_id, rtsp_url_enc, username_enc, password_enc, snapshot_url, status | 摄像头 |

索引：`devices(zone_id, status)`、`devices(ext)`、`devices(mac)`、`extensions(number)`。

### 6.2 报警与值班

| 表 | 关键字段 | 说明 |
| --- | --- | --- |
| `duty_groups` | id, name, strategy, ring_timeout, zone_id | 值班组 |
| `duty_members` | id, group_id, extension_id, order_no, role, enabled | 成员 |
| `duty_schedules` | id, group_id, weekday, start_time, end_time, holiday, overrides | 排班 |
| `alarm_policies` | id, name, match_device_id, match_zone_id, match_type, priority, duty_group_id, ring_strategy, ring_timeout, escalation_json, record, notify_json, auto_broadcast_json, enabled | 策略 |
| `alarm_events` | id, policy_id, device_id, zone_id, type, priority, state, call_id, occurred_at, answered_at, ended_at, closed_at, disposition, note, location_json, chain_hash | 事件 |
| `alarm_timeline` | id, alarm_id, ts, actor, action, detail_json | 时间线 |
| `holidays` | date, name | 节假日 |

索引：`alarm_events(state, occurred_at)`、`alarm_events(device_id, occurred_at)`。

### 6.3 通话与录音

| 表 | 关键字段 | 说明 |
| --- | --- | --- |
| `cdr` | id, call_id, direction, caller, callee, device_id, trunk_id, start_at, answer_at, end_at, duration, bill_duration, result, cause, recording_id | 话单 |
| `recordings` | id, call_id, alarm_id, path, duration_ms, size, sha256, chain_hash, channels, sample_rate, created_at, expires_at | 录音 |
| `unlock_logs` | id, device_id, user_id, call_id, result, ts | 开锁记录 |
| `visitor_logs` | id, device_id, user_id, snapshot_path, ts | 访客记录 |

索引：`cdr(start_at)`、`cdr(caller)`、`recordings(alarm_id)`、`recordings(expires_at)`。

### 6.4 广播

| 表 | 关键字段 | 说明 |
| --- | --- | --- |
| `broadcast_tasks` | id, name, type, zone_ids_json, source_type, media_id, text, voice, priority, schedule_cron, holiday_skip, volume, enabled, created_by | 任务 |
| `broadcast_sessions` | id, task_id, zone_id, priority, state, started_at, ended_at, operator, preempted_by, stats_json | 会话 |
| `broadcast_endpoints` | id, session_id, device_id, state, started_at, ended_at, error | 逐终端 |
| `media_files` | id, name, category, path, duration_ms, size, sha256, sample_rate, created_at | 音频库 |
| `broadcast_logs` | id, session_id, ts, action, detail_json | 日志 |

索引：`broadcast_sessions(state, started_at)`、`broadcast_endpoints(session_id, state)`。

### 6.5 平台

| 表 | 关键字段 | 说明 |
| --- | --- | --- |
| `users` | id, username, password_hash, role_id, zone_scope_json, enabled, last_login, totp_secret_enc | 用户 |
| `roles` | id, name, permissions_json | 角色 |
| `audit_logs` | id, user_id, action, target, ip, ua, detail_json, created_at | 审计 |
| `webhooks` | id, url, secret_enc, events_json, enabled | 订阅 |
| `webhook_deliveries` | id, webhook_id, event_id, status, attempts, last_error, next_retry, created_at | 投递 |
| `settings` | key, value | 系统配置 |
| `licenses` | id, edition, max_devices, max_calls, max_broadcast, max_video, expires_at, fingerprint, raw, activated_at | 许可 |
| `tasks` | id, type, state, progress, result_json, created_at | 异步任务 |

---

## 7. 关键字段与状态

### 7.1 报警状态

`created` → `dispatching` → `ringing` → `answered` → `on_call` → `ended` → `pending_disposition` → `closed`；分支 `rejected`、`timeout`、`all_failed`。

### 7.2 广播状态

`created` → `preparing` → `starting` → `running` → `stopping` → `finished`；异常 `partial_failed`、`preempted`、`failed`。

### 7.3 设备状态

`online`、`offline`、`disabled`、`error`（鉴权失败/配置错误）。

### 7.4 通话结果

`answered`、`no_answer`、`busy`、`rejected`、`canceled`、`failed`、`timeout`。

---

## 8. OpenAPI 与客户端

- 服务端启动生成 OpenAPI 3.1：`GET /api/v1/openapi.json`。
- 前端 TypeScript 客户端由 OpenAPI 生成（`openapi-typescript`），避免手写类型漂移。
- 变更规则：新增字段兼容；删除/改语义升 `v2`；Webhook 事件版本在信封中加 `version`。

---

## 9. 配置样例（节选）

```yaml
license:
  file: "./data/license/license.json"
  grace_days: 7
alarm:
  dedupe_window_s: 30
  default_ring_timeout_s: 20
  max_escalation_levels: 3
broadcast:
  max_concurrent_sessions: 4
  invite_rate_per_s: 50
  default_mode: "unicast"     # unicast | multicast
  multicast_base: "239.255.10.0"
record:
  default_enabled: false
  alarm_enabled: true
  intercom_enabled: true
  segment_seconds: 300
retention:
  cdr_days: 365
  recording_days: 90
  event_days: 365
  audit_days: 180
webhook:
  timeout_s: 5
  retries: [1, 5, 30, 300, 1800]
```

---

## 10. 待确认

1. `sip_web` 现有接口的字段与鉴权方式（用于设备配置下发）。
2. alarmserver-go 的事件格式与回写指令（ack/close）。
3. 广播终端型号与自动应答头、组播能力。
4. 是否需要 MQTT 双向（平台向 MCS 下发广播/处置指令）。
5. 许可文件字段是否需绑定项目/客户名称。

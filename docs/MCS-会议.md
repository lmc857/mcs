# MCS 电话会议

## 1. 配置与网页创建

会议房间**不需要手工改 config.yaml**。管理员登录后：

```text
业务 → 会议 → 创建房间
```

填写短号、名称、方数、是否录音即可，保存到 SQLite，立即生效。

`config.yaml` 中的 `conference.rooms` 只是首次启动的初始房间：

```yaml
conference:
  enabled: true
  rooms:
    - number: "7000"
      name: "会议室"
      max_members: 8
      record: true
```

如果数据库里已经有房间，网页/数据库是唯一数据源，YAML 的 rooms 不再覆盖。

> `conference.enabled` 只控制 **YAML 初始房间是否导入**；网页创建的房间不要求先在 YAML 中配置房间。没有房间时，管理员登录后直接在“会议”页创建即可。

话机直接拨房间短号，例如：

```text
7000
```

即可加入会议。

### 会议短号不是寻呼短号

- 会议短号：如 `7000`，双向混音，多人互相听；
- 广播寻呼短号：如 `5001`/`5002`，单向 RTP 扇出，终端只收不发；
- 两者可以同时存在，但号码不能重复；
- 会议短号也不能和分机号、报警号码 `8000` 冲突。

## 2. 当前能力

- 最多 8 方（可配置 1-16）；
- PCMU / PCMA 8kHz 音频；
- 服务器混音，每方听其他方，不听自己；
- 网页会议页实时显示参与者；
- operator/admin 可静音、取消静音、踢出；
- 可选会议混音录音，结束后生成 WAV；
- 会议房间由网页创建并持久化到 SQLite；
- H.264 视频：只转发当前发言者的视频，不做多画面合成；
- WebRTC 网页加入：麦克风/摄像头 + 会议混音音频 + 当前发言人视频；
- 无 PIN、无主持人角色。

## 3. 网页账号

“会议”页使用的是 **MCS 控制台账号**，不是 SIP 分机账号：

- 打开控制台时，如果未登录会自动弹出登录框；
- 默认用户名通常是 `admin`；
- 首次安装密码在 `<data_dir>/initial-admin.txt`，或配置里的 `auth.console_password`；
- 登录后右上角显示 `admin · admin`，才能静音/踢人；
- 话机拨会议短号 `7000` 加入会议，不需要网页账号。

新增控制台账号：配置 → 用户管理（仅 admin）。

## 4. 网页

业务 → 会议：

- 管理员：创建、编辑、启用/停用、删除房间；
- 房间号、在线人数；
- 参与者分机、名称、编码、加入时间；
- 静音/取消静音、踢出；
- 会议结束后显示录音链接。

## 5. API

```text
GET  /api/v1/conference
POST /api/v1/conference/rooms
PATCH /api/v1/conference/rooms/{number}
DELETE /api/v1/conference/rooms/{number}
POST /api/v1/conference/{room}/participants/{id}/mute
POST /api/v1/conference/{room}/participants/{id}/kick
```

mute：

```json
{"muted": true}
```

创建房间：

```json
{"number":"7001","name":"会议室 2","max_members":8,"record":true}
```

更新/停用：

```json
{"name":"会议室 2","max_members":10,"record":true,"enabled":true}
```

房间有参会者时不能删除；停用后新呼叫不再进入，已有参会者继续到结束。

viewer 只读；operator/admin 可静音、取消静音、踢人；房间创建、修改、启停、删除仅 admin。

## 6. 视频与 WebRTC

### SIP 视频终端

会议 INVITE 带 H.264 视频时，服务器会在 SDP 中返回视频端口：

- 只支持 H264；
- 不做视频混流，只转发**当前发言者**的视频；
- 发言者由音频 RTP 活跃度决定，没有音频时使用第一个视频源；
- 视频 RTP 只改写 payload type，不做转码；
- 其他参会者需自己协商 H264，否则只能听音频；
- 不支持 SIP 视频 SRTP。

### WebRTC 网页加入

会议房间右侧“网页加入”：

1. 浏览器请求麦克风和摄像头；
2. 使用 PCMU/PCMA 音频 + H264 视频；
3. 网页音频进入混音，网页视频可成为当前视频源；
4. 网页接收其他发言者的混音音频和当前视频。

要求：

- 浏览器必须支持 WebRTC H264 和 PCMU/PCMA；
- 网页音视频需要 **HTTPS**，或在本机使用 `http://127.0.0.1` / `http://localhost`；
- 局域网其他电脑用 `http://192.168.x.x:8080` 打开时，浏览器会禁止麦克风/摄像头；
- 跨 NAT/公网可在系统设置配置 STUN/TURN；未配置时只有 host candidate。

API：

```text
POST /api/v1/conference/{room}/webrtc
Content-Type: application/json

{"sdp":"..."}
```

返回：

```json
{"sdp":"...","id":"web-admin-..."}
```

需要 operator/admin 登录；viewer 只读。

## 7. 媒体说明

- 每方独立 UDP RTP 端口；
- 20ms / 160 samples 混音周期；
- 混音为饱和加法，未做 AGC/降噪/抖动缓冲高级处理；
- 录音为双声道 WAV（左右相同混音），存入 `data/recordings/YYYY/MM/DD/`；
- 会议音频是 PCMU/PCMA；不支持 Opus/SRTP 会议；
- 视频是单路发言人转发，不是多画面 MCU。

## 8. 边界

- 设备需要支持标准 SIP INVITE 和 PCMU/PCMA；视频需要 H264；
- WebRTC 需要 HTTPS 或本机安全上下文；
- 会议号码不能和分机、广播短号、报警号码冲突；
- 会议短号不是寻呼短号；
- 会议通话不计入两方 CDR；
- 当前会议没有 PIN 和主持人控制，任何能拨入的终端都可加入；
- 生产使用建议配合网络访问控制。

## 9. 后续

- 会议 PIN、主持人、邀请；
- WebRTC 加入会议；
- Opus/SRTP；
- 会议 CDR、参会人统计；
- 视频会议。

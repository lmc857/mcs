# MCS 电话会议

## 1. 配置

会议默认关闭，在 `config.yaml` 启用：

```yaml
conference:
  enabled: true
  rooms:
    - number: "7000"
      name: "会议室"
      max_members: 8
      record: true
```

重启 MCS 后，话机直接拨：

```text
7000
```

即可加入会议。

## 2. 当前能力

- 最多 8 方（可配置 1-16）；
- PCMU / PCMA 8kHz 音频；
- 服务器混音，每方听其他方，不听自己；
- 网页会议页实时显示参与者；
- operator/admin 可静音、取消静音、踢出；
- 可选会议混音录音，结束后生成 WAV；
- 会议房间由 `config.yaml` 定义；
- 无 PIN、无主持人角色、无 WebRTC 加入、无视频。

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

- 房间号、在线人数；
- 参与者分机、名称、编码、加入时间；
- 静音/取消静音、踢出；
- 会议结束后显示录音链接。

## 5. API

```text
GET  /api/v1/conference
POST /api/v1/conference/{room}/participants/{id}/mute
POST /api/v1/conference/{room}/participants/{id}/kick
```

mute：

```json
{"muted": true}
```

viewer 只读；operator/admin 可操作。

## 6. 媒体说明

- 每方独立 UDP RTP 端口；
- 20ms / 160 samples 混音周期；
- 混音为饱和加法，未做 AGC/降噪/抖动缓冲高级处理；
- 录音为双声道 WAV（左右相同混音），存入 `data/recordings/YYYY/MM/DD/`；
- 不支持 SRTP 会议、Opus 会议、视频会议。

## 7. 边界

- 设备需要支持标准 SIP INVITE 和 PCMU/PCMA；
- 会议号码不能和分机、广播短号、报警号码冲突；
- 会议通话不计入两方 CDR；
- 当前会议没有 PIN 和主持人控制，任何能拨入的终端都可加入；
- 生产使用建议配合网络访问控制。

## 8. 后续

- 会议 PIN、主持人、邀请；
- WebRTC 加入会议；
- Opus/SRTP；
- 会议 CDR、参会人统计；
- 视频会议。

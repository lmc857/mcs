# sipgo 冒烟验证（20 行）

用途：在正式立项前，用最小代码验证 **Go + sipgo 能收发真实 SIP 消息**，避免"协议栈选型"停留在纸面。

## 已验证（2026-11-16）

- 环境：macOS arm64 / Go 1.24.3 / sipgo v1.7.3
- 编译通过，UDP 监听 `127.0.0.1:15060`
- 发送 `REGISTER` 后收到 `200 OK`，响应头正确回填：

```text
SIP/2.0 200 OK
Via: SIP/2.0/UDP 127.0.0.1:5099;branch=z9hG4bK2;rport=56386;received=127.0.0.1
From: <sip:1001@127.0.0.1>;tag=2
To: <sip:1001@127.0.0.1>;tag=ff2584b8-bad6-494f-a4ae-43a45688a7a2
Call-ID: smoke2
CSeq: 1 REGISTER
Content-Length: 0
```

## 运行

```bash
cd poc/sipgo-smoke
go mod download
go run .            # 监听 udp/127.0.0.1:15060
```

另开终端发送 REGISTER（或直接用 Linphone 把服务器地址设为 `127.0.0.1:15060`）：

```bash
python3 - <<'PY'
import socket
msg = (
"REGISTER sip:127.0.0.1:15060 SIP/2.0\r\n"
"Via: SIP/2.0/UDP 127.0.0.1:5099;branch=z9hG4bK2;rport\r\n"
"Max-Forwards: 70\r\n"
"From: <sip:1001@127.0.0.1>;tag=2\r\n"
"To: <sip:1001@127.0.0.1>\r\n"
"Call-ID: smoke2\r\n"
"CSeq: 1 REGISTER\r\n"
"Contact: <sip:1001@127.0.0.1:5099>\r\n"
"Expires: 60\r\n"
"Content-Length: 0\r\n\r\n"
)
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM); s.settimeout(3)
s.sendto(msg.encode(), ("127.0.0.1", 15060))
print(s.recvfrom(4096)[0].decode(errors="replace"))
PY
```

## 注意

- 仅用于验证协议栈，**没有鉴权、没有注册存储、没有呼叫处理**，不能当服务器用。
- 正式项目需在 `internal/sipcore` 中封装 sipgo，锁定版本，并补齐 Digest、事务重传、NAT、TLS 等。

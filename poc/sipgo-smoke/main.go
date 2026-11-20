// 冒烟验证：用 sipgo 接收 REGISTER 并回 200 OK。
// 仅用于验证协议栈与 NAT 字段（received/rport），不含鉴权、注册存储与呼叫处理。
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

func main() {
	ua, err := sipgo.NewUA()
	if err != nil {
		panic(err)
	}
	srv, err := sipgo.NewServer(ua)
	if err != nil {
		panic(err)
	}
	srv.OnRegister(func(req *sip.Request, tx sip.ServerTransaction) {
		fmt.Println("REGISTER from", req.Source())
		_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
	})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	fmt.Println("listening udp/127.0.0.1:15060")
	if err := srv.ListenAndServe(ctx, "udp", "127.0.0.1:15060"); err != nil {
		fmt.Println("server stopped:", err)
	}
}

// uasim registers a set of SIP accounts against MCS for local development.
// It is a test utility, not part of the product.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

func main() {
	var (
		server   = flag.String("server", "127.0.0.1:5060", "MCS SIP address host:port")
		accounts = flag.String("accounts", "1001:pass-1001,1002:pass-1002", "ext:password,...")
		domain   = flag.String("domain", "mcs.local", "SIP domain")
		ttl      = flag.Int("ttl", 60, "registration expires seconds")
	)
	flag.Parse()

	host, port := splitHostPort(*server)
	ua, err := sipgo.NewUA(sipgo.WithUserAgent("MCS-UASim/0.1"))
	if err != nil {
		panic(err)
	}
	defer ua.Close()
	client, err := sipgo.NewClient(ua)
	if err != nil {
		panic(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	type account struct{ ext, pass string }
	var list []account
	for _, part := range strings.Split(*accounts, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		ext, pass, ok := strings.Cut(part, ":")
		if !ok {
			fmt.Fprintf(os.Stderr, "bad account %q, want ext:password\n", part)
			os.Exit(2)
		}
		list = append(list, account{ext: ext, pass: pass})
	}
	if len(list) == 0 {
		fmt.Fprintln(os.Stderr, "no accounts")
		os.Exit(2)
	}

	recipient := sip.Uri{Scheme: "sip", Host: host, Port: port}
	for _, acc := range list {
		if err := register(ctx, client, recipient, *domain, acc.ext, acc.pass, *ttl); err != nil {
			fmt.Printf("[%s] register failed: %v\n", acc.ext, err)
			continue
		}
		fmt.Printf("[%s] registered\n", acc.ext)
	}

	ticker := time.NewTicker(time.Duration(*ttl) * time.Second / 2)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			for _, acc := range list {
				_ = unregister(context.Background(), client, recipient, *domain, acc.ext, acc.pass)
			}
			fmt.Println("bye")
			return
		case <-ticker.C:
			for _, acc := range list {
				if err := register(ctx, client, recipient, *domain, acc.ext, acc.pass, *ttl); err != nil {
					fmt.Printf("[%s] refresh failed: %v\n", acc.ext, err)
				}
			}
			fmt.Printf("refreshed %d accounts\n", len(list))
		}
	}
}

func buildRegister(recipient sip.Uri, domain, ext, pass string, ttl int) *sip.Request {
	req := sip.NewRequest(sip.REGISTER, recipient)
	req.AppendHeader(sip.NewHeader("From", fmt.Sprintf("<sip:%s@%s>;tag=uasim-%d", ext, domain, time.Now().UnixNano())))
	req.AppendHeader(sip.NewHeader("To", fmt.Sprintf("<sip:%s@%s>", ext, domain)))
	req.AppendHeader(sip.NewHeader("Call-ID", fmt.Sprintf("uasim-%s-%d", ext, time.Now().UnixNano())))
	req.AppendHeader(sip.NewHeader("CSeq", "1 REGISTER"))
	req.AppendHeader(sip.NewHeader("Contact", fmt.Sprintf("<sip:%s@127.0.0.1:5099>", ext)))
	req.AppendHeader(sip.NewHeader("Expires", fmt.Sprintf("%d", ttl)))
	req.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	req.AppendHeader(sip.NewHeader("User-Agent", "MCS-UASim/0.1"))
	req.AppendHeader(sip.NewHeader("X-MCS-Model", "UASIM"))
	req.AppendHeader(sip.NewHeader("X-MCS-Firmware", "sim-1.0"))
	return req
}

func register(ctx context.Context, client *sipgo.Client, recipient sip.Uri, domain, ext, pass string, ttl int) error {
	req := buildRegister(recipient, domain, ext, pass, ttl)
	res, err := client.Do(ctx, req)
	if err != nil {
		return err
	}
	if res.StatusCode == sip.StatusUnauthorized {
		res, err = client.DoDigestAuth(ctx, req, res, sipgo.DigestAuth{Username: ext, Password: pass})
		if err != nil {
			return err
		}
	}
	if res.StatusCode != sip.StatusOK {
		return fmt.Errorf("status %d", res.StatusCode)
	}
	return nil
}

func unregister(ctx context.Context, client *sipgo.Client, recipient sip.Uri, domain, ext, pass string) error {
	return register(ctx, client, recipient, domain, ext, pass, 0)
}

func splitHostPort(addr string) (string, int) {
	host := addr
	port := 5060
	if i := strings.LastIndex(addr, ":"); i > 0 {
		host = addr[:i]
		fmt.Sscanf(addr[i+1:], "%d", &port)
	}
	return host, port
}

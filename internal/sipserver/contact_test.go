package sipserver

import (
	"testing"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

func TestContactForTransport(t *testing.T) {
	s := &Server{
		advertiseHost: "192.168.68.106", advertisePort: "5060",
		sipUDPPort: "5060", sipTCPPort: "5060", sipTLSPort: "5061",
	}
	tls := s.contactForTransport("tls")
	if tls.Address.Scheme != "sips" || tls.Address.Port != 5061 {
		t.Fatalf("TLS contact = %s", tls.Address.String())
	}
	if tp, _ := tls.Address.UriParams.Get("transport"); tp != "tls" {
		t.Fatalf("TLS transport = %q", tp)
	}
	udp := s.contactForTransport("udp")
	if udp.Address.Scheme != "sip" || udp.Address.Port != 5060 {
		t.Fatalf("UDP contact = %s", udp.Address.String())
	}
	if tp, _ := udp.Address.UriParams.Get("transport"); tp != "udp" {
		t.Fatalf("UDP transport = %q", tp)
	}
}

func TestALegContactUsesIncomingTransport(t *testing.T) {
	s := &Server{
		advertiseHost: "192.168.68.106", advertisePort: "5060",
		sipUDPPort: "5060", sipTCPPort: "5060", sipTLSPort: "5061",
	}
	req := sip.NewRequest(sip.INVITE, sip.Uri{Scheme: "sips", User: "1005", Host: "192.168.68.106"})
	req.SetTransport("TLS")
	c := &call{server: s, a: &sipgo.DialogServerSession{}}
	c.a.InviteRequest = req
	contact := c.aContactHeader()
	if contact.Address.Scheme != "sips" || contact.Address.Port != 5061 {
		t.Fatalf("A-leg TLS contact = %s", contact.Address.String())
	}
}

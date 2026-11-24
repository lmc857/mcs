package sipserver

import (
	"testing"

	"github.com/emiago/sipgo/sip"

	"mcs/internal/store"
)

func TestContactURIPreservesTLSTransport(t *testing.T) {
	v := store.View{
		Contact:   `<sip:1001@127.0.0.1:5061>;transport=tls;ob`,
		Transport: "tls",
	}
	uri, err := contactURIForView(v)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := uri.UriParams.Get("transport"); got != "tls" {
		t.Fatalf("transport param = %q", got)
	}
	req := sip.NewRequest(sip.INVITE, uri)
	if got := req.Transport(); got != "TLS" {
		t.Fatalf("request transport = %q, want TLS", got)
	}
}

func TestContactURIAddsRegisteredTransport(t *testing.T) {
	v := store.View{Contact: `<sip:1001@127.0.0.1:5061>`, Transport: "tls"}
	uri, err := contactURIForView(v)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := uri.UriParams.Get("transport"); got != "tls" {
		t.Fatalf("transport param = %q", got)
	}
	req := sip.NewRequest(sip.INVITE, uri)
	if got := req.Transport(); got != "TLS" {
		t.Fatalf("request transport = %q, want TLS", got)
	}
}

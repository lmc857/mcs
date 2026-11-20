package sipserver

import "testing"

// RFC 2617 §3.5 example.
func TestDigestResponseRFC2617(t *testing.T) {
	got := DigestResponse(
		"Mufasa",
		"Circle Of Life",
		"testrealm@host.com",
		"dcd98b7102dd2f0e8b11d0f600bfb0c093",
		"/dir/index.html",
		"GET",
		"auth",
		"00000001",
		"0a4f113b",
		"MD5",
	)
	want := "6629fae49393a05397450978507c4ef1"
	if got != want {
		t.Fatalf("digest response = %s, want %s", got, want)
	}
}

func TestParseAuthorization(t *testing.T) {
	h := `Digest username="1001", realm="mcs", nonce="abc", uri="sip:mcs.local", response="deadbeef", algorithm=MD5, qop=auth, nc=00000001, cnonce="xyz"`
	p := ParseAuthorization(h)
	if p["username"] != "1001" || p["nonce"] != "abc" || p["qop"] != "auth" || p["cnonce"] != "xyz" {
		t.Fatalf("unexpected params: %#v", p)
	}
}

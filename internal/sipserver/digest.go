package sipserver

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hash"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Authenticator issues and verifies SIP Digest challenges (RFC 7616 subset).
// Nonces are stateless: timestamp + HMAC, so restarts do not need shared state.
type Authenticator struct {
	Realm  string
	Secret string
	TTL    time.Duration
}

func NewAuthenticator(realm, secret string) *Authenticator {
	if realm == "" {
		realm = "mcs"
	}
	return &Authenticator{Realm: realm, Secret: secret, TTL: 5 * time.Minute}
}

func (a *Authenticator) Challenge() string {
	ts := time.Now().Unix()
	mac := hmac.New(sha256.New, []byte(a.Secret))
	fmt.Fprintf(mac, "%d", ts)
	raw := fmt.Sprintf("%d:%x", ts, mac.Sum(nil))
	nonce := base64.RawURLEncoding.EncodeToString([]byte(raw))
	return fmt.Sprintf(`Digest realm=%q, nonce=%q, algorithm=MD5, qop="auth"`, a.Realm, nonce)
}

func (a *Authenticator) validNonce(nonce string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(nonce)
	if err != nil {
		return false
	}
	parts := strings.SplitN(string(raw), ":", 2)
	if len(parts) != 2 {
		return false
	}
	ts, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return false
	}
	age := time.Since(time.Unix(ts, 0))
	if age < -time.Minute || age > a.TTL {
		return false
	}
	mac := hmac.New(sha256.New, []byte(a.Secret))
	fmt.Fprintf(mac, "%d", ts)
	want := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(parts[1]), []byte(want))
}

var authParamRE = regexp.MustCompile(`([a-zA-Z0-9_-]+)\s*=\s*(?:"([^"]*)"|([^,\s]+))`)

// ParseAuthorization extracts Digest parameters from an Authorization header value.
func ParseAuthorization(value string) map[string]string {
	value = strings.TrimSpace(value)
	if len(value) >= 6 && strings.EqualFold(value[:6], "digest") {
		value = strings.TrimSpace(value[6:])
	}
	params := map[string]string{}
	for _, m := range authParamRE.FindAllStringSubmatch(value, -1) {
		if m[2] != "" {
			params[strings.ToLower(m[1])] = m[2]
		} else {
			params[strings.ToLower(m[1])] = m[3]
		}
	}
	return params
}

// Verify checks a Digest Authorization header against the expected user/password.
func (a *Authenticator) Verify(header, method, requestURI, ext, password string) bool {
	p := ParseAuthorization(header)
	if p["username"] == "" || p["response"] == "" || p["nonce"] == "" {
		return false
	}
	if p["username"] != ext {
		return false
	}
	if p["realm"] != "" && p["realm"] != a.Realm {
		return false
	}
	if !a.validNonce(p["nonce"]) {
		return false
	}
	uri := p["uri"]
	if uri == "" {
		uri = requestURI
	}
	got := DigestResponse(p["username"], password, a.Realm, p["nonce"], uri, method,
		p["qop"], p["nc"], p["cnonce"], p["algorithm"])
	if got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(strings.ToLower(got)), []byte(strings.ToLower(p["response"]))) == 1
}

// DigestResponse computes the RFC 2617/7616 response value.
func DigestResponse(username, password, realm, nonce, uri, method, qop, nc, cnonce, algorithm string) string {
	alg := strings.ToUpper(strings.TrimSpace(algorithm))
	if alg == "" {
		alg = "MD5"
	}
	var h func() hash.Hash
	switch alg {
	case "MD5", "MD5-SESS":
		h = md5.New
	case "SHA-256", "SHA-256-SESS":
		h = sha256.New
	default:
		return ""
	}
	H := func(parts ...string) string {
		hh := h()
		for i, p := range parts {
			if i > 0 {
				hh.Write([]byte(":"))
			}
			hh.Write([]byte(p))
		}
		return hex.EncodeToString(hh.Sum(nil))
	}

	ha1 := H(username, realm, password)
	if strings.HasSuffix(alg, "-SESS") {
		ha1 = H(ha1, nonce, cnonce)
	}
	ha2 := H(method, uri)
	if strings.EqualFold(qop, "auth-int") {
		// Not used by SIP clients in this project; keep explicit.
		return ""
	}
	if qop != "" {
		return H(ha1, nonce, nc, cnonce, qop, ha2)
	}
	return H(ha1, nonce, ha2)
}

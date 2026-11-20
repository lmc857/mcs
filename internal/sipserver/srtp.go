package sipserver

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/pion/srtp/v3"
)

type cryptoInfo struct {
	Suite string
	Key   string // base64 master key + salt
	OK    bool
}

type srtpKey struct {
	MasterKey []byte
	Salt      []byte
	Profile   srtp.ProtectionProfile
	Suite     string
	SDP       string // value for a=crypto: (tag suite inline:key)
}

func newSRTPKey() (*srtpKey, error) {
	return newSRTPKeyFor("AES_CM_128_HMAC_SHA1_80")
}

func newSRTPKeyFor(suite string) (*srtpKey, error) {
	profile := srtp.ProtectionProfileAes128CmHmacSha1_80
	if suite == "AES_CM_128_HMAC_SHA1_32" {
		profile = srtp.ProtectionProfileAes128CmHmacSha1_32
	} else if suite != "AES_CM_128_HMAC_SHA1_80" {
		return nil, fmt.Errorf("unsupported crypto suite %q", suite)
	}
	key := make([]byte, 16)
	salt := make([]byte, 14)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	raw := append(append([]byte(nil), key...), salt...)
	return &srtpKey{
		MasterKey: key, Salt: salt, Profile: profile, Suite: suite,
		SDP: "1 " + suite + " inline:" + base64.StdEncoding.EncodeToString(raw),
	}, nil
}

func parseSRTPKey(c cryptoInfo) (*srtpKey, error) {
	if !c.OK {
		return nil, fmt.Errorf("missing crypto attribute")
	}
	profile := srtp.ProtectionProfileAes128CmHmacSha1_80
	switch c.Suite {
	case "AES_CM_128_HMAC_SHA1_80":
	case "AES_CM_128_HMAC_SHA1_32":
		profile = srtp.ProtectionProfileAes128CmHmacSha1_32
	default:
		return nil, fmt.Errorf("unsupported crypto suite %q", c.Suite)
	}
	raw, err := base64.StdEncoding.DecodeString(c.Key)
	if err != nil {
		return nil, fmt.Errorf("bad crypto key: %w", err)
	}
	if len(raw) < 30 {
		return nil, fmt.Errorf("crypto key too short")
	}
	return &srtpKey{
		MasterKey: raw[:16], Salt: raw[16:30], Profile: profile, Suite: c.Suite,
	}, nil
}

func (k *srtpKey) context() (*srtp.Context, error) {
	if k == nil {
		return nil, nil
	}
	return srtp.CreateContext(k.MasterKey, k.Salt, k.Profile)
}

func parseCryptoAttr(value string) cryptoInfo {
	fields := strings.Fields(value)
	if len(fields) < 3 {
		return cryptoInfo{}
	}
	suite := fields[1]
	key := ""
	for _, f := range fields[2:] {
		if strings.HasPrefix(f, "inline:") {
			key = strings.TrimPrefix(f, "inline:")
			if i := strings.IndexByte(key, '|'); i >= 0 {
				key = key[:i]
			}
			break
		}
	}
	if key == "" {
		return cryptoInfo{}
	}
	return cryptoInfo{Suite: suite, Key: key, OK: true}
}

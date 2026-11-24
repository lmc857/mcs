package license

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const trialPhones = 4

// The corresponding private key is kept outside the repository and is used by
// cmd/mcs-license to issue activation codes.
const defaultPublicKeyHex = "91800338aae8e640ef06c3a41c1ead6b8b4d570974fe52541ad13b9def8714d6"

type Payload struct {
	Version   int    `json:"v"`
	LicenseID string `json:"license_id"`
	Customer  string `json:"customer"`
	MaxPhones int    `json:"max_phones"`
	IssuedAt  string `json:"issued_at,omitempty"`
}

type Info struct {
	LicenseID   string `json:"license_id"`
	Customer    string `json:"customer"`
	MaxPhones   int    `json:"max_phones"`
	IssuedAt    string `json:"issued_at,omitempty"`
	Status      string `json:"status"`
	Activated   bool   `json:"activated"`
	ActivatedAt string `json:"activated_at,omitempty"`
}

type Manager struct {
	mu   sync.RWMutex
	pub  ed25519.PublicKey
	info Info
}

func DefaultPublicKey() ed25519.PublicKey {
	pub, err := hex.DecodeString(defaultPublicKeyHex)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil
	}
	return ed25519.PublicKey(pub)
}

func New(pub ed25519.PublicKey, trial int) *Manager {
	if trial <= 0 {
		trial = trialPhones
	}
	return &Manager{pub: pub, info: Info{MaxPhones: trial, Status: "trial", Customer: "未授权"}}
}

func (m *Manager) Info() Info {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.info
}

func (m *Manager) MaxPhones() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.info.MaxPhones <= 0 {
		return 0
	}
	return m.info.MaxPhones
}

func (m *Manager) Activate(code string) (Info, error) {
	info, err := Decode(code, m.pub)
	if err != nil {
		return m.Info(), err
	}
	if info.MaxPhones <= 0 || info.MaxPhones > 100000 {
		return m.Info(), errors.New("授权话机数量无效")
	}
	info.Status = "licensed"
	info.Activated = true
	info.ActivatedAt = time.Now().Format(time.RFC3339)
	m.mu.Lock()
	m.info = info
	m.mu.Unlock()
	return info, nil
}

func (m *Manager) SetInfo(info Info) {
	m.mu.Lock()
	m.info = info
	m.mu.Unlock()
}

func Decode(code string, pub ed25519.PublicKey) (Info, error) {
	code = strings.TrimSpace(code)
	parts := strings.Split(code, ".")
	if len(parts) != 2 {
		return Info{}, errors.New("激活码格式错误")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Info{}, fmt.Errorf("激活码内容错误: %w", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return Info{}, errors.New("激活码签名错误")
	}
	if !ed25519.Verify(pub, raw, sig) {
		return Info{}, errors.New("激活码无效或不是本产品授权码")
	}
	var p Payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return Info{}, errors.New("激活码内容无法解析")
	}
	if p.Version != 1 {
		return Info{}, errors.New("激活码版本不支持")
	}
	if strings.TrimSpace(p.Customer) == "" {
		return Info{}, errors.New("激活码缺少客户名称")
	}
	if p.MaxPhones <= 0 {
		return Info{}, errors.New("激活码缺少话机数量")
	}
	return Info{
		LicenseID: p.LicenseID, Customer: p.Customer, MaxPhones: p.MaxPhones,
		IssuedAt: p.IssuedAt,
	}, nil
}

func Encode(p Payload, priv ed25519.PrivateKey) (string, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return "", errors.New("私钥格式错误")
	}
	if p.Version == 0 {
		p.Version = 1
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	sig := ed25519.Sign(priv, raw)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// ParsePrivateKey accepts base64/hex raw Ed25519 private keys, a 32-byte
// base64 seed, or PKCS#8 PEM.
func ParsePrivateKey(data []byte) (ed25519.PrivateKey, error) {
	text := strings.TrimSpace(string(data))
	if text == "" {
		return nil, errors.New("私钥为空")
	}
	if strings.Contains(text, "BEGIN") {
		block, _ := pem.Decode(data)
		if block == nil {
			return nil, errors.New("私钥 PEM 格式错误")
		}
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.New("私钥 PEM 不是 PKCS#8")
		}
		ed, ok := key.(ed25519.PrivateKey)
		if !ok {
			return nil, errors.New("私钥不是 Ed25519")
		}
		return ed, nil
	}
	if b, err := base64.StdEncoding.DecodeString(text); err == nil && len(b) == ed25519.PrivateKeySize {
		return ed25519.PrivateKey(b), nil
	}
	if b, err := hex.DecodeString(text); err == nil && len(b) == ed25519.PrivateKeySize {
		return ed25519.PrivateKey(b), nil
	}
	if b, err := base64.StdEncoding.DecodeString(text); err == nil && len(b) == ed25519.SeedSize {
		return ed25519.NewKeyFromSeed(b), nil
	}
	return nil, errors.New("私钥必须是 base64/hex 64 字节或 Ed25519 PEM")
}

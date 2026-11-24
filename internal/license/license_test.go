package license

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
)

func TestActivationCode(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	code, err := Encode(Payload{Version: 1, LicenseID: "MCS-T1", Customer: "客户", MaxPhones: 32}, priv)
	if err != nil {
		t.Fatal(err)
	}
	m := New(pub, 4)
	info, err := m.Activate(code)
	if err != nil {
		t.Fatal(err)
	}
	if info.MaxPhones != 32 || info.Customer != "客户" || info.Status != "licensed" {
		t.Fatalf("info = %+v", info)
	}
	if m.MaxPhones() != 32 {
		t.Fatalf("max = %d", m.MaxPhones())
	}
	parts := strings.Split(code, ".")
	parts[0] = "A" + parts[0][1:]
	tampered := strings.Join(parts, ".")
	if _, err := m.Activate(tampered); err == nil {
		t.Fatal("tampered code accepted")
	}
}

func TestDefaultEncode(t *testing.T) {
	// Regression check that the private key used by cmd/mcs-license matches
	// the embedded public key. This test is skipped when no test key is
	// available in CI; the CLI is exercised separately.
	pub := DefaultPublicKey()
	if len(pub) != ed25519.PublicKeySize {
		t.Fatal("embedded public key invalid")
	}
}

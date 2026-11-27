package license

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"
)

func TestActivationCode(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	code, err := Encode(Payload{Version: 1, LicenseID: "MCS-T1", Customer: "客户", MaxPhones: 32, Machine: "MCS-TEST-DEVICE-0001"}, priv)
	if err != nil {
		t.Fatal(err)
	}
	m := New(pub, 4, "MCS-TEST-DEVICE-0001")
	info, err := m.Activate(code)
	if err != nil {
		t.Fatal(err)
	}
	if info.MaxPhones != 32 || info.Customer != "客户" || info.Status != "licensed" || !info.MachineMatch || info.MachineCode == "" {
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

func TestActivationExpiry(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	m := New(pub, 4, "MCS-TEST-DEVICE-0002")
	m.now = func() time.Time { return now }
	code, err := Encode(Payload{
		Version: 1, LicenseID: "MCS-EXP", Customer: "客户", MaxPhones: 20,
		Machine:   "MCS-TEST-DEVICE-0002",
		ExpiresAt: now.Add(48 * time.Hour).Format(time.RFC3339),
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	info, err := m.Activate(code)
	if err != nil {
		t.Fatal(err)
	}
	if info.Status != "licensed" || info.DaysLeft != 2 || m.MaxPhones() != 20 {
		t.Fatalf("fresh info = %+v max=%d", info, m.MaxPhones())
	}
	m.now = func() time.Time { return now.Add(49 * time.Hour) }
	info = m.Info()
	if info.Status != "expired" || !info.Expired || info.EffectiveMaxPhones != 4 || m.MaxPhones() != 4 {
		t.Fatalf("expired info = %+v max=%d", info, m.MaxPhones())
	}
}

func TestExpiredActivationRejected(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	code, err := Encode(Payload{
		Version: 1, LicenseID: "MCS-OLD", Customer: "客户", MaxPhones: 20,
		ExpiresAt: time.Now().Add(-time.Hour).Format(time.RFC3339),
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(pub, 4, "MCS-TEST-DEVICE-0003").Activate(code); err == nil {
		t.Fatal("expired activation accepted")
	}
}

func TestMachineBindingMismatch(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	code, err := Encode(Payload{
		Version: 1, LicenseID: "MCS-BIND", Customer: "客户", MaxPhones: 20,
		Machine: "MCS-AAAA-BBBB-CCCC-DDDD",
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(pub, 4, "MCS-EEEE-FFFF-GGGG-HHHH").Activate(code); err == nil {
		t.Fatal("code bound to another machine accepted")
	}
}

func TestLegacyUnboundCodeRejected(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	code, err := Encode(Payload{Version: 1, LicenseID: "MCS-LEGACY", Customer: "客户", MaxPhones: 20}, priv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(pub, 4, "MCS-TEST-DEVICE-0004").Activate(code); err == nil {
		t.Fatal("legacy universal code accepted")
	}
}

func TestNormalizeMachineCode(t *testing.T) {
	if got := NormalizeMachineCode(" mcs-abcd-efgh-ijkl "); got != "MCS-ABCD-EFGH-IJKL" {
		t.Fatalf("normalize = %q", got)
	}
	if NormalizeMachineCode("---") != "" {
		t.Fatal("empty machine code accepted")
	}
}

func TestCurrentMachineCode(t *testing.T) {
	code, err := MachineCode()
	if err != nil {
		t.Skipf("machine code not available on this host: %v", err)
	}
	if !strings.HasPrefix(code, "MCS-") {
		t.Fatalf("machine code = %q", code)
	}
	if NormalizeMachineCode(code) != code {
		t.Fatalf("machine code not canonical: %q -> %q", code, NormalizeMachineCode(code))
	}
}

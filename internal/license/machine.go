package license

import (
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"runtime"
	"strings"
)

// MachineCode returns a stable, privacy-preserving machine fingerprint that
// the vendor signs into an activation code. The raw hardware identifiers never
// leave the server; only this grouped Base32 hash is displayed.
func MachineCode() (string, error) {
	parts, err := machineFingerprintParts()
	if err != nil {
		return "", err
	}
	return codeFromParts(parts), nil
}

func codeFromParts(parts []string) string {
	payload := runtime.GOOS + "|" + strings.Join(parts, "|")
	sum := sha256.Sum256([]byte("mcs-machine-v1|" + payload))
	raw := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:16])
	raw = strings.ToUpper(raw)
	var groups []string
	for len(raw) > 4 {
		groups = append(groups, raw[:4])
		raw = raw[4:]
	}
	if raw != "" {
		groups = append(groups, raw)
	}
	return "MCS-" + strings.Join(groups, "-")
}

// NormalizeMachineCode accepts a displayed machine code with lower case,
// spaces or missing dashes and returns its canonical form. It never accepts
// an empty value as a valid machine binding.
func NormalizeMachineCode(code string) string {
	s := strings.ToUpper(strings.TrimSpace(code))
	s = strings.NewReplacer("-", "", " ", "", "_", "").Replace(s)
	if strings.HasPrefix(s, "MCS") {
		s = strings.TrimPrefix(s, "MCS")
	}
	if s == "" {
		return ""
	}
	var groups []string
	for len(s) > 4 {
		groups = append(groups, s[:4])
		s = s[4:]
	}
	if s != "" {
		groups = append(groups, s)
	}
	return "MCS-" + strings.Join(groups, "-")
}

func machinePartsError() error {
	return fmt.Errorf("无法读取本机唯一标识；请使用 Windows/Linux/macOS 正式安装环境")
}

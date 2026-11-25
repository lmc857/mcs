//go:build darwin

package license

import (
	"os/exec"
	"strings"
)

func machineFingerprintParts() ([]string, error) {
	out, err := exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
	if err != nil {
		return nil, machinePartsError()
	}
	var parts []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		for _, key := range []string{"IOPlatformUUID", "IOPlatformSerialNumber"} {
			prefix := `"` + key + `" = `
			if strings.HasPrefix(line, prefix) {
				v := strings.TrimPrefix(line, prefix)
				v = strings.Trim(v, `"`)
				if v != "" {
					parts = append(parts, strings.ToLower(key)+"="+v)
				}
			}
		}
	}
	if len(parts) == 0 {
		return nil, machinePartsError()
	}
	return parts, nil
}

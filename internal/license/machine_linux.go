//go:build linux

package license

import (
	"os"
	"strings"
)

func machineFingerprintParts() ([]string, error) {
	var parts []string
	for _, path := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		if b, err := os.ReadFile(path); err == nil {
			if v := strings.TrimSpace(string(b)); v != "" {
				parts = append(parts, "machine-id="+v)
				break
			}
		}
	}
	if b, err := os.ReadFile("/sys/class/dmi/id/product_uuid"); err == nil {
		if v := strings.TrimSpace(string(b)); v != "" {
			parts = append(parts, "product-uuid="+v)
		}
	}
	if len(parts) == 0 {
		return nil, machinePartsError()
	}
	return parts, nil
}

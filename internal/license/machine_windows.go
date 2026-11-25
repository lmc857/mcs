//go:build windows

package license

import (
	"fmt"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func machineFingerprintParts() ([]string, error) {
	var parts []string

	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Cryptography`, registry.QUERY_VALUE)
	if err == nil {
		if guid, _, err := key.GetStringValue("MachineGuid"); err == nil && guid != "" {
			parts = append(parts, "machine-guid="+guid)
		}
		_ = key.Close()
	}
	bios, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\BIOS`, registry.QUERY_VALUE)
	if err == nil {
		if uuid, _, err := bios.GetStringValue("SystemUUID"); err == nil && uuid != "" {
			parts = append(parts, "system-uuid="+uuid)
		}
		_ = bios.Close()
	}
	root, err := windows.UTF16PtrFromString(`C:\`)
	if err == nil {
		var name [261]uint16
		var fs [261]uint16
		var serial, maxComponent, flags uint32
		if err := windows.GetVolumeInformation(root, &name[0], uint32(len(name)), &serial, &maxComponent, &flags, &fs[0], uint32(len(fs))); err == nil {
			parts = append(parts, fmt.Sprintf("system-volume=%08X", serial))
		}
	}
	if len(parts) == 0 {
		return nil, machinePartsError()
	}
	return parts, nil
}

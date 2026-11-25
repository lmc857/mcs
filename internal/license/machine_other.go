//go:build !windows && !linux && !darwin

package license

func machineFingerprintParts() ([]string, error) {
	return nil, machinePartsError()
}

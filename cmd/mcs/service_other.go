//go:build !windows

package main

import "fmt"

func runWindowsService(configPath, logPath string) error {
	return fmt.Errorf("Windows service mode is only available on Windows")
}

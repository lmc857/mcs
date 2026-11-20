//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"
)

type mcsService struct {
	configPath string
	logPath    string
}

func writeServiceError(logPath string, err error) {
	dir := filepath.Dir(logPath)
	if logPath == "" {
		dir = `C:\ProgramData\MCS`
	}
	_ = os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, "service-error.log")
	line := fmt.Sprintf("%s service run error: %v\n", time.Now().Format(time.RFC3339), err)
	_ = os.WriteFile(path, []byte(line), 0o644)
}

func runWindowsService(configPath, logPath string) error {
	return svc.Run("MCS", &mcsService{configPath: configPath, logPath: logPath})
}

func (m *mcsService) Execute(args []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runServer(ctx, m.configPath, m.logPath) }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}

	for {
		select {
		case err := <-done:
			if err != nil {
				writeServiceError(m.logPath, err)
				return true, 1
			}
			return false, 0
		case req := <-requests:
			switch req.Cmd {
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				<-done
				return false, 0
			}
		}
	}
}

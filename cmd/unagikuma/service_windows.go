//go:build windows

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// runAsService runs the command as a Windows service when the service
// manager started us: baseCtx ends when the service is told to stop.
func runAsService(cmd func() error) (bool, error) {
	is, err := svc.IsWindowsService()
	if err != nil || !is {
		return false, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	baseCtx = ctx
	h := &serviceHandler{run: cmd, cancel: cancel}
	return true, svc.Run("unagikuma", h)
}

type serviceHandler struct {
	run    func() error
	cancel context.CancelFunc
}

func (h *serviceHandler) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	done := make(chan error, 1)
	go func() { done <- h.run() }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-done:
			if err != nil {
				return true, 1
			}
			return false, 0
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				h.cancel()
				select {
				case <-done:
				case <-time.After(20 * time.Second):
				}
				return false, 0
			}
		}
	}
}

// runService installs or removes a Windows service running this binary
// with the given arguments, e.g.
//
//	unagikuma service install --name unagikuma-agent -- agent --server ... --log-file C:\unagikuma\agent.log
func runService(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: unagikuma service install|uninstall [--name NAME] [-- COMMAND ARGS...]")
	}
	sub := args[0]
	fs := flag.NewFlagSet("service "+sub, flag.ExitOnError)
	name := fs.String("name", "unagikuma", "service name")
	_ = fs.Parse(args[1:])
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("service manager (run as Administrator): %w", err)
	}
	defer m.Disconnect()
	switch sub {
	case "install":
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		exe, _ = filepath.Abs(exe)
		cmdArgs := fs.Args()
		if len(cmdArgs) == 0 {
			cmdArgs = []string{"server"}
		}
		s, err := m.CreateService(*name, exe, mgr.Config{
			DisplayName: "unagikuma (" + cmdArgs[0] + ")", Description: "unagikuma monitoring", StartType: mgr.StartAutomatic,
		}, cmdArgs...)
		if err != nil {
			return err
		}
		defer s.Close()
		_ = s.SetRecoveryActions([]mgr.RecoveryAction{{Type: mgr.ServiceRestart, Delay: 5 * time.Second}}, 86400)
		if err := s.Start(); err != nil {
			return fmt.Errorf("installed, but could not start: %w", err)
		}
		fmt.Printf("service %s installed and started\n", *name)
	case "uninstall":
		s, err := m.OpenService(*name)
		if err != nil {
			return err
		}
		defer s.Close()
		_, _ = s.Control(svc.Stop)
		if err := s.Delete(); err != nil {
			return err
		}
		fmt.Printf("service %s removed\n", *name)
	default:
		return fmt.Errorf("unknown service command %q", sub)
	}
	return nil
}

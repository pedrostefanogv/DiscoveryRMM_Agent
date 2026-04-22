//go:build windows

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sys/windows/svc"
)

const windowsServiceName = "DiscoveryAgent"

func tryRunWindowsService(logFile string) (bool, error) {
	inService, err := svc.IsWindowsService()
	if err != nil {
		return false, fmt.Errorf("falha ao detectar contexto do Windows Service: %w", err)
	}
	if !inService {
		return false, nil
	}

	return true, svc.Run(windowsServiceName, &windowsServiceHandler{logFile: logFile})
}

func newConsoleServiceContext() (context.Context, context.CancelFunc, error) {
	ctx, cancel := context.WithCancel(context.Background())
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		fmt.Printf("[SERVICE] Recebido sinal: %v, encerrando...\n", sig)
		signal.Stop(sigChan)
		close(sigChan)
		cancel()
	}()

	return ctx, cancel, nil
}

type windowsServiceHandler struct {
	logFile string
}

func (handler *windowsServiceHandler) Execute(args []string, requests <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown

	changes <- svc.Status{State: svc.StartPending, WaitHint: 15000}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- runServiceRuntime(ctx, handler.logFile)
	}()

	changes <- svc.Status{State: svc.Running, Accepts: accepted}

	for {
		select {
		case err := <-errChan:
			if err != nil {
				log.Printf("[SERVICE] runtime finalizado com erro: %v", err)
				return false, 1
			}
			return false, 0
		case req := <-requests:
			switch req.Cmd {
			case svc.Interrogate:
				changes <- req.CurrentStatus
			case svc.Stop, svc.Shutdown:
				changes <- svc.Status{State: svc.StopPending, WaitHint: 15000}
				cancel()
				err := <-errChan
				if err != nil {
					log.Printf("[SERVICE] erro ao encerrar runtime: %v", err)
					return false, 1
				}
				return false, 0
			default:
				changes <- req.CurrentStatus
			}
		}
	}
}

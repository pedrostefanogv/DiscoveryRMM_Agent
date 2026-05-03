package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	appkg "discovery/app"
	"discovery/internal/service"
)

// ServiceMode indica se a aplicação está rodando como Windows Service
var ServiceMode = false
var serviceLogWriter io.Closer

// runAsService inicia a aplicação no modo Windows Service (headless, sem UI Wails)
// Argumentos esperados: discovery-agent.exe --service [--log-file=path]
func runAsService(logFile string) error {
	ServiceMode = true
	if handled, err := tryRunWindowsService(logFile); handled {
		return err
	}
	return runServiceConsole(logFile)
}

func runServiceConsole(logFile string) error {
	ctx, stop, err := newConsoleServiceContext()
	if err != nil {
		return err
	}
	defer stop()

	errChan := make(chan error, 1)
	go func() {
		errChan <- runServiceRuntime(ctx, logFile)
	}()

	select {
	case err := <-errChan:
		if err != nil {
			fmt.Fprintf(os.Stderr, "[SERVICE] Service error: %v\n", err)
		}
		return err
	case <-ctx.Done():
		err := <-errChan
		if err != nil {
			fmt.Fprintf(os.Stderr, "[SERVICE] Service error: %v\n", err)
			return err
		}
		fmt.Println("[SERVICE] Service encerrado com sucesso")
		return nil
	}
}

func runServiceRuntime(ctx context.Context, logFile string) error {
	log.Printf("[SERVICE] Iniciando Discovery Agent como Windows Service")

	// Criar diretório de dados (C:\ProgramData\Discovery)
	dataDir := appkg.GetDataDir()
	if err := ensureDataDir(dataDir); err != nil {
		log.Printf("[SERVICE] Erro ao criar diretório de dados: %v", err)
		return err
	}
	log.Printf("[SERVICE] Diretório de dados: %s", dataDir)

	// Inicializar logger para arquivo de log
	if logFile == "" {
		logFile = getDefaultLogFile()
	}
	initServiceLogger(logFile)

	log.Printf("[SERVICE] Criando ServiceManager...")
	svcMgr := newRuntimeServiceManager(dataDir)
	log.Printf("[SERVICE] ServiceManager criado, iniciando runtime...")
	return svcMgr.Start(ctx)
}

func newRuntimeServiceManager(dataDir string) *service.ServiceManager {
	svcMgr := service.NewServiceManager(dataDir)
	p2pSvc := appkg.NewHeadlessP2PService(func(line string) {
		log.Printf("[SERVICE.P2P] %s", line)
	})
	svcMgr.SetAgentRuntime(service.NewAgentRuntimeService(svcMgr.GetConfig, func(line string) {
		log.Printf("[SERVICE.Agent] %s", line)
	}, service.AgentRuntimeHooks{
		ReloadConfig:              svcMgr.ReloadConfig,
		RefreshAutomationPolicy:   svcMgr.RefreshAutomationPolicy,
		RequestSelfUpdateCheck:    svcMgr.RequestSelfUpdateCheck,
		ApplyP2PDiscoverySnapshot: p2pSvc.ApplyP2PDiscoverySnapshot,
	}))
	svcMgr.SetAutomationService(service.NewAutomationRuntimeService(svcMgr.GetConfig, func(line string) {
		log.Printf("[SERVICE.Automation] %s", line)
	}))
	svcMgr.SetInventoryService(service.NewInventoryRuntimeService(45*time.Second, svcMgr.GetConfig, func(line string) {
		log.Printf("[SERVICE.Inventory] %s", line)
	}, appkg.Version))
	svcMgr.SetAppsService(service.NewAppsRuntimeService(10 * time.Minute))
	svcMgr.SetP2PService(p2pSvc)
	return svcMgr
}

func getDefaultLogFile() string {
	dataDir := appkg.GetDataDir()
	return dataDir + "\\logs\\discovery-service.log"
}

func initServiceLogger(logFile string) {
	if err := os.MkdirAll(filepath.Dir(logFile), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "[SERVICE] falha ao criar pasta de log: %v\n", err)
		return
	}

	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[SERVICE] falha ao abrir arquivo de log: %v\n", err)
		return
	}

	serviceLogWriter = f
	log.SetOutput(io.MultiWriter(os.Stdout, f))
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("[SERVICE] logger inicializado em %s", logFile)
}

func ensureDataDir(dataDir string) error {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("falha ao criar dataDir %s: %w", dataDir, err)
	}

	for _, subdir := range []string{"logs"} {
		full := filepath.Join(dataDir, subdir)
		if err := os.MkdirAll(full, 0o755); err != nil {
			return fmt.Errorf("falha ao criar subdiretorio %s: %w", full, err)
		}
	}

	return nil
}

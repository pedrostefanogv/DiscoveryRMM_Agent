package main

import (
	"context"
	"discovery/internal/service"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

// ServiceMode indica se a aplicação está rodando como Windows Service
var ServiceMode = false
var serviceLogWriter io.Closer

// runAsService inicia a aplicação no modo Windows Service (headless, sem UI Wails)
// Argumentos esperados: discovery.exe --service [--log-file=path]
func runAsService(logFile string) error {
	ServiceMode = true

	fmt.Println("[SERVICE] Iniciando Discovery Agent como Windows Service")

	// Criar diretório de dados (C:\ProgramData\Discovery)
	dataDir := getDataDir()
	if err := ensureDataDir(dataDir); err != nil {
		fmt.Fprintf(os.Stderr, "[SERVICE] Erro ao criar diretório de dados: %v\n", err)
		return err
	}

	// Inicializar logger para arquivo de log
	if logFile == "" {
		logFile = getDefaultLogFile()
	}
	initServiceLogger(logFile)

	// Criar gerenciador de serviço
	svcMgr := service.NewServiceManager(dataDir)

	// Contexto cancelável para shutdown gracioso
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Canal de sinais (Ctrl+C, SIGTERM)
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Iniciar serviço em goroutine
	errChan := make(chan error, 1)
	go func() {
		errChan <- svcMgr.Start(ctx)
	}()

	fmt.Println("[SERVICE] Service iniciado com sucesso")

	// Aguardar shutdown
	select {
	case err := <-errChan:
		fmt.Fprintf(os.Stderr, "[SERVICE] Service error: %v\n", err)
		return err
	case sig := <-sigChan:
		fmt.Printf("[SERVICE] Recebido sinal: %v, encerrando...\n", sig)
		cancel()
		<-errChan // Aguardar shutdown completo
		fmt.Println("[SERVICE] Service encerrado com sucesso")
		return nil
	}
}

func getDefaultLogFile() string {
	dataDir := getDataDir()
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

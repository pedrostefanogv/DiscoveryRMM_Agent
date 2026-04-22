package services

import (
	"context"
	"encoding/json"
)

type PrinterProvider interface {
	ListPrinters(ctx context.Context) (json.RawMessage, error)
	InstallPrinter(ctx context.Context, name, driverName, portName, portAddress string) (json.RawMessage, error)
	InstallSharedPrinter(ctx context.Context, connectionPath string, setDefault bool) (json.RawMessage, error)
	RemovePrinter(ctx context.Context, name string) (json.RawMessage, error)
	GetPrinterConfig(ctx context.Context, name string) (json.RawMessage, error)
	ListPrintJobs(ctx context.Context, printerName string) (json.RawMessage, error)
	RemovePrintJob(ctx context.Context, printerName string, jobID int) (json.RawMessage, error)
	GetSpoolerStatus(ctx context.Context) (json.RawMessage, error)
	RestartSpooler(ctx context.Context) (json.RawMessage, error)
	ClearQueue(ctx context.Context, printerName string) (json.RawMessage, error)
	ListDrivers(ctx context.Context) (json.RawMessage, error)
}

type PrinterService struct {
	provider PrinterProvider
}

func NewPrinterService(provider PrinterProvider) *PrinterService {
	return &PrinterService{provider: provider}
}

func (s *PrinterService) ListPrinters(ctx context.Context) (json.RawMessage, error) {
	return s.provider.ListPrinters(ctx)
}

func (s *PrinterService) InstallPrinter(ctx context.Context, name, driverName, portName, portAddress string) (json.RawMessage, error) {
	return s.provider.InstallPrinter(ctx, name, driverName, portName, portAddress)
}

func (s *PrinterService) InstallSharedPrinter(ctx context.Context, connectionPath string, setDefault bool) (json.RawMessage, error) {
	return s.provider.InstallSharedPrinter(ctx, connectionPath, setDefault)
}

func (s *PrinterService) RemovePrinter(ctx context.Context, name string) (json.RawMessage, error) {
	return s.provider.RemovePrinter(ctx, name)
}

func (s *PrinterService) GetPrinterConfig(ctx context.Context, name string) (json.RawMessage, error) {
	return s.provider.GetPrinterConfig(ctx, name)
}

func (s *PrinterService) ListPrintJobs(ctx context.Context, printerName string) (json.RawMessage, error) {
	return s.provider.ListPrintJobs(ctx, printerName)
}

func (s *PrinterService) RemovePrintJob(ctx context.Context, printerName string, jobID int) (json.RawMessage, error) {
	return s.provider.RemovePrintJob(ctx, printerName, jobID)
}

func (s *PrinterService) GetSpoolerStatus(ctx context.Context) (json.RawMessage, error) {
	return s.provider.GetSpoolerStatus(ctx)
}

func (s *PrinterService) RestartSpooler(ctx context.Context) (json.RawMessage, error) {
	return s.provider.RestartSpooler(ctx)
}

func (s *PrinterService) ClearQueue(ctx context.Context, printerName string) (json.RawMessage, error) {
	return s.provider.ClearQueue(ctx, printerName)
}

func (s *PrinterService) ListDrivers(ctx context.Context) (json.RawMessage, error) {
	return s.provider.ListDrivers(ctx)
}

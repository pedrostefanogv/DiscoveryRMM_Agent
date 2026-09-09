package app

import (
	"encoding/json"
	"fmt"
	"time"
)

func (a *App) ListPrinters() (json.RawMessage, error) {
	done := a.beginActivity("listagem de impressoras")
	defer done()
	result, err := a.PrinterSvc.ListPrinters(a.ctx)
	a.Logs.Append("[printer list] " + time.Now().Format("15:04:05"))
	a.Logs.Append(string(result))
	return result, err
}

func (a *App) InstallPrinter(name, driverName, portName, portAddress string) (json.RawMessage, error) {
	done := a.beginActivity("instalação de impressora")
	defer done()
	result, err := a.PrinterSvc.InstallPrinter(a.ctx, name, driverName, portName, portAddress)
	a.Logs.Append("[printer install " + name + "] " + time.Now().Format("15:04:05"))
	a.Logs.Append(string(result))
	return result, err
}

func (a *App) InstallSharedPrinter(connectionPath string, setDefault bool) (json.RawMessage, error) {
	done := a.beginActivity("instalação de impressora compartilhada")
	defer done()
	result, err := a.PrinterSvc.InstallSharedPrinter(a.ctx, connectionPath, setDefault)
	a.Logs.Append("[printer install shared " + connectionPath + "] " + time.Now().Format("15:04:05"))
	a.Logs.Append(string(result))
	return result, err
}

func (a *App) RemovePrinter(name string) (json.RawMessage, error) {
	done := a.beginActivity("remocao de impressora")
	defer done()
	result, err := a.PrinterSvc.RemovePrinter(a.ctx, name)
	a.Logs.Append("[printer remove " + name + "] " + time.Now().Format("15:04:05"))
	a.Logs.Append(string(result))
	return result, err
}

func (a *App) GetPrinterConfig(name string) (json.RawMessage, error) {
	done := a.beginActivity("configuração de impressora")
	defer done()
	result, err := a.PrinterSvc.GetPrinterConfig(a.ctx, name)
	a.Logs.Append("[printer config " + name + "] " + time.Now().Format("15:04:05"))
	a.Logs.Append(string(result))
	return result, err
}

func (a *App) ListPrintJobs(printerName string) (json.RawMessage, error) {
	done := a.beginActivity("fila de impressao")
	defer done()
	result, err := a.PrinterSvc.ListPrintJobs(a.ctx, printerName)
	a.Logs.Append("[printer jobs " + printerName + "] " + time.Now().Format("15:04:05"))
	a.Logs.Append(string(result))
	return result, err
}

func (a *App) RemovePrintJob(printerName string, jobID int) (json.RawMessage, error) {
	done := a.beginActivity("cancelamento de job de impressao")
	defer done()
	result, err := a.PrinterSvc.RemovePrintJob(a.ctx, printerName, jobID)
	a.Logs.Append(fmt.Sprintf("[printer remove job %s #%d] %s", printerName, jobID, time.Now().Format("15:04:05")))
	a.Logs.Append(string(result))
	return result, err
}

func (a *App) GetSpoolerStatus() (json.RawMessage, error) {
	done := a.beginActivity("status do spooler")
	defer done()
	result, err := a.PrinterSvc.GetSpoolerStatus(a.ctx)
	a.Logs.Append("[printer spooler status] " + time.Now().Format("15:04:05"))
	a.Logs.Append(string(result))
	return result, err
}

func (a *App) RestartSpooler() (json.RawMessage, error) {
	done := a.beginActivity("reinicio do spooler")
	defer done()
	result, err := a.PrinterSvc.RestartSpooler(a.ctx)
	a.Logs.Append("[printer spooler restart] " + time.Now().Format("15:04:05"))
	a.Logs.Append(string(result))
	return result, err
}

func (a *App) ClearPrintQueue(printerName string) (json.RawMessage, error) {
	done := a.beginActivity("limpeza da fila de impressao")
	defer done()
	result, err := a.PrinterSvc.ClearQueue(a.ctx, printerName)
	a.Logs.Append("[printer clear queue " + printerName + "] " + time.Now().Format("15:04:05"))
	a.Logs.Append(string(result))
	return result, err
}

func (a *App) ListPrinterDrivers() (json.RawMessage, error) {
	done := a.beginActivity("listagem de drivers de impressora")
	defer done()
	result, err := a.PrinterSvc.ListDrivers(a.ctx)
	a.Logs.Append("[printer drivers] " + time.Now().Format("15:04:05"))
	a.Logs.Append(string(result))
	return result, err
}

func (a *App) ListPrintersJSON() (json.RawMessage, error) { return a.ListPrinters() }

func (a *App) InstallPrinterJSON(name, driverName, portName, portAddress string) (json.RawMessage, error) {
	return a.InstallPrinter(name, driverName, portName, portAddress)
}

func (a *App) InstallSharedPrinterJSON(connectionPath string, setDefault bool) (json.RawMessage, error) {
	return a.InstallSharedPrinter(connectionPath, setDefault)
}

func (a *App) RemovePrinterJSON(name string) (json.RawMessage, error) {
	return a.RemovePrinter(name)
}

func (a *App) GetPrinterConfigJSON(name string) (json.RawMessage, error) {
	return a.GetPrinterConfig(name)
}

func (a *App) ListPrintJobsJSON(printerName string) (json.RawMessage, error) {
	return a.ListPrintJobs(printerName)
}

func (a *App) RemovePrintJobJSON(printerName string, jobID int) (json.RawMessage, error) {
	return a.RemovePrintJob(printerName, jobID)
}

func (a *App) GetSpoolerStatusJSON() (json.RawMessage, error) {
	return a.GetSpoolerStatus()
}

func (a *App) RestartSpoolerJSON() (json.RawMessage, error) {
	return a.RestartSpooler()
}

func (a *App) ClearPrintQueueJSON(printerName string) (json.RawMessage, error) {
	return a.ClearPrintQueue(printerName)
}

func (a *App) ListPrinterDriversJSON() (json.RawMessage, error) {
	return a.ListPrinterDrivers()
}

package app

import (
	"encoding/json"
	"fmt"
	"time"
)

func (a *App) ListPrinters() (json.RawMessage, error) {
	done := a.beginActivity("listagem de impressoras")
	defer done()
	result, err := a.printerSvc.ListPrinters(a.ctx)
	a.logs.append("[printer list] " + time.Now().Format("15:04:05"))
	a.logs.append(string(result))
	return result, err
}

func (a *App) InstallPrinter(name, driverName, portName, portAddress string) (json.RawMessage, error) {
	done := a.beginActivity("instalacao de impressora")
	defer done()
	result, err := a.printerSvc.InstallPrinter(a.ctx, name, driverName, portName, portAddress)
	a.logs.append("[printer install " + name + "] " + time.Now().Format("15:04:05"))
	a.logs.append(string(result))
	return result, err
}

func (a *App) InstallSharedPrinter(connectionPath string, setDefault bool) (json.RawMessage, error) {
	done := a.beginActivity("instalacao de impressora compartilhada")
	defer done()
	result, err := a.printerSvc.InstallSharedPrinter(a.ctx, connectionPath, setDefault)
	a.logs.append("[printer install shared " + connectionPath + "] " + time.Now().Format("15:04:05"))
	a.logs.append(string(result))
	return result, err
}

func (a *App) RemovePrinter(name string) (json.RawMessage, error) {
	done := a.beginActivity("remocao de impressora")
	defer done()
	result, err := a.printerSvc.RemovePrinter(a.ctx, name)
	a.logs.append("[printer remove " + name + "] " + time.Now().Format("15:04:05"))
	a.logs.append(string(result))
	return result, err
}

func (a *App) GetPrinterConfig(name string) (json.RawMessage, error) {
	done := a.beginActivity("configuracao de impressora")
	defer done()
	result, err := a.printerSvc.GetPrinterConfig(a.ctx, name)
	a.logs.append("[printer config " + name + "] " + time.Now().Format("15:04:05"))
	a.logs.append(string(result))
	return result, err
}

func (a *App) ListPrintJobs(printerName string) (json.RawMessage, error) {
	done := a.beginActivity("fila de impressao")
	defer done()
	result, err := a.printerSvc.ListPrintJobs(a.ctx, printerName)
	a.logs.append("[printer jobs " + printerName + "] " + time.Now().Format("15:04:05"))
	a.logs.append(string(result))
	return result, err
}

func (a *App) RemovePrintJob(printerName string, jobID int) (json.RawMessage, error) {
	done := a.beginActivity("cancelamento de job de impressao")
	defer done()
	result, err := a.printerSvc.RemovePrintJob(a.ctx, printerName, jobID)
	a.logs.append(fmt.Sprintf("[printer remove job %s #%d] %s", printerName, jobID, time.Now().Format("15:04:05")))
	a.logs.append(string(result))
	return result, err
}

func (a *App) GetSpoolerStatus() (json.RawMessage, error) {
	done := a.beginActivity("status do spooler")
	defer done()
	result, err := a.printerSvc.GetSpoolerStatus(a.ctx)
	a.logs.append("[printer spooler status] " + time.Now().Format("15:04:05"))
	a.logs.append(string(result))
	return result, err
}

func (a *App) RestartSpooler() (json.RawMessage, error) {
	done := a.beginActivity("reinicio do spooler")
	defer done()
	result, err := a.printerSvc.RestartSpooler(a.ctx)
	a.logs.append("[printer spooler restart] " + time.Now().Format("15:04:05"))
	a.logs.append(string(result))
	return result, err
}

func (a *App) ClearPrintQueue(printerName string) (json.RawMessage, error) {
	done := a.beginActivity("limpeza da fila de impressao")
	defer done()
	result, err := a.printerSvc.ClearQueue(a.ctx, printerName)
	a.logs.append("[printer clear queue " + printerName + "] " + time.Now().Format("15:04:05"))
	a.logs.append(string(result))
	return result, err
}

func (a *App) ListPrinterDrivers() (json.RawMessage, error) {
	done := a.beginActivity("listagem de drivers de impressora")
	defer done()
	result, err := a.printerSvc.ListDrivers(a.ctx)
	a.logs.append("[printer drivers] " + time.Now().Format("15:04:05"))
	a.logs.append(string(result))
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

package mcp

import (
	"context"
	"fmt"
)

// registerPrinterTool registra a tool de familia "printer", que consolida as
// antigas tools soltas de impressora. As acoes destrutivas validam confirm=true
// via requireConfirm antes de tocar no AppBridge.
func registerPrinterTool(reg *Registry, app AppBridge) {
	reg.Register(Tool{
		Name: "printer",
		Description: "Gerencia impressoras no Windows (familia action-based). action: " +
			"list | install | install_shared | remove | config | jobs | cancel_job | spooler | restart_spooler | clear_queue | drivers. " +
			"DESTRUTIVAS (exigem confirm=true apos aprovacao do usuario): remove, cancel_job, restart_spooler, clear_queue.",
		Params: []ToolParam{
			{Name: "action", Type: "string", Description: "Acao: list, install, install_shared, remove, config, jobs, cancel_job, spooler, restart_spooler, clear_queue, drivers", Required: true},
			{Name: "name", Type: "string", Description: "Nome da impressora (install, remove, config, jobs, cancel_job, clear_queue)", Required: false},
			{Name: "driverName", Type: "string", Description: "Driver de impressao (install)", Required: false},
			{Name: "portName", Type: "string", Description: "Porta local ou TCP/IP (install)", Required: false},
			{Name: "portAddress", Type: "string", Description: "IP ou hostname para criar a porta TCP/IP (install)", Required: false},
			{Name: "jobId", Type: "integer", Description: "ID numerico do job de impressao (cancel_job)", Required: false},
			{Name: "connectionPath", Type: "string", Description: "Caminho UNC da impressora compartilhada (install_shared)", Required: false},
			{Name: "setDefault", Type: "boolean", Description: "Se true, define a impressora como padrao (install_shared)", Required: false},
			{Name: "confirm", Type: "boolean", Description: "Confirmacao explicita do usuario para acoes destrutivas", Required: false},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			action, err := requireAction(args, "list", "install", "install_shared", "remove", "config", "jobs", "cancel_job", "spooler", "restart_spooler", "clear_queue", "drivers")
			if err != nil {
				return nil, err
			}
			switch action {
			case "list":
				return app.ListPrintersJSON()
			case "install":
				name, err := requiredStringArg(args, "name")
				if err != nil {
					return nil, err
				}
				driverName, err := requiredStringArg(args, "driverName")
				if err != nil {
					return nil, err
				}
				portName, err := requiredStringArg(args, "portName")
				if err != nil {
					return nil, err
				}
				return app.InstallPrinterJSON(name, driverName, portName, optionalStringArg(args, "portAddress"))
			case "install_shared":
				connectionPath, err := requiredStringArg(args, "connectionPath")
				if err != nil {
					return nil, err
				}
				return app.InstallSharedPrinterJSON(connectionPath, optionalBoolArg(args, "setDefault"))
			case "remove":
				if err := requireConfirm(args); err != nil {
					return nil, err
				}
				name, err := requiredStringArg(args, "name")
				if err != nil {
					return nil, err
				}
				return app.RemovePrinterJSON(name)
			case "config":
				name, err := requiredStringArg(args, "name")
				if err != nil {
					return nil, err
				}
				return app.GetPrinterConfigJSON(name)
			case "jobs":
				name, err := requiredStringArg(args, "name")
				if err != nil {
					return nil, err
				}
				return app.ListPrintJobsJSON(name)
			case "cancel_job":
				if err := requireConfirm(args); err != nil {
					return nil, err
				}
				name, err := requiredStringArg(args, "name")
				if err != nil {
					return nil, err
				}
				jobID, err := requiredIntArg(args, "jobId")
				if err != nil {
					return nil, err
				}
				return app.RemovePrintJobJSON(name, jobID)
			case "spooler":
				return app.GetSpoolerStatusJSON()
			case "restart_spooler":
				if err := requireConfirm(args); err != nil {
					return nil, err
				}
				return app.RestartSpoolerJSON()
			case "clear_queue":
				if err := requireConfirm(args); err != nil {
					return nil, err
				}
				name, err := requiredStringArg(args, "name")
				if err != nil {
					return nil, err
				}
				return app.ClearPrintQueueJSON(name)
			case "drivers":
				return app.ListPrinterDriversJSON()
			}
			return nil, fmt.Errorf("action nao suportada: %s", action)
		},
	})
}

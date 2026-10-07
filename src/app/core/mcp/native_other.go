//go:build !windows

package mcp

import (
	"encoding/json"
	"errors"
)

// errWindowsOnly padroniza o erro dos recursos Windows-only no build nao-Windows.
var errWindowsOnly = errors.New("recurso disponivel apenas no Windows")

func listServicesNative() (json.RawMessage, error)     { return nil, errWindowsOnly }
func getServiceNative(string) (json.RawMessage, error) { return nil, errWindowsOnly }
func startServiceNative(string) error                  { return errWindowsOnly }
func stopServiceNative(string) error                   { return errWindowsOnly }
func restartServiceNative(string) error                { return errWindowsOnly }
func listProcessesNative() (json.RawMessage, error)    { return nil, errWindowsOnly }
func killProcessNative(uint32) error                   { return errWindowsOnly }
func systemInfoNative() map[string]any                 { return nil }
func connectionsNative(int) []map[string]any           { return nil }

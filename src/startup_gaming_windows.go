//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// suppressGameBarOverlay registra este executável como "não é um jogo" no
// GameConfigStore do Xbox Game Bar, evitando que o overlay apareça ao abrir o app.
// Replica o mesmo registro que o Windows cria quando o usuário desativa o
// reconhecimento de jogo manualmente via Configurações do Xbox Game Bar.
func suppressGameBarOverlay() {
	exePath, err := os.Executable()
	if err != nil {
		return
	}
	if resolved, err := filepath.EvalSymlinks(exePath); err == nil {
		exePath = resolved
	}

	const childrenPath = `System\GameConfigStore\Children`
	parent, _, err := registry.CreateKey(registry.CURRENT_USER, childrenPath, registry.ALL_ACCESS)
	if err != nil {
		return
	}
	defer parent.Close()

	exeLower := strings.ToLower(exePath)
	subkeys, _ := parent.ReadSubKeyNames(-1)

	// Se o exe já está registrado, garante Verdict=0 (não é jogo)
	for _, name := range subkeys {
		child, err := registry.OpenKey(parent, name, registry.QUERY_VALUE|registry.SET_VALUE)
		if err != nil {
			continue
		}
		fullPath, _, _ := child.GetStringValue("FullPath")
		if strings.ToLower(fullPath) == exeLower {
			child.SetDWordValue("Verdict", 0)
			child.SetDWordValue("GameDVR_Enabled", 0)
			child.Close()
			return
		}
		child.Close()
	}

	// Cria nova entrada marcando como não-jogo
	newName := fmt.Sprintf("%d", len(subkeys))
	child, _, err := registry.CreateKey(parent, newName, registry.SET_VALUE)
	if err != nil {
		return
	}
	defer child.Close()

	child.SetStringValue("FullPath", exePath)
	child.SetDWordValue("Verdict", 0)
	child.SetDWordValue("GameDVR_Enabled", 0)
	child.SetDWordValue("GameConfigStoreDataVersion", 0)
	child.SetDWordValue("GameStoreDataVersion", 0)
}

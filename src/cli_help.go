package main

import (
	"fmt"
	"os"
	"strings"

	"discovery/internal/buildinfo"
)

// showCLIHelp detecta flags de ajuda/versao e exibe a mensagem apropriada.
// Retorna true se uma flag foi detectada e o programa deve sair.
//
// REGRA DE PREFIXO: Nao misturar -- e /.
// Se o usuario passar --help e /debug juntos, o programa ignora as flags
// de ajuda e inicia normalmente. Isso evita ambiguidade de interpretacao.
func showCLIHelp() bool {
	args := os.Args[1:]
	if len(args) == 0 {
		return false
	}

	// Detecta quais familias de prefixo estao em uso
	hasDoubleDash := false
	hasSlash := false
	hasSingleDash := false

	for _, a := range args {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if strings.HasPrefix(a, "--") {
			hasDoubleDash = true
		} else if strings.HasPrefix(a, "/") {
			hasSlash = true
		} else if strings.HasPrefix(a, "-") {
			hasSingleDash = true
		}
	}

	// Mistura de prefixos: ignora flags de help/version e segue normalmente
	if (hasDoubleDash && hasSlash) || (hasDoubleDash && hasSingleDash) || (hasSlash && hasSingleDash) {
		// Unica excecao: se os unicos single-dash sao help/version, trata como modo single-dash
		if hasSlash && hasSingleDash {
			return false
		}
		if hasDoubleDash && hasSingleDash {
			// Se todos os single-dash forem -h/-help/-v/-version, trata como single-dash
			if allSingleDashAreHelp(args) {
				return handleSingleDash(args)
			}
			return false
		}
		return false
	}

	if hasDoubleDash {
		return handleDoubleDash(args)
	}
	if hasSlash {
		return handleSlash(args)
	}
	if hasSingleDash {
		return handleSingleDash(args)
	}

	return false
}

func allSingleDashAreHelp(args []string) bool {
	for _, a := range args {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") {
			lower := strings.ToLower(a)
			if lower != "-h" && lower != "-help" && lower != "-v" && lower != "-version" {
				return false
			}
		}
	}
	return true
}

func handleDoubleDash(args []string) bool {
	for _, a := range args {
		lower := strings.ToLower(strings.TrimSpace(a))
		switch lower {
		case "--help", "--h":
			printHelp("--")
			return true
		case "--version", "--v":
			printVersion()
			return true
		}
	}
	return false
}

func handleSlash(args []string) bool {
	for _, a := range args {
		lower := strings.ToLower(strings.TrimSpace(a))
		switch lower {
		case "/?", "/h", "/help":
			printHelp("/")
			return true
		case "/v", "/version":
			printVersion()
			return true
		}
	}
	return false
}

func handleSingleDash(args []string) bool {
	for _, a := range args {
		lower := strings.ToLower(strings.TrimSpace(a))
		switch lower {
		case "-h", "-help":
			printHelp("-")
			return true
		case "-v", "-version":
			printVersion()
			return true
		}
	}
	return false
}

func printVersion() {
	version := strings.TrimSpace(buildinfo.Version)
	commit := strings.TrimSpace(buildinfo.Commit)
	if version == "" {
		version = "0.0.0"
	}
	fmt.Printf("discovery-agent version %s commit %s\n", version, commit)
}

// printHelp exibe a ajuda formatada de acordo com o prefixo em uso.
// prefix indica qual familia de prefixos o usuario esta usando ("--", "/", "-").
func printHelp(prefix string) {
	version := strings.TrimSpace(buildinfo.Version)
	commit := strings.TrimSpace(buildinfo.Commit)
	if version == "" {
		version = "0.0.0"
	}

	fmt.Printf("Discovery Agent v%s (commit: %s)\n\n", version, commit)
	fmt.Println("Uso: discovery-agent.exe [opcoes]")
	fmt.Println()

	switch prefix {
	case "--":
		printHelpDoubleDash()
	case "/":
		printHelpSlash()
	case "-":
		printHelpSingleDash()
	default:
		printHelpDoubleDash()
	}
}

func printHelpDoubleDash() {
	fmt.Println("Opcoes (prefixo --):")
	fmt.Println()
	fmt.Println("  --help,  --h             Exibe esta ajuda")
	fmt.Println("  --version,  --v          Exibe a versao e sai")
	fmt.Println("  --debug                  Inicia com servidor HTTP de debug local")
	fmt.Println("  --startup-minimized      Inicia minimizado na bandeja do sistema")
	fmt.Println("  --startup-source=<s>     Define a origem da execucao (autostart, manual)")
	fmt.Println("  --windowed-frame         Forca janela com bordas padrao do Windows")
	fmt.Println("  --window-frame=<t>       Define o tipo de frame (standard, frameless)")
	fmt.Println("  --agent-delete-cleanup   Executa limpeza remota de descomissionamento e sai")
	fmt.Println()
	fmt.Println("Exemplos:")
	fmt.Println("  discovery-agent.exe --help")
	fmt.Println("  discovery-agent.exe --version")
	fmt.Println("  discovery-agent.exe --debug")
	fmt.Println("  discovery-agent.exe --startup-minimized --startup-source=autostart")
	fmt.Println("  discovery-agent.exe --agent-delete-cleanup")
	fmt.Println()
	fmt.Println("Nota: Nao misture prefixos (-- com / ou -). Use apenas um estilo.")
}

func printHelpSlash() {
	fmt.Println("Opcoes (prefixo /):")
	fmt.Println()
	fmt.Println("  /?,  /h,  /help          Exibe esta ajuda")
	fmt.Println("  /v,  /version            Exibe a versao e sai")
	fmt.Println("  /debug                   Inicia com servidor HTTP de debug local")
	fmt.Println("  /startup-minimized       Inicia minimizado na bandeja do sistema")
	fmt.Println("  /startup-source=<s>      Define a origem da execucao (autostart, manual)")
	fmt.Println("  /windowed-frame          Forca janela com bordas padrao do Windows")
	fmt.Println("  /window-frame=<t>        Define o tipo de frame (standard, frameless)")
	fmt.Println("  /agent-delete-cleanup    Executa limpeza remota de descomissionamento e sai")
	fmt.Println()
	fmt.Println("Exemplos:")
	fmt.Println("  discovery-agent.exe /?")
	fmt.Println("  discovery-agent.exe /v")
	fmt.Println("  discovery-agent.exe /debug")
	fmt.Println("  discovery-agent.exe /startup-minimized /startup-source=autostart")
	fmt.Println("  discovery-agent.exe /agent-delete-cleanup")
	fmt.Println()
	fmt.Println("Nota: Nao misture prefixos (/ com -- ou -). Use apenas um estilo.")
}

func printHelpSingleDash() {
	fmt.Println("Opcoes (prefixo -):")
	fmt.Println()
	fmt.Println("  -h,  -help               Exibe esta ajuda")
	fmt.Println("  -v,  -version            Exibe a versao e sai")
	fmt.Println("  -debug                   Inicia com servidor HTTP de debug local")
	fmt.Println("  -startup-minimized       Inicia minimizado na bandeja do sistema")
	fmt.Println("  -startup-source=<s>      Define a origem da execucao (autostart, manual)")
	fmt.Println("  -windowed-frame          Forca janela com bordas padrao do Windows")
	fmt.Println("  -window-frame=<t>        Define o tipo de frame (standard, frameless)")
	fmt.Println("  -agent-delete-cleanup    Executa limpeza remota de descomissionamento e sai")
	fmt.Println()
	fmt.Println("Exemplos:")
	fmt.Println("  discovery-agent.exe -h")
	fmt.Println("  discovery-agent.exe -v")
	fmt.Println("  discovery-agent.exe -debug")
	fmt.Println("  discovery-agent.exe -startup-minimized -startup-source=autostart")
	fmt.Println("  discovery-agent.exe -agent-delete-cleanup")
	fmt.Println()
	fmt.Println("Nota: Nao misture prefixos (- com -- ou /). Use apenas um estilo.")
}

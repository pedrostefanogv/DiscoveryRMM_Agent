//go:build windows

package screen

import (
	"fmt"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// sasPolicyKey é o caminho da política que controla o SendSAS.
const sasPolicyKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`

// sasPolicyValue lê o valor atual de SoftwareSASGeneration (0 quando ausente).
// Serve para o erro do SendSAS dizer EXATAMENTE o que está configurado no host
// remoto, em vez de só "recusado".
func sasPolicyValue() (uint32, bool) {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, sasPolicyKey, registry.QUERY_VALUE)
	if err != nil {
		return 0, false
	}
	defer key.Close()
	v, _, err := key.GetIntegerValue("SoftwareSASGeneration")
	if err != nil {
		return 0, false
	}
	return uint32(v), true
}

// describeSASPolicy resume a política para compor a mensagem de erro.
func describeSASPolicy() string {
	v, ok := sasPolicyValue()
	if !ok {
		return "SoftwareSASGeneration ausente (Windows trata como 0 = desabilitado)"
	}
	return fmt.Sprintf("SoftwareSASGeneration=%d", v)
}

// Secure Attention Sequence (SAS) e bloqueio de estação.
//
// IMPORTANTE: Ctrl+Alt+Del NÃO é injetável por SendInput — o Windows intercepta
// a combinação no kernel (Winlogon) antes de qualquer hook de usuário. A API
// suportada para gerar a SAS por software é SendSAS (sas.dll), que depende da
// política SoftwareSASGeneration do host:
//
//	HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System
//	    SoftwareSASGeneration (REG_DWORD)
//	      0 = desabilitado (padrão)
//	      1 = somente serviços
//	      2 = somente aplicativos
//	      3 = serviços e aplicativos
//
// Quando a política está desabilitada o SendSAS falha e o erro é propagado com
// a instrução exata (o operador vê o motivo no viewer em vez de "nada acontece").
var (
	sasDLL              = windows.NewLazySystemDLL("sas.dll")
	procSendSAS         = sasDLL.NewProc("SendSAS")
	procLockWorkStation = user32DLL.NewProc("LockWorkStation")
)

// SendSAS gera a Secure Attention Sequence (Ctrl+Alt+Del) no desktop ativo.
//
// Tenta os dois modos do parâmetro AsUser do SendSAS: FALSE (serviço/sessão 0,
// coberto por SoftwareSASGeneration=1) e TRUE (aplicativo na sessão interativa,
// coberto por SoftwareSASGeneration=2). A primeira tentativa aceita encerra —
// assim a mesma função funciona tanto no worker rodando como serviço quanto no
// worker elevado dentro da sessão do usuário.
func SendSAS() error {
	if err := sasDLL.Load(); err != nil {
		return fmt.Errorf("SendSAS indisponível (sas.dll não carregou): %v", err)
	}
	if err := procSendSAS.Find(); err != nil {
		return fmt.Errorf("SendSAS indisponível (export não encontrado em sas.dll): %v", err)
	}

	var lastErr error
	for _, asUser := range []uintptr{0, 1} {
		ret, _, callErr := procSendSAS.Call(asUser)
		if ret != 0 {
			return nil
		}
		lastErr = callErr
	}

	eno := errnoOf(lastErr)
	if eno == 0 {
		// SendSAS devolve FALSE sem setar erro do sistema quando a política
		// está desabilitada (0, padrão). Sem este caso a mensagem sairia com o
		// texto enganoso "The operation completed successfully. (errno=0)".
		return fmt.Errorf("SendSAS recusado sem erro do sistema (errno=0) — política atual: %s. "+
			"Habilite HKLM\\%s\\SoftwareSASGeneration "+
			"(1=serviços, 2=aplicativos, 3=ambos) no host remoto e reinicie o worker de acesso remoto",
			describeSASPolicy(), sasPolicyKey)
	}
	return fmt.Errorf(
		"SendSAS recusado: %v (errno=%d) — política atual: %s. Habilite HKLM\\%s\\SoftwareSASGeneration "+
			"(1=serviços, 2=aplicativos, 3=ambos) no host remoto e reinicie o worker de acesso remoto",
		describeErr(lastErr), eno, describeSASPolicy(), sasPolicyKey)
}

// LockWorkstation bloqueia a estação de trabalho (equivalente a Win+L).
//
// Win+L também é interceptado pelo SO antes do SendInput: a única forma
// suportada de disparar o bloqueio por software é a API LockWorkStation.
func LockWorkstation() error {
	ret, _, callErr := procLockWorkStation.Call()
	if ret == 0 {
		return fmt.Errorf("LockWorkStation falhou: %v (errno=%d)", describeErr(callErr), errnoOf(callErr))
	}
	return nil
}

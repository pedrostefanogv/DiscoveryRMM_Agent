//go:build windows

package terminal

import (
	"log"
	"os"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// stdLog aponta para o logger do stdlib (injetável em teste se um dia for
// necessário).
var stdLog = log.Printf

// ── Job Object do shell do terminal ──
//
// Cada shell (ConPTY/legacy) é associado a um Job Object com a flag
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE: fechar o handle do job TERMINA todos os
// processos do job. Isso garante que filhos do shell (ex.: pwsh → script →
// subprocesso) não sobrevivam ao fim da sessão de terminal — o Close() do
// shell fecha o job, e o kernel faz o resto, mesmo que o Kill() do processo
// direto falhe (handle/injetor).
//
// Windows 8+ suporta jobs aninhados, então atribuir o shell a um job próprio
// não conflita com o job do processo do agente (quando houver).

const (
	jobObjectExtendedLimitInformation = 9       // JobObjectInformationClass
	jobObjectLimitKillOnJobClose      = 0x2000 // JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
)

var (
	procCreateJobObjectW          = kernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject   = kernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject  = kernel32.NewProc("AssignProcessToJobObject")
)

// jobBasicLimitInformation espelha JOBOBJECT_BASIC_LIMIT_INFORMATION (x64).
// Padding EXPLÍCITO: o tamanho importa (SetInformationJobObject valida cbSize
// com ERROR_BAD_LENGTH) — o layout natural do Go dava 64 bytes onde o Windows
// usa 56 (o struct C termina em dois DWORDs, sem pad final), e o
// JOBOBJECT_EXTENDED_LIMIT_INFORMATION resultante (152 ≠ 144) era REJEITADO —
// o job nunca chegava a existir (descoberto no diagnóstico 2026-10-26).
type jobBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	_                       uint32 // alinhamento p/ os SIZE_T
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	_                       uint32 // alinhamento p/ o ULONG_PTR
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
	// NÃO há padding final: sizeof = 56 (o struct C termina em DWORD).
}

// ioCounters espelha IO_COUNTERS.
type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

// jobExtendedLimitInformation espelha JOBOBJECT_EXTENDED_LIMIT_INFORMATION.
type jobExtendedLimitInformation struct {
	BasicLimitInformation  jobBasicLimitInformation
	IoInfo                 ioCounters
	ProcessMemoryLimit     uintptr
	JobMemoryLimit         uintptr
	PeakProcessMemoryUsed  uintptr
	PeakJobMemoryUsed      uintptr
}

// jobObject é o handle do job com kill-on-close. nil-safe em todos os
// métodos (newKillOnCloseJob pode falhar em host restrito — a limpeza cai de
// volta para o Kill do processo direto).
type jobObject struct {
	h    syscall.Handle
	once sync.Once
}

// newKillOnCloseJob cria o job com JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE.
// Devolve nil (com log) quando a API não está disponível — nunca bloqueia o
// startup do terminal por isso.
func newKillOnCloseJob() *jobObject {
	// Opt-out de diagnóstico: DISCOVERY_TERM_NO_JOB=1 desliga o Job Object
	// (A/B em campo para isolar qualquer interação com AV/EDR — o probe
	// cmd/conptyprobe mostra sobrevivência similar com e sem job, mas o
	// ambiente desta estação está instável hoje: 0xC0000142 em 60-75%).
	switch os.Getenv("DISCOVERY_TERM_NO_JOB") {
	case "1", "true", "yes", "on":
		return nil
	}
	h, _, err := procCreateJobObjectW.Call(0, 0)
	if h == 0 {
		stdLog("[terminal] CreateJobObjectW falhou: %v", err)
		return nil
	}
	j := &jobObject{h: syscall.Handle(h)}
	info := jobExtendedLimitInformation{}
	info.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
	// cbSize = 144 (x64): tamanho REAL aceito pelo kernel para
	// JobObjectExtendedLimitInformation nesta plataforma. unsafe.Sizeof(info)
	// devolve 152 (Go alinha o struct BASIC a 8 no fim) e o kernel REJEITA com
	// ERROR_BAD_LENGTH — medido no probe (cmd/conptyprobe): 152 → falha, 144 →
	// sucesso. Os campos além de 144 não são lidos pelo kernel (todos zero).
	const jobExtendedLimitInfoCBSize = 144
	r1, _, err2 := procSetInformationJobObject.Call(
		uintptr(j.h),
		jobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		jobExtendedLimitInfoCBSize,
	)
	if r1 == 0 {
		stdLog("[terminal] SetInformationJobObject falhou: %v", err2)
		j.close()
		return nil
	}
	return j
}

func log2(err error) {
	// log indireto para manter o arquivo enxuto; evita import cycle-free log
	// direto duplicado (usa o stdlib log).
	stdLog("[terminal] job object: " + err.Error())
}

// assign associa o HANDLE do processo do shell ao job. Falhas são toleradas:
// o processo pode já estar em um job restrito do host (política corporativa) —
// nesse caso a limpeza segue o caminho antigo (Kill direto).
func (j *jobObject) assign(processHandle syscall.Handle) {
	if j == nil || processHandle == 0 {
		return
	}
	if r1, _, err := procAssignProcessToJobObject.Call(uintptr(j.h), uintptr(processHandle)); r1 == 0 {
		stdLog("[terminal] AssignProcessToJobObject falhou (filhos podem sobreviver ao close): %v", err)
	}
}

// assignPid abre o processo por PID (PROCESS_SET_QUOTA|PROCESS_TERMINATE,
// direitos exigidos pelo AssignProcessToJobObject) e o associa ao job. Usado
// pelo backend legacy, onde só existe o PID do exec.Cmd.
func (j *jobObject) assignPid(pid int) {
	if j == nil || pid <= 0 {
		return
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		stdLog("[terminal] OpenProcess(%d) para job falhou (filhos podem sobreviver ao close): %v", pid, err)
		return
	}
	j.assign(syscall.Handle(h))
	_ = windows.CloseHandle(h)
}

// close fecha o handle do job — TERMINA os processos do job
// (JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE). Idempotente.
func (j *jobObject) close() {
	if j == nil {
		return
	}
	j.once.Do(func() {
		if j.h != 0 {
			_ = syscall.CloseHandle(j.h)
			j.h = 0
		}
	})
}

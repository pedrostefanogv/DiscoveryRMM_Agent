package processutil

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// JobObject agrupa um processo e seus filhos para que possam ser encerrados
// juntos (kill de árvore). Instaladores stub costumam criar filhos (ex.:
// BraveUpdate) que sobrevivem quando só o processo principal é morto no
// timeout do exec.CommandContext.
type JobObject struct {
	handle windows.Handle
}

// NewJobObject cria o job. Não usa KILL_ON_JOB_CLOSE de propósito: o encerramento
// da árvore é explícito (Terminate) para não matar filhos legítimos quando a
// instalação termina com sucesso.
func NewJobObject() (*JobObject, error) {
	handle, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	// Sem KILL_ON_JOB_CLOSE: o encerramento da árvore é explícito (Terminate) para
	// não matar helpers legítimos quando a instalação termina com sucesso.
	info.BasicLimitInformation.LimitFlags = 0
	if _, err := windows.SetInformationJobObject(
		handle,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		windows.CloseHandle(handle)
		return nil, err
	}
	return &JobObject{handle: handle}, nil
}

// Assign coloca o processo (e futuros filhos) dentro do job.
func (j *JobObject) Assign(process *os.Process) error {
	if j == nil || j.handle == 0 || process == nil {
		return nil
	}
	processHandle, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(process.Pid),
	)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(processHandle)
	return windows.AssignProcessToJobObject(j.handle, processHandle)
}

// Terminate encerra todos os processos do job (árvore inteira).
func (j *JobObject) Terminate() error {
	if j == nil || j.handle == 0 {
		return nil
	}
	return windows.TerminateJobObject(j.handle, 1)
}

// Close libera o handle do job. Com KILL_ON_JOB_CLOSE, encerra o que restar —
// usado só depois de Terminate/timeout para não deixar filhos órfãos.
func (j *JobObject) Close() {
	if j == nil || j.handle == 0 {
		return
	}
	windows.CloseHandle(j.handle)
	j.handle = 0
}

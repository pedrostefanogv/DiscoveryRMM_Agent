// conptyprobe: diagnóstico isolado do spawn ConPTY + Job Object.
// Uso: go run ./cmd/conptyprobe <job|nojob>
package main

import (
	"fmt"
	"os"
	"os/exec"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func main() {
	mode := "job"
	if len(os.Args) > 1 && os.Args[1] == "plain" {
		// spawn simples (os/exec), sem ConPTY, sem job — 5 tentativas
		okCount := 0
		for i := 0; i < 5; i++ {
			cmd2 := exec.Command("powershell.exe", "-NoProfile", "-Command", "Write-Output PLAIN_OK")
			out, err := cmd2.CombinedOutput()
			if err == nil && len(out) > 0 {
				okCount++
			} else {
				fmt.Println("plain falhou:", err, string(out))
			}
		}
		fmt.Println("plain ok:", okCount, "/5")
		return
	}
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	cpc := kernel32.NewProc("CreatePseudoConsole")
	iptal := kernel32.NewProc("InitializeProcThreadAttributeList")
	upta := kernel32.NewProc("UpdateProcThreadAttribute")
	dptal := kernel32.NewProc("DeleteProcThreadAttributeList")
	cpw := kernel32.NewProc("CreateProcessW")
	cjob := kernel32.NewProc("CreateJobObjectW")
	sijo := kernel32.NewProc("SetInformationJobObject")
	aptj := kernel32.NewProc("AssignProcessToJobObject")
	gec := kernel32.NewProc("GetExitCodeProcess")

	var inR, inW, outR, outW windows.Handle
	if err := windows.CreatePipe(&inR, &inW, nil, 0); err != nil {
		panic(err)
	}
	if err := windows.CreatePipe(&outR, &outW, nil, 0); err != nil {
		panic(err)
	}

	size := struct{ X, Y int16 }{80, 25}
	var hpc windows.Handle
	r1, _, e1 := cpc.Call(uintptr(unsafe.Pointer(&size)), uintptr(inR), uintptr(outW), 0, uintptr(unsafe.Pointer(&hpc)))
	if r1 != 0 {
		panic(fmt.Sprintf("CreatePseudoConsole 0x%x %v", r1, e1))
	}

	var attrSize uintptr
	r0, _, e0 := iptal.Call(0, 1, 0, uintptr(unsafe.Pointer(&attrSize)))
	fmt.Println("iptal(size) r1=", r0, "size=", attrSize, "err=", e0)
	attr := make([]byte, attrSize)
	r0, _, e0 = iptal.Call(uintptr(unsafe.Pointer(&attr[0])), 1, 0, uintptr(unsafe.Pointer(&attrSize)))
	fmt.Println("iptal(init) r1=", r0, "err=", e0)
	const pseudoconsole = 0x00020016
	r0, _, e0 = upta.Call(uintptr(unsafe.Pointer(&attr[0])), 0, pseudoconsole, uintptr(hpc), unsafe.Sizeof(hpc), 0, 0)
	fmt.Println("upta r1=", r0, "err=", e0)

	si := struct {
		cb                                                        uint32
		r1, lpDesktop, lpTitle                                    uintptr
		dwX, dwY, dwXSize, dwYSize                                uint32
		dwXCountChars, dwYCountChars                              uint32
		dwFillAttribute, dwFlags                                  uint32
		wShow, cbReserved                                         uint16
		lpReserved2                                               uintptr
		hStdInput, hStdOutput, hStdErr                            uintptr
		lpAttributeList                                           unsafe.Pointer
	}{}
	fmt.Println("sizeof si =", unsafe.Sizeof(si), "attrList@", unsafe.Offsetof(si.lpAttributeList), "hStdIn@", unsafe.Offsetof(si.hStdInput))
	si.cb = 112
	si.dwFlags = 0x100 // STARTF_USESTDHANDLES
	si.lpAttributeList = unsafe.Pointer(&attr[0])

	cmdline, _ := windows.UTF16PtrFromString("powershell.exe -NoLogo -NoExit")
	var pi struct {
		hProcess, hThread windows.Handle
		pid, tid          uint32
	}

	var jobH windows.Handle
	if mode == "job" {
		jh, _, _ := cjob.Call(0, 0)
		if jh != 0 {
			jobH = windows.Handle(jh)
			// JOBOBJECT_EXTENDED_LIMIT_INFORMATION zerado (x64: BASIC=56, IO=48, 5 SIZE_T → 144).
			// LimitFlags (offset 16) = JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE (0x2000).
			buf := make([]byte, 144)
			buf[16] = 0x00
			buf[17] = 0x20
			r, _, e := sijo.Call(uintptr(jobH), 9, uintptr(unsafe.Pointer(&buf[0])), 144)
			fmt.Println("SetInformationJobObject r1=", r, "err=", e)
		} else {
			fmt.Println("CreateJobObjectW falhou")
		}
	}

	flags := uint32(0x00080000 | 0x00000400) // EXTENDED_STARTUPINFO_PRESENT | CREATE_UNICODE_ENVIRONMENT
	r1, _, e1 = cpw.Call(
		0, uintptr(unsafe.Pointer(cmdline)),
		0, 0, 0, uintptr(flags), 0, 0, // inheritHandles=false (como o agent)
		uintptr(unsafe.Pointer(&si)), uintptr(unsafe.Pointer(&pi)),
	)
	if r1 == 0 {
		panic(fmt.Sprintf("CreateProcessW %v", e1))
	}
	if jobH != 0 {
		r, _, e := aptj.Call(uintptr(jobH), uintptr(pi.hProcess))
		fmt.Println("AssignProcessToJobObject r1=", r, "err=", e)
	}
	windows.CloseHandle(pi.hThread)

	for i := 0; i < 40; i++ {
		time.Sleep(500 * time.Millisecond)
		var code uint32
		r, _, _ := gec.Call(uintptr(pi.hProcess), uintptr(unsafe.Pointer(&code)))
		alive := r != 0 && code == 0x00000103 // STILL_ACTIVE
		peek := kernel32.NewProc("PeekNamedPipe")
		var avail uint32
		peek.Call(uintptr(outR), 0, 0, 0, uintptr(unsafe.Pointer(&avail)), 0)
		fmt.Printf("t=%dms alive=%v exit=0x%08x pipeBytes=%d\n", (i+1)*300, alive, code, avail)
		if !alive {
			break
		}
	}
	dptal.Call(uintptr(unsafe.Pointer(&attr[0])))
	_ = windows.TerminateProcess(pi.hProcess, 1)
	windows.CloseHandle(pi.hProcess)
	if jobH != 0 {
		windows.CloseHandle(jobH)
	}
}

//go:build windows

package main

// Windows с >64 LP режет CPU на группы. Процесс по умолчанию живёт в одной группе —
// на этой машине это один Xeon из двух (на скрине: 44 LP на 100%, другие 44 почти спят).
// OpenMP OMP_PROC_BIND ещё и прибивает пул к «видимой» группе. Поэтому:
// 1) процессу — все CPU sets;
// 2) нити (и дочерние python) — по группам по кругу;
// 3) во время синтеза повторяем размазку: ONNX плодит пул уже после старта запроса.

import (
	"bytes"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

const (
	th32csSnapThread  = 0x00000004
	th32csSnapProcess = 0x00000002
	threadSetInfo     = 0x0020
	threadQueryInfo   = 0x0040
	threadSetLimited  = 0x0400
	threadQueryLim    = 0x0800
	processSetLimited = 0x2000
	processQueryLim   = 0x1000
	invalidSnap       = ^uintptr(0)
)

type groupAffinity struct {
	Mask     uintptr
	Group    uint16
	Reserved [3]uint16
}

type threadEntry32 struct {
	Size           uint32
	Usage          uint32
	ThreadID       uint32
	OwnerProcessID uint32
	BasePri        int32
	DeltaPri       int32
	Flags          uint32
}

type processEntry32W struct {
	Size            uint32
	Usage           uint32
	ProcessID       uint32
	DefaultHeapID   uintptr
	ModuleID        uint32
	Threads         uint32
	ParentProcessID uint32
	PriClassBase    int32
	Flags           uint32
	ExeFile         [260]uint16
}

var (
	k32Aff                         = syscall.NewLazyDLL("kernel32.dll")
	procGetActiveProcessorGroupCnt = k32Aff.NewProc("GetActiveProcessorGroupCount")
	procGetActiveProcessorCount    = k32Aff.NewProc("GetActiveProcessorCount")
	procSetThreadGroupAffinity     = k32Aff.NewProc("SetThreadGroupAffinity")
	procOpenThread                 = k32Aff.NewProc("OpenThread")
	procOpenProcessAff             = k32Aff.NewProc("OpenProcess")
	procCloseHandleAff             = k32Aff.NewProc("CloseHandle")
	procCreateToolhelp32Snapshot   = k32Aff.NewProc("CreateToolhelp32Snapshot")
	procThread32First              = k32Aff.NewProc("Thread32First")
	procThread32Next               = k32Aff.NewProc("Thread32Next")
	procProcess32FirstW            = k32Aff.NewProc("Process32FirstW")
	procProcess32NextW             = k32Aff.NewProc("Process32NextW")
	procGetSystemCpuSetInformation = k32Aff.NewProc("GetSystemCpuSetInformation")
	procSetProcessDefaultCpuSets   = k32Aff.NewProc("SetProcessDefaultCpuSets")
)

func pidListeningOnPort(port int) int {
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		"(Get-NetTCPConnection -LocalPort "+strconv.Itoa(port)+" -State Listen -ErrorAction SilentlyContinue | Select-Object -First 1 -ExpandProperty OwningProcess)")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(out.String()))
	return n
}

func setProcessAllCpuSets(pid int) bool {
	if pid <= 0 {
		return false
	}
	ids := systemCpuSetIDs()
	if len(ids) == 0 {
		return false
	}
	h, _, _ := procOpenProcessAff.Call(processSetLimited|processQueryLim, 0, uintptr(pid))
	if h == 0 {
		return false
	}
	defer procCloseHandleAff.Call(h)
	r, _, _ := procSetProcessDefaultCpuSets.Call(h, uintptr(unsafe.Pointer(&ids[0])), uintptr(len(ids)))
	return r != 0
}

func systemCpuSetIDs() []uint32 {
	var need uint32
	procGetSystemCpuSetInformation.Call(0, 0, uintptr(unsafe.Pointer(&need)), 0, 0)
	if need < 12 {
		return nil
	}
	buf := make([]byte, need)
	var got uint32
	r, _, _ := procGetSystemCpuSetInformation.Call(
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&got)), 0, 0)
	if r == 0 || got < 12 {
		return nil
	}
	var ids []uint32
	for off := 0; off+8 <= int(got); {
		size := *(*uint32)(unsafe.Pointer(&buf[off]))
		typ := *(*uint32)(unsafe.Pointer(&buf[off+4]))
		if size < 12 {
			break
		}
		if typ == 0 {
			ids = append(ids, *(*uint32)(unsafe.Pointer(&buf[off+8])))
		}
		off += int(size)
	}
	return ids
}

func childPIDs(parent int) []int {
	snap, _, _ := procCreateToolhelp32Snapshot.Call(th32csSnapProcess, 0)
	if snap == 0 || snap == invalidSnap {
		return nil
	}
	defer procCloseHandleAff.Call(snap)
	var pe processEntry32W
	pe.Size = uint32(unsafe.Sizeof(pe))
	ok, _, _ := procProcess32FirstW.Call(snap, uintptr(unsafe.Pointer(&pe)))
	var kids []int
	for ok != 0 {
		if int(pe.ParentProcessID) == parent && int(pe.ProcessID) != parent {
			kids = append(kids, int(pe.ProcessID))
		}
		ok, _, _ = procProcess32NextW.Call(snap, uintptr(unsafe.Pointer(&pe)))
	}
	return kids
}

func spreadPIDTree(pid int) int {
	if pid <= 0 {
		return 0
	}
	n := spreadPIDAcrossCPUGroups(pid)
	setProcessAllCpuSets(pid)
	for _, c := range childPIDs(pid) {
		setProcessAllCpuSets(c)
		n += spreadPIDAcrossCPUGroups(c)
	}
	return n
}

func spreadPIDAcrossCPUGroups(pid int) int {
	if pid <= 0 {
		return 0
	}
	groups, _, _ := procGetActiveProcessorGroupCnt.Call()
	if groups == 0 {
		groups = 1
	}
	masks := make([]uintptr, groups)
	for g := uintptr(0); g < groups; g++ {
		n, _, _ := procGetActiveProcessorCount.Call(g)
		if n == 0 || n > 64 {
			n = 64
		}
		if n >= 64 {
			masks[g] = ^uintptr(0)
		} else {
			masks[g] = (uintptr(1) << n) - 1
		}
	}

	snap, _, _ := procCreateToolhelp32Snapshot.Call(th32csSnapThread, 0)
	if snap == 0 || snap == invalidSnap {
		return 0
	}
	defer procCloseHandleAff.Call(snap)

	var te threadEntry32
	te.Size = uint32(unsafe.Sizeof(te))
	ok, _, _ := procThread32First.Call(snap, uintptr(unsafe.Pointer(&te)))
	moved := 0
	idx := 0
	access := uintptr(threadSetInfo | threadQueryInfo | threadSetLimited | threadQueryLim)
	for ok != 0 {
		if int(te.OwnerProcessID) == pid {
			g := uint16(idx % int(groups))
			if affThread(te.ThreadID, g, masks[g], access) {
				moved++
			}
			idx++
		}
		ok, _, _ = procThread32Next.Call(snap, uintptr(unsafe.Pointer(&te)))
	}
	return moved
}

func affThread(tid uint32, group uint16, mask uintptr, access uintptr) bool {
	h, _, _ := procOpenThread.Call(access, 0, uintptr(tid))
	if h == 0 {
		h, _, _ = procOpenThread.Call(threadSetInfo|threadQueryInfo, 0, uintptr(tid))
	}
	if h == 0 {
		return false
	}
	defer procCloseHandleAff.Call(h)
	ga := groupAffinity{Mask: mask, Group: group}
	r, _, _ := procSetThreadGroupAffinity.Call(h, uintptr(unsafe.Pointer(&ga)), 0)
	return r != 0
}

//go:build windows

package hostclock

import (
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows: QueryUnbiasedInterruptTime excludes sleep and hibernation
// (100 ns units), GetTickCount64 includes them (milliseconds).
var (
	kernel32           = windows.NewLazySystemDLL("kernel32.dll")
	procTickCount64    = kernel32.NewProc("GetTickCount64")
	procUnbiasedIntrTm = kernel32.NewProc("QueryUnbiasedInterruptTime")
)

func read() (Reading, bool) {
	if procTickCount64.Find() != nil || procUnbiasedIntrTm.Find() != nil {
		return Reading{}, false
	}
	var unbiased uint64
	if r, _, _ := procUnbiasedIntrTm.Call(uintptr(unsafe.Pointer(&unbiased))); r == 0 {
		return Reading{}, false
	}
	ticks, _, _ := procTickCount64.Call()
	return Reading{
		Awake: time.Duration(unbiased) * 100,
		Total: time.Duration(ticks) * time.Millisecond,
	}, true
}

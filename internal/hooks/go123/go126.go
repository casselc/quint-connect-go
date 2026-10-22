package go123

// Hooks for runtime entry points that are new since Go 1.23 (or moved, like
// sync.Mutex into internal/sync). Despite the package name, the fork targets
// Go 1.26.

import (
	"syscall"
	"unsafe"

	"github.com/jellevandenhooff/gosim/gosimruntime"
	"github.com/jellevandenhooff/gosim/internal/simulation"
)

// internal/sync holds sync.Mutex since Go 1.24.

func InternalSync_runtime_SemacquireMutex(addr *uint32, lifo bool, skipframes int) {
	gosimruntime.Semacquire(addr, lifo)
}

func InternalSync_runtime_Semrelease(addr *uint32, handoff bool, skipframes int) {
	gosimruntime.Semrelease(addr)
}

func InternalSync_runtime_canSpin(i int) bool { return false }

func InternalSync_runtime_doSpin() { panic("no") }

func InternalSync_runtime_nanotime() int64 { return gosimruntime.Nanotime() }

func InternalSync_runtime_rand() uint64 { return gosimruntime.Fastrand64() }

func InternalSync_throw(str string) { panic(str) }

func InternalSync_fatal(str string) { panic(str) }

func Sync_runtime_SemacquireWaitGroup(addr *uint32, synctestDurable bool) {
	gosimruntime.Semacquire(addr, false)
}

// The simulation is its own bubble: testing/synctest is not supported inside
// it, so nothing is ever in a synctest bubble.

func InternalSynctest_Run(f func()) { panic("gosim: testing/synctest is not supported") }

func InternalSynctest_Wait() { panic("gosim: testing/synctest is not supported") }

func InternalSynctest_IsInBubble() bool { return false }

func InternalSynctest_associate(p unsafe.Pointer) int { return 0 } // unbubbled

func InternalSynctest_disassociate(b unsafe.Pointer) {}

func InternalSynctest_isAssociated(p unsafe.Pointer) bool { return true }

func InternalSynctest_acquire() any { return nil }

func InternalSynctest_release(any) {}

func InternalSynctest_inBubble(b any, f func()) { f() }

func Time_runtimeNow() (sec int64, nsec int32, mono int64) { return Time_now() }

func Time_runtimeIsBubbled() bool { return false }

// Weak pointers stay strong: the simulation does not model the collector.

func Weak_runtime_registerWeakPointer(p unsafe.Pointer) unsafe.Pointer {
	cell := new(unsafe.Pointer)
	*cell = p
	return unsafe.Pointer(cell)
}

func Weak_runtime_makeStrongFromWeak(u unsafe.Pointer) unsafe.Pointer {
	return *(*unsafe.Pointer)(u)
}

func CryptoSubtle_setDITEnabled() bool { return false }

func CryptoSubtle_setDITDisabled() {}

func CryptoFips140_setBypass() {}

func CryptoFips140_isBypassed() bool { return false }

func CryptoFips140_unsetBypass() {}

func CryptoInternalSysrand_fatal(str string) { panic(str) }

func CryptoRand_fatal(str string) { panic(str) }

// Without the vDSO, getrandom falls back to the (simulated) system call.

func InternalSyscallUnix_vgetrandom(p []byte, flags uint32) (ret int, supported bool) {
	return 0, false
}

func GolangOrgXSysUnix_vgetrandom(p []byte, flags uint32) (ret int, supported bool) {
	return 0, false
}

func InternalRuntimeSyscallLinux_Syscall6(num, a1, a2, a3, a4, a5, a6 uintptr) (r1, r2, errno uintptr) {
	r1, r2, err := simulation.RawSyscall6(num, a1, a2, a3, a4, a5, a6)
	return r1, r2, uintptr(err)
}

var _ syscall.Errno

func Syscall_runtimeClearenv(env map[string]int) {
	panic("gosim not implemented")
}

// Compiler intrinsics outside the runtime lose their intrinsic status once
// translated to another package path. Their callers (internal/runtime/maps
// for race annotations) only use them for diagnostics.

func InternalRuntimeSys_GetCallerPC() uintptr { return 0 }

func InternalRuntimeSys_GetCallerSP() uintptr { return 0 }

func InternalRuntimeSys_GetClosurePtr() uintptr { return 0 }

func CryptoInternalFips140_fatal(str string) { panic(str) }

// The service indicator is per goroutine in the runtime; nothing in the
// simulation reads it.

func CryptoInternalFips140_getIndicator() uint8 { return 0 }

func CryptoInternalFips140_setIndicator(uint8) {}

// Compiler intrinsics whose Go bodies panic.

func CryptoInternalConstanttime_boolToUint8(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

// Built with purego, hash/maphash seeds from crypto/rand, which reads
// /dev/urandom before the simulated OS exists. Seed from the simulation.
func HashMaphash_randUint64() uint64 { return gosimruntime.Fastrand64() }

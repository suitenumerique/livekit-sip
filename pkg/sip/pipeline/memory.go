package pipeline

/*
#include <malloc.h>
*/
import "C"
import (
	"runtime"
	"runtime/debug"
)

// Call this after p.SetState(gst.StateNull) and p.Unref()
func ForceMemoryRelease() {
	runtime.GC()

	// 1. Force Go GC to run (clean up Go wrappers)
	debug.FreeOSMemory()

	// 2. Force C glibc to release free pages to OS
	C.malloc_trim(0)
}

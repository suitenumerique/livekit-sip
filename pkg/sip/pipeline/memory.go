package pipeline

/*
#include <malloc.h>
*/
import "C"
import (
	"runtime"
	"runtime/debug"
	"time"
)

// Call this after p.SetState(gst.StateNull)
func ForceMemoryRelease() {
	for range 5 {
		runtime.GC()
		time.Sleep(100 * time.Millisecond)
	}
	debug.FreeOSMemory()
	C.malloc_trim(0)
}

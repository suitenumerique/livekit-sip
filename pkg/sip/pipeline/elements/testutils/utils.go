package testutils

import (
	"bufio"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/go-gst/go-glib/glib"
)

func init() {
	glib.SetEnv("GST_TRACERS", "leaks(check-refs=false)", true)
	glib.SetEnv("GST_DEBUG", "*:3,GST_TRACER:7", true)
	glib.SetEnv("GST_LEAKS_TRACER_SIG", "SIGUSR1", true)

	println("GST_TRACERS:", glib.GetEnv("GST_TRACERS"))
	println("GST_DEBUG:", glib.GetEnv("GST_DEBUG"))
	println("GST_LEAKS_TRACER_SIG:", glib.GetEnv("GST_LEAKS_TRACER_SIG"))
}

const ansi = "[\u001B\u009B][[\\]()#;?]*(?:(?:(?:[a-zA-Z\\d]*(?:;[a-zA-Z\\d]*)*)?\u0007)|(?:(?:\\d{1,4}(?:;\\d{0,4})*)?[\\dA-PRZcf-ntqry=><~]))"

var re = regexp.MustCompile(ansi)

func IsLeak(line string) bool {
	line = re.ReplaceAllString(line, "")

	parts := strings.Fields(line)

	if len(parts) <= 7 {
		return false
	}

	if parts[3] != "TRACE" ||
		parts[4] != "GST_TRACER" ||
		parts[6] != "object-alive," {
		return false
	}

	if !strings.HasPrefix(parts[7], "type-name=(string)") {
		return false
	}
	factory := strings.TrimPrefix(parts[7], "type-name=(string)")
	if factory == "GstElementFactory," {
		return false
	}
	return true
}

type LeakDetector struct {
	mu          sync.Mutex
	t           *testing.T
	oldStderrFD int
	leaks       []string

	r *os.File
	w *os.File

	wg sync.WaitGroup
}

func (d *LeakDetector) Close() {
	d.t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	syscall.Dup2(d.oldStderrFD, int(os.Stderr.Fd()))
	syscall.Close(d.oldStderrFD)
	d.r.Close()
	d.wg.Wait()
	d.w.Close()

	if len(d.leaks) > 0 {
		d.t.Fatalf("Detected %d leaks:\n%s", len(d.leaks), strings.Join(d.leaks, "\n"))
	}
}

func SetupLeakDetection(t *testing.T) *LeakDetector {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Failed to create pipe: %v", err)
	}
	stderrFD := int(os.Stderr.Fd())
	savedStderrFD, err := syscall.Dup(stderrFD)
	if err != nil {
		t.Fatalf("Failed to duplicate stdout: %v", err)
	}
	err = syscall.Dup2(int(w.Fd()), stderrFD)
	if err != nil {
		t.Fatalf("Failed to redirect stdout: %v", err)
	}

	l := &LeakDetector{
		t:           t,
		oldStderrFD: savedStderrFD,
		r:           r,
		w:           w,
	}

	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		stderr := os.NewFile(uintptr(l.oldStderrFD), "/dev/stderr")
		scanner := bufio.NewScanner(r)
		verbose := testing.Verbose()
		for scanner.Scan() {
			line := scanner.Text()
			if IsLeak(line) {
				l.mu.Lock()
				l.leaks = append(l.leaks, line)
				l.mu.Unlock()
			}
			if verbose {
				stderr.WriteString(line + "\n")
			}

		}
	}()

	return l

}

func AssertNoLeaks(t *testing.T) {
	t.Helper()
	t.Log("Running GC before leak detection...")
	for i := 0; i < 5; i++ {
		runtime.GC()
		time.Sleep(100 * time.Millisecond)
	}
	t.Log("Running leak detection...")
	leaks := SetupLeakDetection(t)
	defer leaks.Close()
	syscall.Kill(syscall.Getpid(), syscall.SIGUSR1)
	time.Sleep(100 * time.Millisecond)
}

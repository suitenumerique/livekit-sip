//go:build debug_ui

package debug

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/go-gst/go-gst/gst"
)

type Server struct {
	mu       sync.RWMutex
	handlers map[string]http.Handler
	server   *http.Server
	dumpCH   chan<- bool
}

func NewServer(addr string, dumpCH chan<- bool) *Server {
	s := &Server{
		handlers: make(map[string]http.Handler),
		dumpCH:   dumpCH,
	}
	s.server = &http.Server{
		Addr:    addr,
		Handler: s,
	}
	return s
}

func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.server.Addr)
	if err != nil {
		return err
	}
	go s.server.Serve(ln)
	return nil
}

func (s *Server) Stop(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}

func (s *Server) OnElementAdded(child *gst.Element) {
	factory := child.GetFactory()
	if factory == nil {
		return
	}
	factoryName := factory.GetName()
	fn, ok := registry[factoryName]
	if !ok {
		return
	}
	handler := fn(child)
	if handler == nil {
		return
	}
	name := child.GetName()
	s.mu.Lock()
	s.handlers[name] = handler
	s.mu.Unlock()
}

func (s *Server) OnElementRemoved(child *gst.Element) {
	name := child.GetName()
	s.mu.Lock()
	delete(s.handlers, name)
	s.mu.Unlock()
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/" {
		s.serveIndex(w, r)
		return
	}
	if path == "/dot" && r.Method == http.MethodPost {
		s.handleDumpDot(w, r)
		return
	}

	// Extract first path segment as the element name.
	path = strings.TrimPrefix(path, "/")
	name, rest, _ := strings.Cut(path, "/")

	s.mu.RLock()
	handler, ok := s.handlers[name]
	s.mu.RUnlock()

	if !ok {
		http.NotFound(w, r)
		return
	}

	r.URL.Path = "/" + rest
	handler.ServeHTTP(w, r)
}

func (s *Server) handleDumpDot(w http.ResponseWriter, _ *http.Request) {
	select {
	case s.dumpCH <- true:
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	default:
		http.Error(w, "dump channel full", http.StatusServiceUnavailable)
	}
}

func (s *Server) serveIndex(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!DOCTYPE html><html><head><meta charset="UTF-8"><title>Debug</title>`)
	fmt.Fprint(w, `<style>body{font-family:monospace;margin:20px;background:#1a1a1a;color:#e0e0e0}a{color:#6af}a:hover{color:#8cf}</style>`)
	fmt.Fprint(w, `</head><body><h1>Debug Elements</h1><ul>`)
	for name := range s.handlers {
		fmt.Fprintf(w, `<li><a href="/%s/">%s</a></li>`, name, name)
	}
	if len(s.handlers) == 0 {
		fmt.Fprint(w, `<li style="color:#888">No debuggable elements registered</li>`)
	}
	fmt.Fprint(w, `</ul><hr style="border-color:#333"><button onclick="fetch('/dot',{method:'POST'}).then(r=>r.ok?console.log('Dumped'):console.error('Error'))" style="padding:6px 16px;cursor:pointer;background:#2a6;color:#fff;border:none;font-family:monospace">Dump DOT</button>`)
	fmt.Fprint(w, `</body></html>`)
}

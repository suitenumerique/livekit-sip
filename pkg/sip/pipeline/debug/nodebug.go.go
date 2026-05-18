//go:build !debug_ui

package debug

import (
	"context"
	"net/http"

	"github.com/go-gst/go-gst/gst"
)

type Server struct{}

func NewServer(addr string, dumpCH chan<- bool) *Server {
	return &Server{}
}

func (s *Server) Start() error {
	return nil
}

func (s *Server) Stop(ctx context.Context) error {
	return nil
}

func (s *Server) OnElementAdded(child *gst.Element) {}

func (s *Server) OnElementRemoved(child *gst.Element) {}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {}

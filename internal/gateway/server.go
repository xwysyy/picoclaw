package gateway

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

var errServerNotInitialized = errors.New("gateway server is not initialized")

type Server struct {
	addr       string
	mux        *http.ServeMux
	httpServer *http.Server
}

func NewServer(addr string) *Server {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		addr = "127.0.0.1:18790"
	}

	mux := http.NewServeMux()
	srv := &Server{
		addr: addr,
		mux:  mux,
	}
	srv.registerRoutes()
	srv.httpServer = &http.Server{
		Addr:    addr,
		Handler: mux,
	}
	return srv
}

func (s *Server) Handler() http.Handler {
	if s == nil || s.mux == nil {
		return http.NotFoundHandler()
	}
	return s.mux
}

func (s *Server) Start() error {
	if s == nil || s.httpServer == nil {
		return errServerNotInitialized
	}
	return s.httpServer.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil || s.httpServer == nil {
		return errServerNotInitialized
	}
	return s.httpServer.Shutdown(ctx)
}

package gateway

import (
	"context"
	"net/http"
)

type Server struct {
	handler    http.Handler
	httpServer *http.Server
}

func NewServer(addr string) *Server {
	handler := newRoutes()
	return &Server{
		handler: handler,
		httpServer: &http.Server{
			Addr:    addr,
			Handler: handler,
		},
	}
}

func (s *Server) Handler() http.Handler {
	if s == nil || s.handler == nil {
		return http.NotFoundHandler()
	}
	return s.handler
}

func (s *Server) Start() error {
	if s == nil || s.httpServer == nil {
		return nil
	}
	return s.httpServer.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil || s.httpServer == nil {
		return nil
	}
	return s.httpServer.Shutdown(ctx)
}

package gateway

func (s *Server) registerRoutes() {
	if s == nil || s.mux == nil {
		return
	}
	s.mux.HandleFunc("/health", healthHandler)
}

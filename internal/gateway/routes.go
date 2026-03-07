package gateway

import "net/http"

func newRoutes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/health", healthHandler())
	return mux
}

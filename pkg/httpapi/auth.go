package httpapi

import (
	"net/http"
	"strings"
)

// authorizeAPIKeyOrLoopback applies the same auth policy used by /api/notify:
// - If apiKey is empty: only allow loopback callers.
// - If apiKey is set: require X-API-Key or Authorization: Bearer <apiKey>.
func authorizeAPIKeyOrLoopback(apiKey string, r *http.Request) bool {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return isLoopbackRemote(r.RemoteAddr)
	}

	if strings.TrimSpace(r.Header.Get("X-API-Key")) == apiKey {
		return true
	}

	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(auth) > 7 && strings.EqualFold(auth[:7], "bearer ") {
		token := strings.TrimSpace(auth[7:])
		return token != "" && token == apiKey
	}

	return false
}

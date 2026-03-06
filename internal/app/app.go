package app

import (
	"fmt"
	"os"
	"strings"

	coregateway "github.com/xwysyy/X-Claw/internal/gateway"
)

const defaultGatewayAddr = "127.0.0.1:18790"

func RunGateway(debug bool) error {
	addr := strings.TrimSpace(os.Getenv("X_CLAW_GATEWAY_ADDR"))
	if addr == "" {
		addr = defaultGatewayAddr
	}
	if debug {
		fmt.Println("🔍 Debug mode enabled")
	}
	fmt.Printf("→ Starting Gateway Core on http://%s/health\n", addr)
	return coregateway.NewServer(addr).Start()
}

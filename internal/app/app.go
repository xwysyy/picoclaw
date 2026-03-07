package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	coregateway "github.com/xwysyy/X-Claw/internal/gateway"
	"github.com/xwysyy/X-Claw/pkg/logger"
)

type GatewayOptions struct {
	Addr  string
	Debug bool
}

func RunGateway(opts GatewayOptions) error {
	addr := opts.Addr
	if addr == "" {
		addr = "127.0.0.1:18790"
	}
	if opts.Debug {
		logger.SetLevel(logger.DEBUG)
		fmt.Println("🔍 Debug mode enabled")
	}

	srv := coregateway.NewServer(addr)
	errCh := make(chan error, 1)
	go func() {
		err := srv.Start()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	fmt.Printf("✓ Gateway Core started on %s\n", addr)
	fmt.Println("Press Ctrl+C to stop")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

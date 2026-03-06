package gateway

import (
	"net/http"

	pkghttpapi "github.com/xwysyy/X-Claw/pkg/httpapi"
)

type ConsoleHandlerOptions = pkghttpapi.ConsoleHandlerOptions
type ConsoleInfo = pkghttpapi.ConsoleInfo

func NewConsoleHandler(opts ConsoleHandlerOptions) http.Handler {
	return pkghttpapi.NewConsoleHandler(opts)
}

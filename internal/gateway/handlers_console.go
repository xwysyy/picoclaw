package gateway

import (
	oldhttpapi "github.com/xwysyy/X-Claw/pkg/httpapi"
)

type ConsoleHandlerOptions = oldhttpapi.ConsoleHandlerOptions
type ConsoleInfo = oldhttpapi.ConsoleInfo

type ConsoleHandler = oldhttpapi.ConsoleHandler

func NewConsoleHandler(opts ConsoleHandlerOptions) *ConsoleHandler {
	return oldhttpapi.NewConsoleHandler(opts)
}

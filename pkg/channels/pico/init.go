package pico

import (
	"github.com/xwysyy/picoclaw/pkg/bus"
	"github.com/xwysyy/picoclaw/pkg/channels"
	"github.com/xwysyy/picoclaw/pkg/config"
)

func init() {
	channels.RegisterFactory("pico", func(cfg *config.Config, b *bus.MessageBus) (channels.Channel, error) {
		return NewPicoChannel(cfg.Channels.Pico, b)
	})
}

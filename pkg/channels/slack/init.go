package slack

import (
	"github.com/xwysyy/picoclaw/pkg/bus"
	"github.com/xwysyy/picoclaw/pkg/channels"
	"github.com/xwysyy/picoclaw/pkg/config"
)

func init() {
	channels.RegisterFactory("slack", func(cfg *config.Config, b *bus.MessageBus) (channels.Channel, error) {
		return NewSlackChannel(cfg.Channels.Slack, b)
	})
}

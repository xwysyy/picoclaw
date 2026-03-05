package dingtalk

import (
	"github.com/xwysyy/picoclaw/pkg/bus"
	"github.com/xwysyy/picoclaw/pkg/channels"
	"github.com/xwysyy/picoclaw/pkg/config"
)

func init() {
	channels.RegisterFactory("dingtalk", func(cfg *config.Config, b *bus.MessageBus) (channels.Channel, error) {
		return NewDingTalkChannel(cfg.Channels.DingTalk, b)
	})
}

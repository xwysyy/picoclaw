package channels

import "github.com/xwysyy/X-Claw/pkg/config"

type channelInitSpec struct {
	name        string
	displayName string
	enabled     func(cfg *config.Config) bool
}

func selectedChannelInitializers(cfg *config.Config) []channelInitSpec {
	if cfg == nil {
		return nil
	}

	return []channelInitSpec{
		{
			name:        "telegram",
			displayName: "Telegram",
			enabled: func(cfg *config.Config) bool {
				return cfg.Channels.Telegram.Enabled && cfg.Channels.Telegram.Token.Present()
			},
		},
		{
			name:        "feishu",
			displayName: "Feishu",
			enabled:     func(cfg *config.Config) bool { return cfg.Channels.Feishu.Enabled },
		},
	}
}

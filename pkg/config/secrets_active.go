package config

import (
	"fmt"
	"strings"
)

// SecretValidationError reports failures to resolve secrets for enabled ("active") config surfaces.
// It is intentionally structured so callers can render errors in a CLI-friendly way.
//
// Note: This is NOT part of ValidateAll() because "active surfaces" depend on runtime intent.
// Gateway/channel startup and hot-reload paths should call these checks explicitly.
type SecretValidationError struct {
	Problems []ValidationProblem `json:"problems"`
}

func (e *SecretValidationError) Error() string {
	if e == nil || len(e.Problems) == 0 {
		return "secret resolution failed"
	}
	parts := make([]string, 0, len(e.Problems))
	for _, p := range e.Problems {
		path := strings.TrimSpace(p.Path)
		msg := strings.TrimSpace(p.Message)
		if msg == "" {
			continue
		}
		if path == "" {
			parts = append(parts, msg)
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %s", path, msg))
	}
	if len(parts) == 0 {
		return "secret resolution failed"
	}
	return "secret resolution failed: " + strings.Join(parts, "; ")
}

// ValidateActiveChannelConfig validates that all enabled channels have their required fields
// and secrets resolvable (env/file/inline) at runtime.
//
// This is part of "active-surface filtering": we only validate surfaces that are enabled.
func (c *Config) ValidateActiveChannelConfig() error {
	if c == nil {
		return nil
	}
	problems := c.activeChannelProblems()
	if len(problems) == 0 {
		return nil
	}
	return &SecretValidationError{Problems: problems}
}

func (c *Config) activeChannelProblems() []ValidationProblem {
	if c == nil {
		return nil
	}

	var problems []ValidationProblem
	add := func(path, msg string) {
		msg = strings.TrimSpace(msg)
		if msg == "" {
			return
		}
		problems = append(problems, ValidationProblem{
			Path:    strings.TrimSpace(path),
			Message: msg,
		})
	}
	requireString := func(path, v, label string) {
		if strings.TrimSpace(v) == "" {
			if label == "" {
				label = "value"
			}
			add(path, label+" is required")
		}
	}
	requireSecret := func(path string, ref SecretRef) {
		if !ref.Present() {
			add(path, "secret is required")
			return
		}
		v, err := ref.Resolve("")
		if err != nil {
			add(path, err.Error())
			return
		}
		if strings.TrimSpace(v) == "" {
			add(path, "secret resolved empty")
		}
	}
	optionalSecret := func(path string, ref SecretRef) {
		if !ref.Present() {
			return
		}
		v, err := ref.Resolve("")
		if err != nil {
			add(path, err.Error())
			return
		}
		if strings.TrimSpace(v) == "" {
			add(path, "secret resolved empty")
		}
	}

	// WhatsApp
	if c.Channels.WhatsApp.Enabled {
		if !c.Channels.WhatsApp.UseNative {
			requireString("channels.whatsapp.bridge_url", c.Channels.WhatsApp.BridgeURL, "bridge_url")
		}
	}

	// Telegram
	if c.Channels.Telegram.Enabled {
		requireSecret("channels.telegram.token", c.Channels.Telegram.Token)
	}

	// Feishu/Lark
	if c.Channels.Feishu.Enabled {
		requireString("channels.feishu.app_id", c.Channels.Feishu.AppID, "app_id")
		requireSecret("channels.feishu.app_secret", c.Channels.Feishu.AppSecret)
		optionalSecret("channels.feishu.verification_token", c.Channels.Feishu.VerificationToken)
		optionalSecret("channels.feishu.encrypt_key", c.Channels.Feishu.EncryptKey)
	}

	// Discord
	if c.Channels.Discord.Enabled {
		requireSecret("channels.discord.token", c.Channels.Discord.Token)
	}

	// QQ
	if c.Channels.QQ.Enabled {
		requireString("channels.qq.app_id", c.Channels.QQ.AppID, "app_id")
		requireSecret("channels.qq.app_secret", c.Channels.QQ.AppSecret)
	}

	// DingTalk
	if c.Channels.DingTalk.Enabled {
		requireString("channels.dingtalk.client_id", c.Channels.DingTalk.ClientID, "client_id")
		requireSecret("channels.dingtalk.client_secret", c.Channels.DingTalk.ClientSecret)
	}

	// Slack
	if c.Channels.Slack.Enabled {
		requireSecret("channels.slack.bot_token", c.Channels.Slack.BotToken)
		requireSecret("channels.slack.app_token", c.Channels.Slack.AppToken)
	}

	// LINE
	if c.Channels.LINE.Enabled {
		requireSecret("channels.line.channel_secret", c.Channels.LINE.ChannelSecret)
		requireSecret("channels.line.channel_access_token", c.Channels.LINE.ChannelAccessToken)
	}

	// OneBot
	if c.Channels.OneBot.Enabled {
		requireString("channels.onebot.ws_url", c.Channels.OneBot.WSUrl, "ws_url")
		optionalSecret("channels.onebot.access_token", c.Channels.OneBot.AccessToken)
	}

	// WeCom bot
	if c.Channels.WeCom.Enabled {
		requireSecret("channels.wecom.token", c.Channels.WeCom.Token)
		requireString("channels.wecom.webhook_url", c.Channels.WeCom.WebhookURL, "webhook_url")
		optionalSecret("channels.wecom.encoding_aes_key", c.Channels.WeCom.EncodingAESKey)
	}

	// WeCom AI Bot
	if c.Channels.WeComAIBot.Enabled {
		requireSecret("channels.wecom_aibot.token", c.Channels.WeComAIBot.Token)
		requireSecret("channels.wecom_aibot.encoding_aes_key", c.Channels.WeComAIBot.EncodingAESKey)
	}

	// WeCom App
	if c.Channels.WeComApp.Enabled {
		requireString("channels.wecom_app.corp_id", c.Channels.WeComApp.CorpID, "corp_id")
		requireSecret("channels.wecom_app.corp_secret", c.Channels.WeComApp.CorpSecret)
		if c.Channels.WeComApp.AgentID == 0 {
			add("channels.wecom_app.agent_id", "agent_id is required")
		}
		optionalSecret("channels.wecom_app.token", c.Channels.WeComApp.Token)
		optionalSecret("channels.wecom_app.encoding_aes_key", c.Channels.WeComApp.EncodingAESKey)
	}

	// Pico
	if c.Channels.Pico.Enabled {
		requireSecret("channels.pico.token", c.Channels.Pico.Token)
	}

	return problems
}

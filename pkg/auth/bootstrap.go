package auth

import "strings"

type BootstrapMethod string

const (
	BootstrapPasteToken   BootstrapMethod = "paste_token"
	BootstrapOAuthBrowser BootstrapMethod = "oauth_browser"
	BootstrapOAuthDevice  BootstrapMethod = "oauth_device"
)

func NormalizeBootstrapProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "antigravity", "google-antigravity":
		return "google-antigravity"
	default:
		return strings.ToLower(strings.TrimSpace(provider))
	}
}

func ResolveAuthBootstrapMethod(provider string) BootstrapMethod {
	switch NormalizeBootstrapProvider(provider) {
	case "openai", "google-antigravity":
		return BootstrapOAuthBrowser
	case "anthropic":
		return BootstrapPasteToken
	default:
		return BootstrapPasteToken
	}
}

func BootstrapMethodHelp(provider string) string {
	switch ResolveAuthBootstrapMethod(provider) {
	case BootstrapOAuthBrowser:
		return "complete the browser OAuth bootstrap and populate the local auth store"
	case BootstrapOAuthDevice:
		return "complete the device OAuth bootstrap and populate the local auth store"
	default:
		return "paste a token into the local auth store"
	}
}

func ProviderDisplayName(provider string) string {
	switch NormalizeBootstrapProvider(provider) {
	case "anthropic":
		return "console.anthropic.com"
	case "openai":
		return "platform.openai.com"
	case "google-antigravity":
		return "Google Antigravity"
	default:
		return strings.TrimSpace(provider)
	}
}

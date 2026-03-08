package providers

import (
	"fmt"
	"strings"

	"github.com/xwysyy/X-Claw/pkg/auth"
)

func missingOAuthCredentialError(provider string) error {
	provider = auth.NormalizeBootstrapProvider(provider)
	return fmt.Errorf(
		"no credentials for %s. Configure `api_key` in config, or %s before using oauth/token auth",
		strings.TrimSpace(provider),
		auth.BootstrapMethodHelp(provider),
	)
}

func expiredOAuthCredentialError(provider string) error {
	provider = auth.NormalizeBootstrapProvider(provider)
	return fmt.Errorf(
		"%s credentials expired. Refresh the local auth store credential via %s and retry",
		strings.TrimSpace(provider),
		auth.BootstrapMethodHelp(provider),
	)
}

func createClaudeAuthProvider() (LLMProvider, error) {
	cred, err := getCredential("anthropic")
	if err != nil {
		return nil, fmt.Errorf("loading auth credentials: %w", err)
	}
	if cred == nil {
		return nil, missingOAuthCredentialError("anthropic")
	}
	return NewClaudeProviderWithTokenSource(cred.AccessToken, createClaudeTokenSource()), nil
}

func createCodexAuthProvider() (LLMProvider, error) {
	cred, err := getCredential("openai")
	if err != nil {
		return nil, fmt.Errorf("loading auth credentials: %w", err)
	}
	if cred == nil {
		return nil, missingOAuthCredentialError("openai")
	}
	return NewCodexProviderWithTokenSource(cred.AccessToken, cred.AccountID, createCodexTokenSource()), nil
}

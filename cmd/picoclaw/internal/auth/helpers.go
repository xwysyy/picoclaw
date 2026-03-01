package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/cmd/picoclaw/internal"
	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

const supportedProvidersMsg = "supported providers: openai, anthropic, google-antigravity"

// providerMeta defines provider-specific login metadata to avoid repeating
// the same config-update boilerplate across three nearly identical functions.
type providerMeta struct {
	configKey    string // key in Providers struct: "openai", "anthropic", "antigravity"
	defaultModel string // e.g. "gpt-5.2", "claude-sonnet-4.6", "gemini-flash"
	fullModel    string // e.g. "openai/gpt-5.2", "anthropic/claude-sonnet-4.6"
	modelChecker func(string) bool
}

var providerMetaRegistry = map[string]providerMeta{
	"openai": {
		configKey:    "openai",
		defaultModel: "gpt-5.2",
		fullModel:    "openai/gpt-5.2",
		modelChecker: isOpenAIModel,
	},
	"anthropic": {
		configKey:    "anthropic",
		defaultModel: "claude-sonnet-4.6",
		fullModel:    "anthropic/claude-sonnet-4.6",
		modelChecker: isAnthropicModel,
	},
	"google-antigravity": {
		configKey:    "antigravity",
		defaultModel: "gemini-flash",
		fullModel:    "antigravity/gemini-3-flash",
		modelChecker: isAntigravityModel,
	},
}

func authLoginCmd(provider string, useDeviceCode bool) error {
	switch provider {
	case "openai":
		return authLoginOpenAI(useDeviceCode)
	case "anthropic":
		return authLoginPasteToken(provider)
	case "google-antigravity", "antigravity":
		return authLoginGoogleAntigravity()
	default:
		return fmt.Errorf("unsupported provider: %s (%s)", provider, supportedProvidersMsg)
	}
}

// updateConfigForLogin handles the common pattern of updating ModelList and default model
// after a successful login. This replaces ~30 lines of duplicated code per provider.
func updateConfigForLogin(providerName, authMethod string) {
	meta, ok := providerMetaRegistry[providerName]
	if !ok {
		return
	}

	appCfg, err := internal.LoadConfig()
	if err != nil {
		return
	}

	// Update legacy Providers section
	setProviderAuthMethod(appCfg, meta.configKey, authMethod)

	// Update or add in ModelList
	found := false
	for i := range appCfg.ModelList {
		if meta.modelChecker(appCfg.ModelList[i].Model) {
			appCfg.ModelList[i].AuthMethod = authMethod
			found = true
			break
		}
	}
	if !found {
		appCfg.ModelList = append(appCfg.ModelList, config.ModelConfig{
			ModelName:  meta.defaultModel,
			Model:      meta.fullModel,
			AuthMethod: authMethod,
		})
	}

	appCfg.Agents.Defaults.ModelName = meta.defaultModel

	if err := config.SaveConfig(internal.GetConfigPath(), appCfg); err != nil {
		fmt.Printf("Warning: could not update config: %v\n", err)
	}
}

// setProviderAuthMethod updates the AuthMethod in the legacy Providers struct.
func setProviderAuthMethod(cfg *config.Config, key, method string) {
	switch key {
	case "openai":
		cfg.Providers.OpenAI.AuthMethod = method
	case "anthropic":
		cfg.Providers.Anthropic.AuthMethod = method
	case "antigravity":
		cfg.Providers.Antigravity.AuthMethod = method
	}
}

func authLoginOpenAI(useDeviceCode bool) error {
	cfg := auth.OpenAIOAuthConfig()

	var cred *auth.AuthCredential
	var err error
	if useDeviceCode {
		cred, err = auth.LoginDeviceCode(cfg)
	} else {
		cred, err = auth.LoginBrowser(cfg)
	}
	if err != nil {
		return fmt.Errorf("login failed: %w", err)
	}

	if err = auth.SetCredential("openai", cred); err != nil {
		return fmt.Errorf("failed to save credentials: %w", err)
	}

	updateConfigForLogin("openai", "oauth")

	fmt.Println("Login successful!")
	if cred.AccountID != "" {
		fmt.Printf("Account: %s\n", cred.AccountID)
	}
	fmt.Println("Default model set to: gpt-5.2")
	return nil
}

func authLoginGoogleAntigravity() error {
	cfg := auth.GoogleAntigravityOAuthConfig()

	cred, err := auth.LoginBrowser(cfg)
	if err != nil {
		return fmt.Errorf("login failed: %w", err)
	}

	cred.Provider = "google-antigravity"

	// Fetch user email from Google userinfo
	if email, err := fetchGoogleUserEmail(cred.AccessToken); err != nil {
		fmt.Printf("Warning: could not fetch email: %v\n", err)
	} else {
		cred.Email = email
		fmt.Printf("Email: %s\n", email)
	}

	// Fetch Cloud Code Assist project ID
	if projectID, err := providers.FetchAntigravityProjectID(cred.AccessToken); err != nil {
		fmt.Printf("Warning: could not fetch project ID: %v\n", err)
		fmt.Println("You may need Google Cloud Code Assist enabled on your account.")
	} else {
		cred.ProjectID = projectID
		fmt.Printf("Project: %s\n", projectID)
	}

	if err = auth.SetCredential("google-antigravity", cred); err != nil {
		return fmt.Errorf("failed to save credentials: %w", err)
	}

	updateConfigForLogin("google-antigravity", "oauth")

	fmt.Println("\n✓ Google Antigravity login successful!")
	fmt.Println("Default model set to: gemini-flash")
	fmt.Println("Try it: picoclaw agent -m \"Hello world\"")
	return nil
}

func fetchGoogleUserEmail(accessToken string) (string, error) {
	req, err := http.NewRequest("GET", "https://www.googleapis.com/oauth2/v2/userinfo", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("userinfo request failed: %s", string(body))
	}

	var userInfo struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(body, &userInfo); err != nil {
		return "", err
	}
	return userInfo.Email, nil
}

func authLoginPasteToken(provider string) error {
	cred, err := auth.LoginPasteToken(provider, os.Stdin)
	if err != nil {
		return fmt.Errorf("login failed: %w", err)
	}

	if err = auth.SetCredential(provider, cred); err != nil {
		return fmt.Errorf("failed to save credentials: %w", err)
	}

	updateConfigForLogin(provider, "token")

	fmt.Printf("Token saved for %s!\n", provider)
	appCfg, err := internal.LoadConfig()
	if err == nil {
		fmt.Printf("Default model set to: %s\n", appCfg.Agents.Defaults.GetModelName())
	}
	return nil
}

func authLogoutCmd(provider string) error {
	if provider != "" {
		if err := auth.DeleteCredential(provider); err != nil {
			return fmt.Errorf("failed to remove credentials: %w", err)
		}

		appCfg, err := internal.LoadConfig()
		if err == nil {
			clearModelListAuth(appCfg, provider)
			setProviderAuthMethod(appCfg, normalizeProviderKey(provider), "")
			config.SaveConfig(internal.GetConfigPath(), appCfg)
		}

		fmt.Printf("Logged out from %s\n", provider)
		return nil
	}

	if err := auth.DeleteAllCredentials(); err != nil {
		return fmt.Errorf("failed to remove credentials: %w", err)
	}

	appCfg, err := internal.LoadConfig()
	if err == nil {
		for i := range appCfg.ModelList {
			appCfg.ModelList[i].AuthMethod = ""
		}
		appCfg.Providers.OpenAI.AuthMethod = ""
		appCfg.Providers.Anthropic.AuthMethod = ""
		appCfg.Providers.Antigravity.AuthMethod = ""
		config.SaveConfig(internal.GetConfigPath(), appCfg)
	}

	fmt.Println("Logged out from all providers")
	return nil
}

// clearModelListAuth clears the AuthMethod for all models matching the given provider.
func clearModelListAuth(cfg *config.Config, provider string) {
	checker := modelCheckerForProvider(provider)
	if checker == nil {
		return
	}
	for i := range cfg.ModelList {
		if checker(cfg.ModelList[i].Model) {
			cfg.ModelList[i].AuthMethod = ""
		}
	}
}

// normalizeProviderKey maps provider names to their config key.
func normalizeProviderKey(provider string) string {
	switch provider {
	case "google-antigravity", "antigravity":
		return "antigravity"
	default:
		return provider
	}
}

// modelCheckerForProvider returns the appropriate model checker for a provider name.
func modelCheckerForProvider(provider string) func(string) bool {
	switch provider {
	case "openai":
		return isOpenAIModel
	case "anthropic":
		return isAnthropicModel
	case "google-antigravity", "antigravity":
		return isAntigravityModel
	default:
		return nil
	}
}

func authStatusCmd() error {
	store, err := auth.LoadStore()
	if err != nil {
		return fmt.Errorf("failed to load auth store: %w", err)
	}

	if len(store.Credentials) == 0 {
		fmt.Println("No authenticated providers.")
		fmt.Println("Run: picoclaw auth login --provider <name>")
		return nil
	}

	fmt.Println("\nAuthenticated Providers:")
	fmt.Println("------------------------")
	for provider, cred := range store.Credentials {
		status := "active"
		if cred.IsExpired() {
			status = "expired"
		} else if cred.NeedsRefresh() {
			status = "needs refresh"
		}

		fmt.Printf("  %s:\n", provider)
		fmt.Printf("    Method: %s\n", cred.AuthMethod)
		fmt.Printf("    Status: %s\n", status)
		if cred.AccountID != "" {
			fmt.Printf("    Account: %s\n", cred.AccountID)
		}
		if cred.Email != "" {
			fmt.Printf("    Email: %s\n", cred.Email)
		}
		if cred.ProjectID != "" {
			fmt.Printf("    Project: %s\n", cred.ProjectID)
		}
		if !cred.ExpiresAt.IsZero() {
			fmt.Printf("    Expires: %s\n", cred.ExpiresAt.Format("2006-01-02 15:04"))
		}
	}

	return nil
}

func authModelsCmd() error {
	cred, err := auth.GetCredential("google-antigravity")
	if err != nil || cred == nil {
		return fmt.Errorf(
			"not logged in to Google Antigravity.\nrun: picoclaw auth login --provider google-antigravity",
		)
	}

	// Refresh token if needed
	if cred.NeedsRefresh() && cred.RefreshToken != "" {
		oauthCfg := auth.GoogleAntigravityOAuthConfig()
		refreshed, refreshErr := auth.RefreshAccessToken(cred, oauthCfg)
		if refreshErr == nil {
			cred = refreshed
			_ = auth.SetCredential("google-antigravity", cred)
		}
	}

	projectID := cred.ProjectID
	if projectID == "" {
		return fmt.Errorf("no project id stored. Try logging in again")
	}

	fmt.Printf("Fetching models for project: %s\n\n", projectID)

	models, err := providers.FetchAntigravityModels(cred.AccessToken, projectID)
	if err != nil {
		return fmt.Errorf("error fetching models: %w", err)
	}

	if len(models) == 0 {
		return fmt.Errorf("no models available")
	}

	fmt.Println("Available Antigravity Models:")
	fmt.Println("-----------------------------")
	for _, m := range models {
		status := "✓"
		if m.IsExhausted {
			status = "✗ (quota exhausted)"
		}
		name := m.ID
		if m.DisplayName != "" {
			name = fmt.Sprintf("%s (%s)", m.ID, m.DisplayName)
		}
		fmt.Printf("  %s %s\n", status, name)
	}

	return nil
}

// isProviderModel checks if a model string matches any of the given provider prefixes.
func isProviderModel(model string, providers ...string) bool {
	for _, p := range providers {
		if model == p || strings.HasPrefix(model, p+"/") {
			return true
		}
	}
	return false
}

func isAntigravityModel(model string) bool {
	return isProviderModel(model, "antigravity", "google-antigravity")
}

func isOpenAIModel(model string) bool {
	return isProviderModel(model, "openai")
}

func isAnthropicModel(model string) bool {
	return isProviderModel(model, "anthropic")
}

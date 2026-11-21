package model

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

// ExecutorConfig contains no secrets. APIKeyEnv names a server environment variable.
// CLI subscription authentication remains in the CLI's own credential store.
type ExecutorConfig struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Command   string `json:"command,omitempty"`
	Model     string `json:"model,omitempty"`
	BaseURL   string `json:"baseUrl,omitempty"`
	APIKeyEnv string `json:"apiKeyEnv,omitempty"`
}

func DefaultExecutorConfigs() []ExecutorConfig {
	return []ExecutorConfig{
		{ID: "codex", Name: "Codex CLI", Kind: "codex", Command: "codex"},
		{ID: "claude", Name: "Claude Code", Kind: "claude", Command: "claude"},
		{ID: "deepseek", Name: "DeepSeek API", Kind: "openai-compatible", Model: "deepseek-flash", BaseURL: "https://api.deepseek.com", APIKeyEnv: "DEEPSEEK_API_KEY"},
	}
}

func ValidateExecutorConfigs(configs []ExecutorConfig) error {
	if len(configs) > 64 {
		return errors.New("допускается не более 64 исполнителей")
	}
	ids := map[string]bool{}
	for _, c := range configs {
		if !ValidID(c.ID) || ids[c.ID] {
			return errors.New("некорректный или повторяющийся ID исполнителя")
		}
		ids[c.ID] = true
		if strings.TrimSpace(c.Name) == "" || len(c.Name) > 200 {
			return errors.New("укажите название исполнителя (до 200 байт)")
		}
		if strings.ContainsAny(c.Command+c.Model, "\x00\r\n") {
			return errors.New("команда и модель содержат управляющие символы")
		}
		switch c.Kind {
		case "codex", "claude":
			if c.BaseURL != "" || c.APIKeyEnv != "" {
				return errors.New("CLI использует собственную авторизацию; URL и переменная ключа относятся к API-исполнителю")
			}
		case "openai-compatible":
			if c.Command != "" {
				return errors.New("API-исполнитель не использует локальную команду")
			}
			if strings.TrimSpace(c.Model) == "" || !ValidIdentifier(c.APIKeyEnv) {
				return errors.New("API требует модель и имя переменной окружения с ключом")
			}
			if err := validateExecutorURL(c.BaseURL); err != nil {
				return err
			}
		default:
			return errors.New("неизвестный тип исполнителя")
		}
	}
	return nil
}

func validateExecutorURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") || u.Opaque != "" {
		return errors.New("URL API должен содержать адрес без авторизации, query и fragment")
	}
	if u.Scheme == "https" {
		return nil
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme == "http" && (strings.EqualFold(u.Hostname(), "localhost") || (ip != nil && ip.IsLoopback())) {
		return nil
	}
	return errors.New("URL API требует HTTPS; HTTP разрешён только для loopback")
}

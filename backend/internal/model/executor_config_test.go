package model

import "testing"

func TestExecutorConfigValidation(t *testing.T) {
	for _, raw := range []string{"https://api.deepseek.com", "https://api.example.test/v1", "http://localhost:1234/v1", "http://127.0.0.1:1234", "http://[::1]:1234"} {
		config := DefaultExecutorConfigs()[2]
		config.BaseURL = raw
		if err := ValidateExecutorConfigs([]ExecutorConfig{config}); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
	}
	for _, raw := range []string{"http://api.example.test", "https://user:secret@example.test", "https://example.test?key=secret", "https://example.test?", "https://example.test#secret", "https://example.test#", "file:///tmp/test", "https:///path", "http://127.0.0.1.evil.test", "https://"} {
		config := DefaultExecutorConfigs()[2]
		config.BaseURL = raw
		if err := ValidateExecutorConfigs([]ExecutorConfig{config}); err == nil {
			t.Fatalf("unsafe URL %q accepted", raw)
		}
	}
	for _, change := range []func(*ExecutorConfig){
		func(c *ExecutorConfig) { c.ID = "../bad" },
		func(c *ExecutorConfig) { c.Kind = "shell" },
		func(c *ExecutorConfig) { c.Name = "" },
		func(c *ExecutorConfig) { c.APIKeyEnv = "secret-key-value!" },
		func(c *ExecutorConfig) { c.Command = "sh" },
	} {
		config := DefaultExecutorConfigs()[2]
		change(&config)
		if err := ValidateExecutorConfigs([]ExecutorConfig{config}); err == nil {
			t.Fatalf("invalid config accepted: %+v", config)
		}
	}
	c := DefaultExecutorConfigs()[0]
	if err := ValidateExecutorConfigs([]ExecutorConfig{c, c}); err == nil {
		t.Fatal("duplicate ID accepted")
	}
}

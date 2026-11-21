package settings

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"flowline/internal/model"
)

func TestLoadDefaultsAndEmptyExecutors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if _, err := Load(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file error = %v", err)
	}

	for _, tc := range []struct {
		name     string
		contents string
		want     []model.ExecutorConfig
	}{
		{"omitted", `{}`, model.DefaultExecutorConfigs()},
		{"empty", `{"executors":[]}`, []model.ExecutorConfig{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.contents), 0600); err != nil {
				t.Fatal(err)
			}

			config, err := Load(path)
			if err != nil || !reflect.DeepEqual(config.Executors, tc.want) {
				t.Fatalf("executors = %+v, error = %v", config.Executors, err)
			}
		})
	}
}

func TestSaveExecutorsPreservesServerSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	contents := `{"listen":"[::1]:4174","dataDir":"~/flowline-data","frontendDir":"../frontend"}`
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatal(err)
	}

	profiles := model.DefaultExecutorConfigs()[:1]
	profiles[0].Model = "configured-model"
	if err := SaveExecutors(path, profiles); err != nil {
		t.Fatal(err)
	}

	config, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if config.Listen != "[::1]:4174" || config.DataDir != "~/flowline-data" || config.FrontendDir != "../frontend" || !reflect.DeepEqual(config.Executors, profiles) {
		t.Fatalf("settings changed unexpectedly: %+v", config)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if info.Mode().Perm() != 0600 {
		t.Fatalf("permissions = %v", info.Mode().Perm())
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files leaked: %v, error = %v", entries, err)
	}

	// A subsequent UI save must read the latest settings rather than a cached copy.
	contents = `{"listen":"127.0.0.1:4200","dataDir":"new-data","frontendDir":"new-frontend"}`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}

	if err := SaveExecutors(path, nil); err != nil {
		t.Fatal(err)
	}

	config, err = Load(path)
	if err != nil || config.Executors == nil || len(config.Executors) != 0 || config.Listen != "127.0.0.1:4200" || config.DataDir != "new-data" || config.FrontendDir != "new-frontend" {
		t.Fatalf("empty profiles or latest settings lost: %+v, error = %v", config, err)
	}
}

func TestSaveExecutorsCreatesMissingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "settings.json")
	profiles := model.DefaultExecutorConfigs()
	if err := SaveExecutors(path, profiles); err != nil {
		t.Fatal(err)
	}

	config, err := Load(path)
	if err != nil || !reflect.DeepEqual(config.Executors, profiles) {
		t.Fatalf("config = %+v, error = %v", config, err)
	}
}

func TestLoadRejectsMalformedAndUnsafeSettings(t *testing.T) {
	for name, contents := range map[string]string{
		"null document":      `null`,
		"array document":     `[]`,
		"null executors":     `{"executors":null}`,
		"unknown field":      `{"typo":true}`,
		"secret":             `{"apiKey":"secret"}`,
		"executor secret":    `{"executors":[{"id":"api","name":"API","kind":"openai-compatible","apiKey":"secret"}]}`,
		"trailing document":  `{} {}`,
		"trailing junk":      `{} junk`,
		"invalid JSON":       `not JSON`,
		"empty document":     ``,
		"oversized document": strings.Repeat(" ", maxSize+1),
		"public address":     `{"listen":"0.0.0.0:4173"}`,
		"hostname":           `{"listen":"localhost:4173"}`,
		"missing port":       `{"listen":"127.0.0.1"}`,
		"named port":         `{"listen":"127.0.0.1:http"}`,
		"large port":         `{"listen":"127.0.0.1:65536"}`,
		"signed port":        `{"listen":"127.0.0.1:+4173"}`,
		"negative port":      `{"listen":"127.0.0.1:-1"}`,
		"empty port":         `{"listen":"127.0.0.1:"}`,
		"data control":       `{"dataDir":"bad\u0000path"}`,
		"frontend control":   `{"frontendDir":"bad\npath"}`,
		"invalid executor":   `{"executors":[{"id":"bad","name":"Bad","kind":"shell"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}

			if _, err := Load(path); err == nil {
				t.Fatal("invalid settings accepted")
			}

			if err := SaveExecutors(path, model.DefaultExecutorConfigs()); err == nil {
				t.Fatal("invalid existing document overwritten")
			}

			actual, err := os.ReadFile(path)
			if err != nil || string(actual) != contents {
				t.Fatalf("invalid existing document changed: error = %v", err)
			}
		})
	}
}

func TestInvalidSavePreservesPreviousFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := SaveExecutors(path, model.DefaultExecutorConfigs()); err != nil {
		t.Fatal(err)
	}

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	for _, configs := range [][]model.ExecutorConfig{
		{{ID: "../bad", Name: "Bad", Kind: "codex"}},
		{{ID: "large", Name: "Large", Kind: "codex", Model: strings.Repeat("m", maxSize)}},
	} {
		if err := SaveExecutors(path, configs); err == nil {
			t.Fatal("invalid save succeeded")
		}

		after, err := os.ReadFile(path)
		if err != nil || string(after) != string(before) {
			t.Fatalf("previous file changed: error = %v", err)
		}
	}
}

func TestListenLoopbackAddresses(t *testing.T) {
	for _, listen := range []string{"127.0.0.1:0", "127.0.0.1:4173", "[::1]:65535"} {
		config := Default()
		config.Listen = listen
		if err := Validate(config); err != nil {
			t.Fatalf("%s: %v", listen, err)
		}
	}
}

func TestExampleSettings(t *testing.T) {
	config, err := Load(filepath.Join("..", "..", "..", "..", "examples", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}

	if config.Listen != "127.0.0.1:4173" || config.DataDir != "~/.flowline" || config.FrontendDir != "" || !reflect.DeepEqual(config.Executors, model.DefaultExecutorConfigs()) {
		t.Fatalf("example does not match documented defaults: %+v", config)
	}
}

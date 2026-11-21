package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"flowline/internal/model"
)

func writeSettings(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultLocationUsesHomeAndPreservesLegacyProfiles(t *testing.T) {
	root := t.TempDir()
	home, cwd := filepath.Join(root, "home"), filepath.Join(root, "project", "backend")
	data := filepath.Join(home, ".flowline")
	profiles := model.DefaultExecutorConfigs()[:1]
	profiles[0].Model = "existing-model"
	legacy := filepath.Join(data, "executors.json")
	contents, err := json.Marshal(profiles)
	if err != nil {
		t.Fatal(err)
	}
	writeSettings(t, legacy, string(contents))

	config, err := load(nil, home, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if config.SettingsPath != filepath.Join(home, ".flowline", "settings.json") || config.DataDir != data || config.FrontendDir != filepath.Join(root, "project", "frontend") {
		t.Fatalf("unexpected paths: %+v", config)
	}
	if !reflect.DeepEqual(config.Executors, profiles) {
		t.Fatalf("lost existing profiles: %+v", config.Executors)
	}
	if _, err := os.Stat(config.SettingsPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("loading must not write settings: %v", err)
	}

	writeSettings(t, config.SettingsPath, `{"executors":[]}`)
	config, err = load(nil, home, cwd)
	if err != nil || len(config.Executors) != 0 {
		t.Fatalf("explicit empty profiles must override legacy profiles: %+v, %v", config.Executors, err)
	}
}

func TestDefaultDataDirectoryIsSharedAcrossProjects(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	wantData := filepath.Join(home, ".flowline")
	for _, project := range []string{"project-a", "project-b"} {
		cwd := filepath.Join(root, project, "backend")
		// Project-local legacy profiles must not influence shared application state.
		writeSettings(t, filepath.Join(root, project, ".flowline", "executors.json"), `invalid json`)

		config, err := load(nil, home, cwd)
		if err != nil {
			t.Fatal(err)
		}
		if config.DataDir != wantData || config.SettingsPath != filepath.Join(wantData, "settings.json") {
			t.Fatalf("project %s resolved different state: %+v", project, config)
		}
		if !reflect.DeepEqual(config.Executors, model.DefaultExecutorConfigs()) {
			t.Fatalf("project profiles influenced shared state: %+v", config.Executors)
		}
	}
}

func TestAlternateSettingsFileDoesNotRelocateDefaultData(t *testing.T) {
	root := t.TempDir()
	home, cwd := filepath.Join(root, "home"), filepath.Join(root, "project", "backend")
	path := filepath.Join(root, "external-config", "settings.json")
	writeSettings(t, path, `{"executors":[]}`)

	config, err := load([]string{"--settings", path}, home, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if config.SettingsPath != path || config.DataDir != filepath.Join(home, ".flowline") {
		t.Fatalf("alternate settings relocated shared state: %+v", config)
	}
}

func TestSettingsPathsAndFlagPrecedence(t *testing.T) {
	root := t.TempDir()
	home, cwd := filepath.Join(root, "home"), filepath.Join(root, "project", "backend")
	path := filepath.Join(home, ".flowline", "settings.json")
	writeSettings(t, path, `{"listen":"127.0.0.1:4500","dataDir":"data","frontendDir":"~/code/flowline/frontend","executors":[]}`)

	config, err := load(nil, home, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if config.Listen != "127.0.0.1:4500" || config.DataDir != filepath.Join(home, ".flowline", "data") || config.FrontendDir != filepath.Join(home, "code", "flowline", "frontend") {
		t.Fatalf("file-relative and home paths: %+v", config)
	}

	args := []string{"--settings", "~/.flowline/settings.json", "--listen", "[::1]:4600", "--data", "./other-data", "--frontend", "../other-ui"}
	config, err = load(args, home, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if config.SettingsPath != path || config.Listen != "[::1]:4600" || config.DataDir != filepath.Join(cwd, "other-data") || config.FrontendDir != filepath.Join(root, "project", "other-ui") {
		t.Fatalf("CLI paths must be cwd-relative and override file: %+v", config)
	}

	writeSettings(t, filepath.Join(cwd, "alternate.json"), `{"dataDir":"custom-data","executors":[]}`)
	config, err = load([]string{"--settings", "alternate.json"}, home, cwd)
	if err != nil || config.DataDir != filepath.Join(cwd, "custom-data") {
		t.Fatalf("alternate settings file: %+v, %v", config, err)
	}
}

func TestInvalidSettingsDoNotFallBackToDefaults(t *testing.T) {
	root := t.TempDir()
	home, cwd := filepath.Join(root, "home"), filepath.Join(root, "project", "backend")
	path := filepath.Join(home, ".flowline", "settings.json")
	if _, err := load([]string{"--settings", path}, home, cwd); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("explicit missing settings must fail: %v", err)
	}

	for _, contents := range []string{`not-json`, `{"listen":"0.0.0.0:4173"}`, `{"executors":null}`, `{"unknown":true}`} {
		writeSettings(t, path, contents)
		if _, err := load(nil, home, cwd); err == nil {
			t.Fatalf("invalid settings silently ignored: %s", contents)
		}
	}

	writeSettings(t, path, `{}`)
	for _, args := range [][]string{
		{"--listen", "0.0.0.0:4173"}, {"--listen", "127.0.0.1:99999"}, {"--listen", ""},
		{"--data", ""}, {"--frontend", "~other/frontend"}, {"--settings", ""}, {"unexpected"},
	} {
		if _, err := load(args, home, cwd); err == nil {
			t.Fatalf("invalid flags accepted: %v", args)
		}
	}
}

func TestMissingSettingsAndLegacyUseDefaults(t *testing.T) {
	root := t.TempDir()
	config, err := load(nil, filepath.Join(root, "home"), filepath.Join(root, "project", "backend"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(config.Executors, model.DefaultExecutorConfigs()) || config.Listen != "127.0.0.1:4173" {
		t.Fatalf("unexpected defaults: %+v", config)
	}
}

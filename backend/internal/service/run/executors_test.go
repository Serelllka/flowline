package run

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"flowline/internal/model"
	"flowline/internal/repository/settings"
)

func TestExecutorSettingsLoadWithoutWriteAndPersistOutsideData(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	path := filepath.Join(root, "user-settings", "settings.json")
	engine := NewEngine(nil, data, path, nil, nil)
	defer engine.Close()
	profiles := model.DefaultExecutorConfigs()[:1]
	profiles[0].Model = "from-settings"
	if err := engine.InitializeExecutors(profiles); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("initialization rewrote settings: %v", err)
	}
	if !reflect.DeepEqual(engine.ExecutorConfigs(), profiles) {
		t.Fatal("profiles not initialized")
	}

	if err := engine.ConfigureExecutors(profiles); err != nil {
		t.Fatal(err)
	}
	config, err := settings.Load(path)
	if err != nil || !reflect.DeepEqual(config.Executors, profiles) {
		t.Fatalf("profiles not persisted at settings path: %+v, %v", config, err)
	}
	if _, err := os.Stat(data); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("configuration wrote into data directory: %v", err)
	}

	if err := os.WriteFile(path, []byte(`{"listen":"127.0.0.1:4567","executors":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := engine.ConfigureExecutors(profiles); err != nil {
		t.Fatal(err)
	}
	config, err = settings.Load(path)
	if err != nil || config.Listen != "127.0.0.1:4567" {
		t.Fatalf("UI profile save overwrote server settings: %+v, %v", config, err)
	}

	if err := os.WriteFile(path, []byte(`invalid json`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := engine.ConfigureExecutors(nil); err == nil {
		t.Fatal("malformed settings overwritten")
	}
	if !reflect.DeepEqual(engine.ExecutorConfigs(), profiles) {
		t.Fatal("failed persistence changed active profiles")
	}
}

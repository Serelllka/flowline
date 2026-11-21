package app

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"flowline/internal/repository/executorconfig"
	"flowline/internal/repository/settings"
)

// Config contains resolved paths and the settings used by this process.
type Config struct {
	settings.Config
	SettingsPath string
}

func Load(args []string) (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, fmt.Errorf("домашний каталог пользователя: %w", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return Config{}, err
	}

	return load(args, home, cwd)
}

func load(args []string, home, cwd string) (Config, error) {
	flags := flag.NewFlagSet("flowline", flag.ContinueOnError)
	settingsPath := flags.String("settings", filepath.Join(home, ".flowline", "settings.json"), "settings JSON file")
	listen := flags.String("listen", "127.0.0.1:4173", "loopback HTTP address (overrides settings)")
	data := flags.String("data", filepath.Join(home, ".flowline"), "shared local data directory (overrides settings)")
	frontend := flags.String("frontend", "../frontend", "frontend asset directory (overrides settings)")
	if err := flags.Parse(args); err != nil {
		return Config{}, err
	}
	if flags.NArg() != 0 {
		return Config{}, errors.New("Flowline принимает только именованные параметры запуска")
	}

	explicit := make(map[string]bool)
	flags.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	path, err := resolvePath(*settingsPath, cwd, home)
	if err != nil {
		return Config{}, fmt.Errorf("путь settings.json: %w", err)
	}

	fileConfig, err := settings.Load(path)
	missing := errors.Is(err, os.ErrNotExist) && !explicit["settings"]
	if err != nil && !missing {
		return Config{}, fmt.Errorf("настройки %s: %w", path, err)
	}
	if missing {
		fileConfig = settings.Default()
	}

	config := Config{Config: fileConfig, SettingsPath: path}
	if config.Listen == "" || explicit["listen"] {
		config.Listen = *listen
	}
	if config.Listen == "" {
		return Config{}, errors.New("--listen не может быть пустым")
	}

	dataBase := filepath.Dir(path)
	if config.DataDir == "" || explicit["data"] {
		config.DataDir, dataBase = *data, cwd
	}
	config.DataDir, err = resolvePath(config.DataDir, dataBase, home)
	if err != nil {
		return Config{}, fmt.Errorf("каталог данных: %w", err)
	}

	frontendBase := filepath.Dir(path)
	if config.FrontendDir == "" || explicit["frontend"] {
		config.FrontendDir, frontendBase = *frontend, cwd
	}
	config.FrontendDir, err = resolvePath(config.FrontendDir, frontendBase, home)
	if err != nil {
		return Config{}, fmt.Errorf("каталог интерфейса: %w", err)
	}

	// Older installations stored profiles next to SQLite. Read them only while
	// the default settings file is absent; a present settings.json is authoritative.
	if missing {
		config.Executors, err = executorconfig.LoadExecutorConfigs(filepath.Join(config.DataDir, "executors.json"))
		if err != nil {
			return Config{}, fmt.Errorf("прежние настройки исполнителей: %w", err)
		}
	}
	if err := settings.Validate(config.Config); err != nil {
		return Config{}, err
	}

	return config, nil
}

func resolvePath(value, base, home string) (string, error) {
	if value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("укажите непустой путь без управляющих символов")
	}
	if value == "~" {
		return home, nil
	}
	if strings.HasPrefix(value, "~/") {
		return filepath.Join(home, value[2:]), nil
	}
	if strings.HasPrefix(value, "~") {
		return "", errors.New("поддерживается только ~ или ~/путь текущего пользователя")
	}
	if !filepath.IsAbs(value) {
		return filepath.Clean(filepath.Join(base, value)), nil
	}

	return filepath.Clean(value), nil
}

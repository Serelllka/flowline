package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"flowline/internal/model"
)

const maxSize = 1024 * 1024

// Config contains application settings without credentials. Empty server fields
// leave the startup defaults in effect; Executors may explicitly be empty.
type Config struct {
	Listen      string                 `json:"listen,omitempty"`
	DataDir     string                 `json:"dataDir,omitempty"`
	FrontendDir string                 `json:"frontendDir,omitempty"`
	Executors   []model.ExecutorConfig `json:"executors"`
}

func Default() Config {
	return Config{Executors: model.DefaultExecutorConfigs()}
}

// Load reports missing files to the caller rather than silently replacing them
// with defaults. Omitted executor settings receive defaults; [] remains empty.
func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("открытие settings.json: %w", err)
	}
	defer file.Close()

	contents, err := io.ReadAll(io.LimitReader(file, maxSize+1))
	if err != nil {
		return Config{}, fmt.Errorf("чтение settings.json: %w", err)
	}

	if len(contents) > maxSize {
		return Config{}, errors.New("settings.json превышает 1 MiB")
	}

	config := Default()
	decoded := &config
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return Config{}, fmt.Errorf("формат settings.json: %w", err)
	}

	if decoded == nil {
		return Config{}, errors.New("settings.json должен содержать JSON-объект, а не null")
	}

	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Config{}, errors.New("settings.json должен содержать один JSON-объект")
	}

	if err := Validate(config); err != nil {
		return Config{}, err
	}

	return config, nil
}

func Validate(config Config) error {
	if config.Executors == nil {
		return errors.New("executors должен быть массивом; используйте [] вместо null")
	}

	if err := model.ValidateExecutorConfigs(config.Executors); err != nil {
		return err
	}

	if config.Listen != "" {
		host, port, err := net.SplitHostPort(config.Listen)
		ip := net.ParseIP(host)
		if err != nil || ip == nil || !ip.IsLoopback() {
			return errors.New("listen должен содержать loopback IP и порт, например 127.0.0.1:4173")
		}

		if port == "" || strings.IndexFunc(port, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return errors.New("порт listen должен быть числом от 0 до 65535")
		}

		if _, err := strconv.ParseUint(port, 10, 16); err != nil {
			return errors.New("порт listen должен быть числом от 0 до 65535")
		}
	}

	if strings.IndexFunc(config.DataDir+config.FrontendDir, unicode.IsControl) >= 0 {
		return errors.New("пути dataDir и frontendDir не должны содержать управляющие символы")
	}

	return nil
}

// SaveExecutors updates only the profiles and preserves the current server
// settings. Invalid existing documents are never overwritten with defaults.
func SaveExecutors(path string, configs []model.ExecutorConfig) error {
	if err := model.ValidateExecutorConfigs(configs); err != nil {
		return err
	}

	config, err := Load(path)
	if errors.Is(err, os.ErrNotExist) {
		config = Default()
	} else if err != nil {
		return err
	}

	config.Executors = append([]model.ExecutorConfig{}, configs...)
	contents, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}

	contents = append(contents, '\n')
	if len(contents) > maxSize {
		return errors.New("settings.json превышает 1 MiB")
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}

	file, err := os.CreateTemp(filepath.Dir(path), ".settings-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()

	if err := file.Chmod(0600); err != nil {
		return err
	}

	if _, err := file.Write(contents); err != nil {
		return err
	}

	if err := file.Sync(); err != nil {
		return err
	}

	if err := file.Close(); err != nil {
		return err
	}

	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}

	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()

	return directory.Sync()
}

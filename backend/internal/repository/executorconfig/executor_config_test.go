package executorconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestExecutorConfigDefaultsAndRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "executors.json")
	configs, err := LoadExecutorConfigs(path)
	if err != nil || len(configs) != 3 || configs[2].Model != "deepseek-flash" {
		t.Fatalf("defaults=%+v err=%v", configs, err)
	}

	configs[0].Model = "configured-model"
	contents, err := json.Marshal(configs)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, contents, 0600); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadExecutorConfigs(path)
	if err != nil || !reflect.DeepEqual(loaded, configs) {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}

func TestExecutorConfigRejectsSecretsAndMalformedInput(t *testing.T) {
	for _, contents := range []string{
		`[{"id":"api","name":"API","kind":"openai-compatible","apiKey":"secret-value"}]`,
		`[] []`, `not-json`, strings.Repeat(" ", 1024*1024+1),
	} {
		path := filepath.Join(t.TempDir(), "executors.json")
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadExecutorConfigs(path); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
}

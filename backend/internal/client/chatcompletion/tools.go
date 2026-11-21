package chatcompletion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"flowline/internal/cli/process"
)

// safeWorktreePath follows existing symlinks before accepting a file path. Git
// metadata and common credential files are not available to API file tools.
func safeWorktreePath(root, relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) || strings.ContainsRune(relative, '\x00') {
		return "", errors.New("нужен относительный путь внутри рабочего дерева")
	}
	clean := filepath.Clean(relative)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("путь выходит за рабочее дерево")
	}
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		lower := strings.ToLower(part)
		if lower == ".git" || lower == ".ssh" || lower == ".aws" || strings.HasPrefix(lower, ".env") || lower == "credentials" || lower == "id_rsa" || strings.HasSuffix(lower, ".pem") {
			return "", errors.New("служебные файлы Git и файлы секретов недоступны")
		}
	}

	base, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	target := filepath.Join(base, clean)
	ancestor := base
	for _, component := range strings.Split(clean, string(filepath.Separator)) {
		ancestor = filepath.Join(ancestor, component)
		info, err := os.Lstat(ancestor)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("API-инструмент не следует символическим ссылкам")
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return "", errors.New("доступны только обычные файлы и каталоги")
		}
	}

	return target, nil
}

func executeAPITool(ctx context.Context, cwd string, call apiToolCall, log func(string)) (string, error) {
	var args struct {
		Path    string   `json:"path"`
		Content string   `json:"content"`
		Argv    []string `json:"argv"`
	}
	decoder := json.NewDecoder(strings.NewReader(call.Function.Arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return "", fmt.Errorf("неверные аргументы инструмента: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return "", errors.New("лишние данные в аргументах инструмента")
	}

	switch call.Function.Name {
	case "list_files":
		out, _, err := process.RunProcess(ctx, []string{"git", "-c", "core.fsmonitor=false", "ls-files", "--cached", "--others", "--exclude-standard"}, cwd, "", nil)
		if len(out) > 64<<10 {
			out = out[:64<<10] + "\n[список сокращён]"
		}

		return out, err
	case "read_file", "write_file":
		_, err := safeWorktreePath(cwd, args.Path)
		if err != nil {
			return "", err
		}
		root, err := os.OpenRoot(cwd)
		if err != nil {
			return "", err
		}
		defer root.Close()
		if call.Function.Name == "write_file" {
			if len(args.Content) > 1<<20 {
				return "", errors.New("файл превышает 1 MiB")
			}
			if err := root.MkdirAll(filepath.Dir(args.Path), 0700); err != nil {
				return "", err
			}
			file, err := root.OpenFile(args.Path, os.O_CREATE|os.O_WRONLY|syscall.O_NONBLOCK, 0600)
			if err != nil {
				return "", err
			}
			defer file.Close()
			info, err := file.Stat()
			if err != nil {
				return "", err
			}
			if !info.Mode().IsRegular() {
				return "", errors.New("запись разрешена только в обычные файлы")
			}
			if err := file.Truncate(0); err != nil {
				return "", err
			}
			if _, err := file.WriteString(args.Content); err != nil {
				return "", err
			}

			return "Файл записан: " + args.Path, nil
		}
		file, err := root.OpenFile(args.Path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			return "", err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() {
			return "", errors.New("чтение разрешено только для обычных файлов")
		}
		data, err := io.ReadAll(io.LimitReader(file, (128<<10)+1))
		if len(data) > 128<<10 {
			return "", errors.New("файл превышает 128 KiB; выберите меньший файл")
		}

		return string(data), err
	case "run_check":
		if len(args.Argv) == 0 || len(args.Argv) > 100 {
			return "", errors.New("команда должна содержать от 1 до 100 аргументов")
		}
		if runtime.GOOS != "darwin" {
			return "", errors.New("изолированные API-проверки пока поддерживаются на macOS; используйте CLI-исполнитель для команд на этой ОС")
		}
		temporary, err := os.MkdirTemp("", "flowline-check-")
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(temporary)
		absolute, err := filepath.EvalSymlinks(cwd)
		if err != nil {
			return "", err
		}
		temporary, err = filepath.EvalSymlinks(temporary)
		if err != nil {
			return "", err
		}
		// sandbox-exec uses Scheme string literals; JSON encoding safely quotes
		// backslashes, quotes, and control bytes in paths supplied by the user.
		quotedRoot, _ := json.Marshal(absolute)
		quotedTemp, _ := json.Marshal(temporary)
		gitMarker, _ := json.Marshal(filepath.Join(absolute, ".git"))
		profile := fmt.Sprintf(`(version 1) (allow default) (deny network*) (deny file-write*) (allow file-write* (subpath %s) (subpath %s) (literal "/dev/null")) (deny file-write* (literal %s))`, quotedRoot, quotedTemp, gitMarker)
		if home, err := os.UserHomeDir(); err == nil {
			for _, sensitive := range []string{".ssh", ".aws", ".codex", ".claude", "Library/Keychains"} {
				quoted, _ := json.Marshal(filepath.Join(home, sensitive))
				profile += fmt.Sprintf(" (deny file-read* (subpath %s))", quoted)
			}
		}
		argv := []string{"/usr/bin/sandbox-exec", "-p", profile, "/usr/bin/env", "-i", "PATH=" + os.Getenv("PATH"), "TMPDIR=" + temporary, "GOCACHE=" + filepath.Join(temporary, "go-cache"), "PYTHONDONTWRITEBYTECODE=1"}
		argv = append(argv, args.Argv...)
		checkCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		if log != nil {
			encoded, _ := json.Marshal(args.Argv)
			log("Проверка: " + process.Redact(string(encoded)) + "\n")
		}
		out, code, err := process.RunProcess(checkCtx, argv, cwd, "", log)
		if len(out) > 64<<10 {
			out = out[:64<<10] + "\n[вывод сокращён]"
		}
		if log != nil {
			log(fmt.Sprintf("Код завершения проверки: %d\n", code))
		}

		return fmt.Sprintf("exit_code=%d\n%s", code, out), err
	default:
		return "", fmt.Errorf("неизвестный инструмент %q", call.Function.Name)
	}
}

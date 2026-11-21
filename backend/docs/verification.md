# Проверка Flowline — 2026-11-22

## Проверено

Финальная структура использует модуль `flowline` и конкретные подпакеты;
`backend/internal/flowline` удалён. Старые импорты `code-bench` отсутствуют.

- `make test` — PASS (с отдельным Go cache для окружения проверки).
  Go race suite: HTTP API, DAG, переименование и неоднозначные ссылки, SQLite,
  миграции/восстановление, конфигурации, GitHub, API tool loop, CLI-адаптеры,
  планировщик, отмена и process guardian. Frontend: 8/8 тестов.
- `make check` — PASS (`go vet` и JS syntax).
- `make build` — PASS; `bin/flowline` создан.
- Интеграционный сценарий HTTP-handler → SQLite → реальный локальный подпроцесс →
  сохранённый результат проходит в тестах без сетевого listener.
- После очистки повторно проверено без test cache: добавление настоящего Git-репозитория
  через POST API, повторное добавление без дубликата, выдача через bootstrap, сохранение
  пайплайна, выполнение и неизменность снимка. Все используемые HTML/CSS/JS-файлы
  проверены через HTTP-handler из реального каталога `frontend/`.
- Повторно прошли race-тесты планировщика, SQLite, конфигурации и settings: fork/join,
  ошибки зависимостей, отмена, сохранность истории после открытия БД заново и единый
  каталог данных при запуске из разных проектов. `go vet` и 8 frontend-тестов прошли.
- Текущий сервис также собран в отдельном временном каталоге, чтобы
  не возвращать сборочные файлы в очищенный проект. `--help` подтверждает стандартные
  пути `~/.flowline` и `~/.flowline/settings.json`.
- Реальные временные Git-репозитории проверяют fork/join, merge conflict, исходные
  staged/dirty/untracked изменения, попытки перенаправления `.git`, частичный diff.
- Снимки графа, Git-контекста и определений исполнителей не изменяются после
  редактирования пайплайна/профиля. Исторические имена в UI берутся из снимка.
- Guardian-тест аварийно убивает helper backend через SIGKILL и проверяет завершение
  целевого процесса и потомка, игнорирующих TERM.
- API-протокольные тесты проверяют реальные файловые операции, отказ на выход за
  worktree, symlink/FIFO, ошибки/отмену/redirects/секреты, неуспешные проверки и лимиты.

GOCACHE переопределён только в командах проверки: sandbox не позволяет писать в
обычный пользовательский Go cache. Приложение не навязывает этот путь пользователю.

## Не удалось подтвердить в текущем окружении

1. **Запуск сервера с пользовательским состоянием и браузерный основной сценарий.**
   Попытка `make run` 22 ноября остановилась на создании
   `~/.flowline/server.lock`: `operation not permitted`. Окружение сессии запрещает
   запись в домашний каталог. CUA снова вернул `The browser access host is unavailable`.
   HTTP-handler и статические assets проверены, но это не заменяет серверный и
   браузерный прогон. Пользователю передана команда запуска из обычного терминала;
   подтверждение запуска пока не получено.
2. **Настоящий Codex с авторизацией.** Opt-in smoke-test действительно запустил CLI,
   который завершился exit 1: `failed to initialize in-process app-server client:
   Operation not permitted`. Ошибка не подменялась успехом. Интеграционные тесты
   адаптеров используют реальные локальные протокольные subprocess-фикстуры.
3. **Claude/DeepSeek и GitHub с реальными аккаунтами.** Claude отсутствует в PATH;
   действительность аккаунтов/API-ключей не проверялась. GitHub проверен локальным
   CLI-протокольным фикстуром, API — тестовым HTTP RoundTripper. Эти фикстуры не
   доступны в рабочем режиме приложения.

## Следующий реальный smoke-test

В среде с разрешённым loopback запустить `make run`, добавить существующий Git
репозиторий и авторизованный профиль, сохранить граф, выполнить его, проверить
входы следующего блока, отмену и историю после перезапуска. Команда opt-in проверки
Codex приведена в README. Состояние этих проверок остаётся **не подтверждено**.

## Основания интеграций

Флаги Codex 0.171.0 и gh 2.103.3 проверены через локальный help.
Claude-флаги и API-протокол сверены с официальными источниками:

- [Codex noninteractive](https://learn.chatgpt.com/docs/non-interactive-mode)
- [Claude headless](https://code.claude.com/docs/en/headless)
- [Claude CLI reference](https://code.claude.com/docs/en/cli-reference)
- [DeepSeek chat completion](https://api-docs.deepseek.com/api/create-chat-completion/)
- [DeepSeek tool calls](https://api-docs.deepseek.com/guides/tool_calls/)
- [DeepSeek thinking mode](https://api-docs.deepseek.com/guides/thinking_mode/)
- [GitHub PR list](https://cli.github.com/manual/gh_pr_list)
- [GitHub PR view](https://cli.github.com/manual/gh_pr_view)

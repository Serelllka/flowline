package chatcompletion

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"flowline/internal/model"
	"flowline/internal/testutil"
)

type apiTestTransport func(*http.Request) (*http.Response, error)

func (f apiTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func apiTestExecutor(t *testing.T, transport apiTestTransport) *Executor {
	t.Helper()
	t.Setenv("FLOWLINE_TEST_CREDENTIAL", "opaque-flowline-credential-24862")
	executor, err := New(model.ExecutorConfig{ID: "api", Name: "API fixture", Kind: "openai-compatible", Model: "fixture-model", BaseURL: "https://api.deepseek.com", APIKeyEnv: "FLOWLINE_TEST_CREDENTIAL"})
	if err != nil {
		t.Fatal(err)
	}
	api := executor.(*Executor)
	api.client.Transport = transport

	return api
}

func apiTestResponse(request *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}
}

func apiTestChoice(t *testing.T, message apiMessage, finish string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": message, "finish_reason": finish}}})
	if err != nil {
		t.Fatal(err)
	}

	return string(body)
}

func apiTestCall(id, name, arguments string) apiToolCall {
	call := apiToolCall{ID: id, Type: "function"}
	call.Function.Name, call.Function.Arguments = name, arguments

	return call
}

func apiTestNode() model.Node {
	return model.Node{ID: "api-node", Name: "API", Outputs: []model.Output{{ID: "out", Name: "result"}}}
}

func TestAPIExecutorToolLoopWritesReadsAndListsActualFiles(t *testing.T) {
	repo, _ := testutil.Repository(t)
	calls := 0
	executor := apiTestExecutor(t, func(request *http.Request) (*http.Response, error) {
		calls++
		if request.URL.String() != "https://api.deepseek.com/chat/completions" || request.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", request.Method, request.URL)
		}
		if request.Header.Get("Authorization") != "Bearer "+os.Getenv("FLOWLINE_TEST_CREDENTIAL") {
			t.Error("backend did not set configured credential")
		}
		var payload struct {
			Model    string            `json:"model"`
			Messages []apiMessage      `json:"messages"`
			Thinking map[string]string `json:"thinking"`
			Tools    []struct {
				Function struct {
					Name       string `json:"name"`
					Parameters struct {
						Required json.RawMessage `json:"required"`
					} `json:"parameters"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Model != "fixture-model" || payload.Thinking["type"] != "disabled" {
			t.Error("model or DeepSeek thinking configuration missing")
		}
		for _, tool := range payload.Tools {
			if tool.Function.Name == "list_files" && string(tool.Function.Parameters.Required) != "[]" {
				t.Fatalf("zero-argument schema required must be [], got %s", tool.Function.Parameters.Required)
			}
		}
		if calls == 1 {
			message := apiMessage{Role: "assistant", ToolCalls: []apiToolCall{
				apiTestCall("write", "write_file", `{"path":"nested/note.txt","content":"real file content"}`),
				apiTestCall("read", "read_file", `{"path":"nested/note.txt"}`),
				apiTestCall("list", "list_files", `{}`),
			}}

			return apiTestResponse(request, 200, apiTestChoice(t, message, "tool_calls")), nil
		}
		if calls != 2 || len(payload.Messages) != 6 {
			t.Fatalf("unexpected tool transcript: calls=%d messages=%d", calls, len(payload.Messages))
		}
		if payload.Messages[3].ToolCallID != "write" || payload.Messages[4].Content != "real file content" || !strings.Contains(payload.Messages[5].Content.(string), "nested/note.txt") {
			t.Fatalf("actual tool outputs not passed back: %+v", payload.Messages)
		}

		return apiTestResponse(request, 200, apiTestChoice(t, apiMessage{Role: "assistant", Content: `{"result":"file created and inspected"}`}, "stop")), nil
	})

	outputs, code, err := executor.Execute(context.Background(), apiTestNode(), repo.Path, "Create and inspect a file", nil)
	if err != nil || code != 0 || outputs["result"] != "file created and inspected" || calls != 2 {
		t.Fatalf("API result: %v, code=%d, err=%v, requests=%d", outputs, code, err, calls)
	}
	contents, err := os.ReadFile(filepath.Join(repo.Path, "nested", "note.txt"))
	if err != nil || string(contents) != "real file content" {
		t.Fatalf("actual write missing: %q, %v", contents, err)
	}
}

func TestAPIExecutorRejectsIncompleteAndInvalidResponses(t *testing.T) {
	for _, reason := range []string{"length", "content_filter", "insufficient_system_resource", "aborted", "unknown", "tool_calls"} {
		t.Run(reason, func(t *testing.T) {
			executor := apiTestExecutor(t, func(request *http.Request) (*http.Response, error) {
				return apiTestResponse(request, 200, apiTestChoice(t, apiMessage{Role: "assistant", Content: `{"result":"claimed success"}`}, reason)), nil
			})

			_, code, err := executor.Execute(context.Background(), apiTestNode(), t.TempDir(), "prompt", nil)
			if err == nil || code == 0 {
				t.Fatalf("incomplete response treated as success: code=%d, err=%v", code, err)
			}
		})
	}
	for name, body := range map[string]string{
		"invalid JSON": "not JSON",
		"no choices":   `{"choices":[]}`,
		"empty text":   `{"choices":[{"message":{"role":"assistant","content":""},"finish_reason":"stop"}]}`,
		"null text":    `{"choices":[{"message":{"role":"assistant","content":null},"finish_reason":"stop"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			executor := apiTestExecutor(t, func(request *http.Request) (*http.Response, error) {
				return apiTestResponse(request, 200, body), nil
			})

			if _, _, err := executor.Execute(context.Background(), apiTestNode(), t.TempDir(), "prompt", nil); err == nil {
				t.Fatal("invalid API response accepted")
			}
		})
	}
}

func TestAPIExecutorDoesNotConvertFailedCheckToSuccess(t *testing.T) {
	requests := 0
	executor := apiTestExecutor(t, func(request *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			message := apiMessage{Role: "assistant", ToolCalls: []apiToolCall{apiTestCall("check", "run_check", `{"argv":[]}`)}}

			return apiTestResponse(request, 200, apiTestChoice(t, message, "tool_calls")), nil
		}

		return apiTestResponse(request, 200, apiTestChoice(t, apiMessage{Role: "assistant", Content: `{"result":"all tests passed"}`}, "stop")), nil
	})

	outputs, code, err := executor.Execute(context.Background(), apiTestNode(), t.TempDir(), "Run tests", nil)
	if err == nil || code == 0 || outputs != nil || requests != 2 {
		t.Fatalf("failed actual check replaced by model success: outputs=%v code=%d err=%v", outputs, code, err)
	}
}

func TestAPIExecutorRedactsConfiguredCredentialAndRefusesRedirects(t *testing.T) {
	t.Run("HTTP error", func(t *testing.T) {
		executor := apiTestExecutor(t, func(request *http.Request) (*http.Response, error) {
			return apiTestResponse(request, 401, "rejected "+os.Getenv("FLOWLINE_TEST_CREDENTIAL")), nil
		})

		_, code, err := executor.Execute(context.Background(), apiTestNode(), t.TempDir(), "prompt", nil)
		if err == nil || code != 401 || strings.Contains(err.Error(), os.Getenv("FLOWLINE_TEST_CREDENTIAL")) || !strings.Contains(err.Error(), "[REDACTED]") {
			t.Fatalf("credential leaked or HTTP error lost: code=%d err=%v", code, err)
		}
	})
	t.Run("final output", func(t *testing.T) {
		executor := apiTestExecutor(t, func(request *http.Request) (*http.Response, error) {
			output, _ := json.Marshal(map[string]string{"result": os.Getenv("FLOWLINE_TEST_CREDENTIAL")})

			return apiTestResponse(request, 200, apiTestChoice(t, apiMessage{Role: "assistant", Content: string(output)}, "stop")), nil
		})

		outputs, _, err := executor.Execute(context.Background(), apiTestNode(), t.TempDir(), "prompt", nil)
		if err != nil || outputs["result"] != "[REDACTED]" {
			t.Fatalf("credential persisted in output: %v %v", outputs, err)
		}
	})
	t.Run("redirect", func(t *testing.T) {
		calls := 0
		executor := apiTestExecutor(t, func(request *http.Request) (*http.Response, error) {
			calls++
			response := apiTestResponse(request, http.StatusFound, "redirect")
			response.Header.Set("Location", "https://other.example/chat/completions")

			return response, nil
		})

		_, code, err := executor.Execute(context.Background(), apiTestNode(), t.TempDir(), "prompt", nil)
		if err == nil || code != http.StatusFound || calls != 1 {
			t.Fatalf("redirect followed or accepted: calls=%d code=%d err=%v", calls, code, err)
		}
	})
}

func TestAPIExecutorCancellationAndMissingCredential(t *testing.T) {
	started := make(chan struct{})
	executor := apiTestExecutor(t, func(request *http.Request) (*http.Response, error) {
		close(started)
		<-request.Context().Done()

		return nil, request.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	cwd := t.TempDir()
	go func() {
		_, _, err := executor.Execute(ctx, apiTestNode(), cwd, "prompt", nil)
		done <- err
	}()
	<-started

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation not propagated: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("API request did not stop")
	}

	t.Setenv("FLOWLINE_TEST_CREDENTIAL", "")
	if executor.Info().Available {
		t.Fatal("missing API credential reported available")
	}
	if _, _, err := executor.Execute(context.Background(), apiTestNode(), cwd, "prompt", nil); err == nil {
		t.Fatal("missing API credential accepted")
	}
}

func TestAPIToolsRejectTraversalSymlinksSpecialFilesAndMalformedArguments(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("private fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"alias": ".env", "dangling": filepath.Join(outside, "new-file"), "external": outside} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := syscall.Mkfifo(filepath.Join(root, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../outside", filepath.Join(outside, "absolute"), ".git", ".env", ".aws/credentials", "key.pem", "alias", "dangling", "external/file", "fifo"} {
		for _, tool := range []string{"read_file", "write_file"} {
			t.Run(tool+"/"+path, func(t *testing.T) {
				arguments, _ := json.Marshal(map[string]string{"path": path, "content": "must not escape"})
				completed := make(chan error, 1)
				go func() {
					_, err := executeAPITool(context.Background(), root, apiTestCall("call", tool, string(arguments)), nil)
					completed <- err
				}()

				select {
				case err := <-completed:
					if err == nil {
						t.Fatal("unsafe path accepted")
					}
				case <-time.After(2 * time.Second):
					t.Fatal("special file blocked tool and cancellation")
				}
			})
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "new-file")); !os.IsNotExist(err) {
		t.Fatalf("dangling symlink wrote outside tree: %v", err)
	}
	contents, _ := os.ReadFile(filepath.Join(root, ".env"))
	if string(contents) != "private fixture" {
		t.Fatal("symlink alias overwrote protected file")
	}
	for _, arguments := range []string{"not JSON", `{"path":"ok","unexpected":1}`, `{"path":"ok"} {}`, `{"path":7}`} {
		if _, err := executeAPITool(context.Background(), root, apiTestCall("malformed", "read_file", arguments), nil); err == nil {
			t.Errorf("malformed args accepted: %s", arguments)
		}
	}
}

func TestAPIExecutorStopsAtToolRoundLimit(t *testing.T) {
	var calls atomic.Int32
	executor := apiTestExecutor(t, func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		message := apiMessage{Role: "assistant", ToolCalls: []apiToolCall{apiTestCall("read", "read_file", `{"path":"missing.txt"}`)}}

		return apiTestResponse(request, 200, apiTestChoice(t, message, "tool_calls")), nil
	})

	_, code, err := executor.Execute(context.Background(), apiTestNode(), t.TempDir(), "prompt", nil)
	if err == nil || code == 0 || calls.Load() != 30 {
		t.Fatalf("tool loop limit failed: calls=%d code=%d err=%v", calls.Load(), code, err)
	}
}

func TestCheckRetryIdentityIgnoresJSONFormatting(t *testing.T) {
	first := checkIdentity(`{"argv":["go","test","./..."]}`)
	retry := checkIdentity("{\n  \"argv\": [ \"go\", \"test\", \"./...\" ]\n}")
	if first != retry {
		t.Fatal("same check was treated as a different retry")
	}
	if first == checkIdentity(`{"argv":["go","test","./other"]}`) {
		t.Fatal("different check erased prior failure")
	}
}

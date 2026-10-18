package runtimewire

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A caller failure or cancellation is terminal, never a request to synthesize
// another answer. Drain SSE to EOF so a later model request cannot escape checks.
func TestRunDoesNotRegenerateAfterCallerErrorOrCancel(t *testing.T) {
	for _, mode := range []string{"caller-error", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(New(testToken))
			defer server.Close()
			req := request("reuse-terminal-" + mode)
			response := post(t, server.URL+"/v1/runs", req, testToken, "")
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("start status=%d", response.StatusCode)
			}
			scanner := bufio.NewScanner(response.Body)
			modelCalls, terminal := 0, ""
			for scanner.Scan() {
				if !strings.HasPrefix(scanner.Text(), "data: ") {
					continue
				}
				var event Event
				if err := json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &event); err != nil {
					t.Fatal(err)
				}
				switch event.Type {
				case "model.request":
					modelCalls++
					if modelCalls != 1 {
						t.Fatal("model request repeated after caller error or cancellation")
					}
					path := "/responses"
					var body any = Response{RequestID: event.RequestID, Error: "authorized model call failed"}
					if mode == "cancel" {
						path, body = "/cancel", map[string]any{}
					}
					callback := post(t, server.URL+"/v1/runs/"+req.RunID+path, body, testToken, runToken)
					_ = callback.Body.Close()
					if callback.StatusCode != http.StatusNoContent {
						t.Fatalf("terminal callback status=%d", callback.StatusCode)
					}
				case "tool.request", "message.delta", "run.completed":
					t.Fatalf("unexpected continuation after terminal callback: %s", event.Type)
				case "run.failed", "run.cancelled":
					if terminal != "" {
						t.Fatal("duplicate terminal event")
					}
					terminal = event.Type
				}
			}
			if err := scanner.Err(); err != nil {
				t.Fatal(err)
			}
			want := "run.failed"
			if mode == "cancel" {
				want = "run.cancelled"
			}
			if modelCalls != 1 || terminal != want {
				t.Fatalf("model calls=%d terminal=%s want=%s", modelCalls, terminal, want)
			}
		})
	}
}

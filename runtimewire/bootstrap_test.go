package runtimewire

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInitialToolCallsRunBeforeModelAndFeedStreamingAnswer(t *testing.T) {
	server := httptest.NewServer(New(testToken))
	defer server.Close()

	req := request("bootstrap-order")
	req.MaxIterations = 1
	req.Tools = append(req.Tools, Tool{Type: "function", Function: Function{
		Name: "skill_http_request", Description: "authorized skill API",
		Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
	}})
	body := requestWithInitialCalls(t, req, `[
		{"id":"bootstrap-skill","type":"function","function":{"name":"skills_read","arguments":"{\"skill\":\"conversation-image-understanding\"}"}},
		{"id":"bootstrap-image","type":"function","function":{"name":"skill_http_request","arguments":"{\"path\":\"/ai/scai/skill-tools/conversation-images/describe\"}"}}
	]`)
	response := rawPost(t, server.URL+"/v1/runs", body)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("bootstrap request rejected: status=%d", response.StatusCode)
	}

	scanner := bufio.NewScanner(response.Body)
	toolIndex, modelCalls := 0, 0
	bootstrapAdvertised, completed := false, false
	var deltas strings.Builder
	wantCalls := []struct{ id, name, arguments string }{
		{"bootstrap-skill", "skills_read", `{"skill":"conversation-image-understanding"}`},
		{"bootstrap-image", "skill_http_request", `{"path":"/ai/scai/skill-tools/conversation-images/describe"}`},
	}
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		switch event.Type {
		case "run.started":
			for _, feature := range event.Features {
				if feature == "tool.bootstrap" {
					bootstrapAdvertised = true
				}
			}
		case "tool.request":
			if modelCalls != 0 || toolIndex >= len(wantCalls) || event.Call == nil {
				t.Fatalf("bootstrap tool request was not before first model: model=%d call=%#v", modelCalls, event.Call)
			}
			want := wantCalls[toolIndex]
			if event.Call.ID != want.id || event.Call.Function.Name != want.name || event.Call.Function.Arguments != want.arguments {
				t.Fatalf("bootstrap call %d = %#v", toolIndex, event.Call)
			}
			toolIndex++
			result := "authorized result " + want.id
			callback, _ := json.Marshal(Response{RequestID: event.RequestID, Result: &result})
			postCallback(t, server.URL, event.RunID, string(callback))
		case "model.request":
			modelCalls++
			var assistantCalls, observations int
			for _, message := range event.Messages {
				if message.Role == "assistant" {
					for _, call := range message.ToolCalls {
						if call.ID == "bootstrap-skill" || call.ID == "bootstrap-image" {
							assistantCalls++
						}
					}
				}
				if message.Role == "tool" && (message.ToolCallID == "bootstrap-skill" || message.ToolCallID == "bootstrap-image") {
					observations++
				}
			}
			if assistantCalls != 2 || observations != 2 {
				t.Fatalf("bootstrap history calls=%d observations=%d messages=%#v", assistantCalls, observations, event.Messages)
			}
			answer := "图片读取结果已进入本轮证据。"
			delta, _ := json.Marshal(Response{RequestID: event.RequestID, Delta: answer})
			postCallback(t, server.URL, event.RunID, string(delta))
			message := Message{Role: "assistant", Content: answer}
			callback, _ := json.Marshal(Response{RequestID: event.RequestID, Message: &message})
			postCallback(t, server.URL, event.RunID, string(callback))
		case "message.delta":
			deltas.WriteString(event.Delta)
		case "run.completed":
			completed = event.Final == "图片读取结果已进入本轮证据。"
		case "run.failed":
			t.Fatalf("run failed: %s", event.Error)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !bootstrapAdvertised || toolIndex != 2 || modelCalls != 1 || !completed || deltas.String() != "图片读取结果已进入本轮证据。" {
		t.Fatalf("feature=%v tools=%d models=%d completed=%v deltas=%q", bootstrapAdvertised, toolIndex, modelCalls, completed, deltas.String())
	}
}

func TestBootstrapToolFailureRemainsAnExplicitModelObservation(t *testing.T) {
	server := httptest.NewServer(New(testToken))
	defer server.Close()
	req := request("bootstrap-failure")
	req.MaxIterations = 1
	body := requestWithInitialCalls(t, req, `[{"id":"read-skill","type":"function","function":{"name":"skills_read","arguments":"{}"}}]`)
	response := rawPost(t, server.URL+"/v1/runs", body)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("bootstrap request rejected: status=%d", response.StatusCode)
	}

	scanner := bufio.NewScanner(response.Body)
	failedObservationSeen, completed := false, false
	for scanner.Scan() {
		if !strings.HasPrefix(scanner.Text(), "data: ") {
			continue
		}
		var event Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		switch event.Type {
		case "tool.request":
			callback, _ := json.Marshal(Response{RequestID: event.RequestID, Error: "caller denied tool"})
			postCallback(t, server.URL, event.RunID, string(callback))
		case "model.request":
			for _, message := range event.Messages {
				if message.Role == "tool" && message.ToolCallID == "read-skill" && message.Content == `{"isError":true,"error":"bootstrap tool failed"}` {
					failedObservationSeen = true
				}
			}
			answer := "图片理解步骤失败，无法确认图片内容。"
			delta, _ := json.Marshal(Response{RequestID: event.RequestID, Delta: answer})
			postCallback(t, server.URL, event.RunID, string(delta))
			message := Message{Role: "assistant", Content: answer}
			callback, _ := json.Marshal(Response{RequestID: event.RequestID, Message: &message})
			postCallback(t, server.URL, event.RunID, string(callback))
		case "run.completed":
			completed = event.Final == "图片理解步骤失败，无法确认图片内容。"
		case "run.failed":
			t.Fatalf("run failed instead of returning failed observation: %s", event.Error)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !failedObservationSeen || !completed {
		t.Fatalf("failed observation=%v completed=%v", failedObservationSeen, completed)
	}
}

func TestInitialToolCallsRejectInvalidOrUnsafeBootstrap(t *testing.T) {
	validCall := `{"id":"bootstrap-1","type":"function","function":{"name":"skills_read","arguments":"{}"}}`
	secondCall := `{"id":"bootstrap-2","type":"function","function":{"name":"skills_read","arguments":"{}"}}`
	cases := []struct {
		name  string
		calls string
	}{
		{"undeclared tool", `[{"id":"bootstrap-1","type":"function","function":{"name":"unknown_tool","arguments":"{}"}}]`},
		{"invalid ID", `[{"id":"bad/id","type":"function","function":{"name":"skills_read","arguments":"{}"}}]`},
		{"wrong call type", `[{"id":"bootstrap-1","type":"other","function":{"name":"skills_read","arguments":"{}"}}]`},
		{"duplicate IDs", `[` + validCall + `,` + strings.Replace(secondCall, "bootstrap-2", "bootstrap-1", 1) + `]`},
		{"invalid JSON arguments", `[{"id":"bootstrap-1","type":"function","function":{"name":"skills_read","arguments":"not-json"}}]`},
		{"non-object arguments", `[{"id":"bootstrap-1","type":"function","function":{"name":"skills_read","arguments":"[]"}}]`},
		{"raw image bytes", `[{"id":"bootstrap-1","type":"function","function":{"name":"skills_read","arguments":"{\"imageData\":\"data:image/png;base64,AAAA\"}"}}]`},
		{"raw identity", `[{"id":"bootstrap-1","type":"function","function":{"name":"skills_read","arguments":"{\"userId\":42}"}}]`},
		{"raw identity alias", `[{"id":"bootstrap-1","type":"function","function":{"name":"skills_read","arguments":"{\"raw_identity\":\"person-1\"}"}}]`},
		{"extra metadata", `[{"id":"bootstrap-1","type":"function","extra":{"raw":"value"},"function":{"name":"skills_read","arguments":"{}"}}]`},
		{"more than two calls", `[` + validCall + `,` + secondCall + `,{"id":"bootstrap-3","type":"function","function":{"name":"skills_read","arguments":"{}"}}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := request("reject-bootstrap")
			assertRunRejected(t, requestWithInitialCalls(t, req, tc.calls))
		})
	}
}

func requestWithInitialCalls(t *testing.T, req Request, calls string) string {
	t.Helper()
	encoded, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	payload["initialToolCalls"] = json.RawMessage(calls)
	encoded, err = json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

package runtimewire

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

const testToken = "test-service-token"
const runToken = "test-per-run-secret-at-least-24-characters"

func post(t *testing.T, url string, body any, service, run string) *http.Response {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("POST", url, bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	if service != "" {
		req.Header.Set("Authorization", "Bearer "+service)
	}
	if run != "" {
		req.Header.Set("X-VSJin-Run-Token", run)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
func request(id string) Request {
	return Request{Version: "1", RunID: id, RunToken: runToken, Messages: []Message{{Role: "system", Content: "已授权drawing-analysis技能"}, {Role: "user", Content: "核对图纸标高"}}, Tools: []Tool{{Type: "function", Function: Function{Name: "skills_read", Description: "read authorized skill", Parameters: json.RawMessage(`{"type":"object","properties":{"skill":{"type":"string"}},"required":["skill"]}`)}}}, MaxIterations: 4, TimeoutSeconds: 5}
}
func TestRunModelToolAndFinalThroughRealHTTP(t *testing.T) {
	server := httptest.NewServer(New(testToken))
	defer server.Close()
	req := request("round-trip")
	req.MaxIterations = 2
	response := post(t, server.URL+"/v1/runs", req, testToken, "")
	defer response.Body.Close()
	if response.StatusCode != 200 {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("start=%d %s", response.StatusCode, data)
	}
	scanner := bufio.NewScanner(response.Body)
	var modelCalls, toolCalls, sequence int
	completed := false
	var answerDeltas strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		if event.Version != "1" || event.RunID != "round-trip" || event.Sequence != sequence+1 {
			t.Fatalf("bad envelope %#v", event)
		}
		sequence = event.Sequence
		var reply Response
		switch event.Type {
		case "model.request":
			modelCalls++
			msg := Message{Role: "assistant", Content: "<think>内部证据核对</think>图纸给定标高为45.000m，不能据此免除其他区域要求。"}
			if modelCalls == 1 {
				if len(event.Tools) != 1 || event.Tools[0].Function.Name != "skills_read" {
					t.Fatal("tool grant changed")
				}
				msg.Content = "先读取授权技能"
				msg.ToolCalls = []schema.ToolCall{{ID: "call-1", Type: "function", Function: schema.FunctionCall{Name: "skills_read", Arguments: `{"skill":"drawing-analysis"}`}}}
			} else {
				if len(event.Tools) != 0 {
					t.Fatal("final answer round must not receive tools")
				}
				toolEvidence := false
				for _, message := range event.Messages {
					if message.Role == "tool" {
						toolEvidence = true
					}
				}
				if !toolEvidence {
					t.Fatal("tool evidence did not reach model")
				}
			}
			reply = Response{RequestID: event.RequestID, Message: &msg}
		case "tool.request":
			toolCalls++
			if event.Call.ID != "call-1" || event.Call.Function.Name != "skills_read" {
				t.Fatal("tool identity changed")
			}
			denied := post(t, server.URL+"/v1/runs/round-trip/responses", Response{RequestID: event.RequestID, Result: new(string)}, testToken, "foreign-run-token")
			denied.Body.Close()
			if denied.StatusCode != 403 {
				t.Fatalf("foreign run token status=%d", denied.StatusCode)
			}
			result := "只读取授权图纸，工具后形成最终答复"
			reply = Response{RequestID: event.RequestID, Result: &result}
		case "message.delta":
			answerDeltas.WriteString(event.Delta)
		case "run.completed":
			completed = true
			if answerDeltas.String() != event.Final {
				t.Fatal("delta and final answer differ")
			}
			if !strings.Contains(event.Final, "45.000") || strings.Contains(event.Final, "think>") || strings.Contains(event.Final, "内部证据核对") {
				t.Fatal("missing final")
			}
		case "run.failed":
			t.Fatalf("unexpected failure %s", event.Error)
		}
		if reply.RequestID != "" {
			r := post(t, server.URL+"/v1/runs/round-trip/responses", reply, testToken, runToken)
			r.Body.Close()
			if r.StatusCode != 204 {
				t.Fatalf("callback status=%d", r.StatusCode)
			}
			duplicate := post(t, server.URL+"/v1/runs/round-trip/responses", reply, testToken, runToken)
			duplicate.Body.Close()
			if duplicate.StatusCode != 409 && duplicate.StatusCode != 404 {
				t.Fatalf("duplicate status=%d", duplicate.StatusCode)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !completed || modelCalls != 2 || toolCalls != 1 {
		t.Fatalf("incomplete chain completed=%v model=%d tool=%d", completed, modelCalls, toolCalls)
	}
}

func TestRunForwardsModelDeltasBeforeFinalResponse(t *testing.T) {
	server := httptest.NewServer(New(testToken))
	defer server.Close()
	req := request("incremental")
	req.MaxIterations = 2
	response := post(t, server.URL+"/v1/runs", req, testToken, "")
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	var runID, requestID string
	var answer strings.Builder
	deltas := []string{"第一段", "第二段", "第三段"}
	seenDeltas, modelCalls := 0, 0
	completed := false
	for scanner.Scan() {
		if !strings.HasPrefix(scanner.Text(), "data: ") {
			continue
		}
		var event Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		runID = event.RunID
		switch event.Type {
		case "model.request":
			modelCalls++
			requestID = event.RequestID
			if modelCalls == 1 {
				if len(event.Tools) != 1 {
					t.Fatal("decision round did not include tools")
				}
				draft := Message{Role: "assistant", Content: "draft answer"}
				result := post(t, server.URL+"/v1/runs/"+runID+"/responses", Response{RequestID: requestID, Message: &draft}, testToken, runToken)
				result.Body.Close()
				if result.StatusCode != http.StatusNoContent {
					t.Fatalf("decision callback status=%d", result.StatusCode)
				}
				continue
			}
			if len(event.Messages) == 0 || event.Messages[len(event.Messages)-1].Role != "user" {
				t.Fatal("final answer request must end in a generation-triggering user message")
			}
			if modelCalls != 2 || len(event.Tools) != 0 {
				t.Fatalf("answer round=%d tools=%d", modelCalls, len(event.Tools))
			}
			for i, delta := range deltas {
				result := post(t, server.URL+"/v1/runs/"+runID+"/responses", Response{RequestID: requestID, Delta: delta}, testToken, runToken)
				result.Body.Close()
				if result.StatusCode != http.StatusNoContent {
					t.Fatalf("delta callback %d status=%d", i, result.StatusCode)
				}
				want := strings.Join(deltas[:i+1], "")
				for answer.String() != want {
					line, err := scannerLine(t, scanner)
					if err != nil {
						t.Fatal(err)
					}
					if strings.HasPrefix(line, "data: ") {
						var streamed Event
						if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &streamed); err != nil {
							t.Fatal(err)
						}
						if streamed.Type == "message.delta" {
							answer.WriteString(streamed.Delta)
							seenDeltas++
						} else if streamed.Type == "run.failed" {
							t.Fatalf("run failed before final response: %s", streamed.Error)
						}
					}
				}
			}
			final := strings.Join(deltas, "")
			message := Message{Role: "assistant", Content: final}
			result := post(t, server.URL+"/v1/runs/"+runID+"/responses", Response{RequestID: requestID, Message: &message}, testToken, runToken)
			result.Body.Close()
			if result.StatusCode != http.StatusNoContent {
				t.Fatalf("final callback status=%d", result.StatusCode)
			}
		case "run.completed":
			completed = true
			if answer.String() != event.Final || event.Final != strings.Join(deltas, "") {
				t.Fatalf("stream=%q final=%q", answer.String(), event.Final)
			}
		case "run.failed":
			t.Fatalf("run failed: %s", event.Error)
		}
		if completed {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !completed || seenDeltas != len(deltas) || modelCalls != 2 {
		t.Fatalf("completed=%v deltas=%d model calls=%d", completed, seenDeltas, modelCalls)
	}
}

func scannerLine(t *testing.T, scanner *bufio.Scanner) (string, error) {
	t.Helper()
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", err
		}
		return "", io.EOF
	}
	return scanner.Text(), nil
}
func TestEmptyFinalAndCancellation(t *testing.T) {
	for _, mode := range []string{"empty", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			h := New(testToken)
			server := httptest.NewServer(h)
			defer server.Close()
			resp := post(t, server.URL+"/v1/runs", request(mode), testToken, "")
			defer resp.Body.Close()
			scanner := bufio.NewScanner(resp.Body)
			terminal := ""
			for scanner.Scan() {
				if !strings.HasPrefix(scanner.Text(), "data: ") {
					continue
				}
				var e Event
				json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &e)
				if e.Type == "model.request" {
					path := "/responses"
					reply := any(Response{RequestID: e.RequestID, Message: &Message{Role: "assistant"}})
					if mode == "cancel" {
						path = "/cancel"
						reply = map[string]string{}
					}
					r := post(t, server.URL+"/v1/runs/"+mode+path, reply, testToken, runToken)
					r.Body.Close()
					if r.StatusCode != 204 {
						t.Fatalf("reply=%d", r.StatusCode)
					}
				}
				if e.Type == "run.completed" {
					t.Fatal("empty or canceled run marked successful")
				}
				if e.Type == "run.failed" || e.Type == "run.cancelled" {
					terminal = e.Type
				}
			}
			want := "run.failed"
			if mode == "cancel" {
				want = "run.cancelled"
			}
			if terminal != want {
				t.Fatalf("terminal=%s want=%s", terminal, want)
			}
		})
	}
}
func TestRuntimeServiceAuthAndUnsupportedInput(t *testing.T) {
	server := httptest.NewServer(New(testToken))
	defer server.Close()
	r := post(t, server.URL+"/v1/runs", request("auth"), "", "")
	r.Body.Close()
	if r.StatusCode != 401 {
		t.Fatal(r.StatusCode)
	}
	invalid := map[string]any{"version": "1", "runId": "bad", "runToken": runToken, "messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "image_url"}}}}, "maxIterations": 4, "timeoutSeconds": 5}
	r = post(t, server.URL+"/v1/runs", invalid, testToken, "")
	r.Body.Close()
	if r.StatusCode != 400 {
		t.Fatalf("silently accepted unsupported multimodal: %d", r.StatusCode)
	}
}

func TestLastModelRoundSummarizesToolErrorsWithoutMoreTools(t *testing.T) {
	server := httptest.NewServer(New(testToken))
	defer server.Close()
	req := request("final-budget")
	req.MaxIterations = 2
	response := post(t, server.URL+"/v1/runs", req, testToken, "")
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	calls, completed := 0, false
	for scanner.Scan() {
		if !strings.HasPrefix(scanner.Text(), "data: ") {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &e); err != nil {
			t.Fatal(err)
		}
		var reply Response
		switch e.Type {
		case "model.request":
			calls++
			msg := Message{Role: "assistant", Content: "检索请求失败，当前不能确认项目要求；请补充有效资料。"}
			if calls == 1 {
				if len(e.Tools) != 1 {
					t.Fatal("first round missing authorized tool")
				}
				msg.ToolCalls = []schema.ToolCall{{ID: "failed-search", Type: "function", Function: schema.FunctionCall{Name: "skills_read", Arguments: `{"skill":"drawing-analysis"}`}}}
			} else {
				if len(e.Messages) == 0 || e.Messages[len(e.Messages)-1].Role != "user" {
					t.Fatal("budgeted final answer request must end in a user message")
				}
				if len(e.Tools) != 0 {
					t.Fatal("final round must reserve answer budget, not offer further tools")
				}
				encoded, _ := json.Marshal(e.Messages)
				if !strings.Contains(string(encoded), "isError") {
					t.Fatal("tool error evidence not preserved")
				}
			}
			reply = Response{RequestID: e.RequestID, Message: &msg}
		case "tool.request":
			result := `{"isError":true,"content":[{"type":"text","text":"HTTP 400 invalid query"}]}`
			reply = Response{RequestID: e.RequestID, Result: &result}
		case "run.completed":
			completed = true
		case "run.failed":
			t.Fatalf("unexpected failure: %s", e.Error)
		}
		if reply.RequestID != "" {
			res := post(t, server.URL+"/v1/runs/final-budget/responses", reply, testToken, runToken)
			res.Body.Close()
			if res.StatusCode != 204 {
				t.Fatal(res.StatusCode)
			}
		}
	}
	if !completed || calls != 2 {
		t.Fatalf("completed=%v calls=%d", completed, calls)
	}
}

func TestOneRoundBudgetOnlyAnswersAndRejectsToolCalls(t *testing.T) {
	for _, name := range []string{"answer-only", "unexpected-final-tool"} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(New(testToken))
			defer server.Close()
			req := request(name)
			req.MaxIterations = 1
			response := post(t, server.URL+"/v1/runs", req, testToken, "")
			defer response.Body.Close()
			scanner := bufio.NewScanner(response.Body)
			terminal := ""
			for scanner.Scan() {
				if !strings.HasPrefix(scanner.Text(), "data: ") {
					continue
				}
				var e Event
				if err := json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &e); err != nil {
					t.Fatal(err)
				}
				if e.Type == "model.request" {
					if len(e.Tools) != 0 {
						t.Fatal("one-round budget offered tools")
					}
					msg := Message{Role: "assistant", Content: "当前没有取得图纸资料，不能确认标高。"}
					if name == "unexpected-final-tool" {
						msg.ToolCalls = []schema.ToolCall{{ID: "extra", Type: "function", Function: schema.FunctionCall{Name: "skills_read", Arguments: `{}`}}}
					}
					res := post(t, server.URL+"/v1/runs/"+name+"/responses", Response{RequestID: e.RequestID, Message: &msg}, testToken, runToken)
					res.Body.Close()
					if res.StatusCode != 204 {
						t.Fatal(res.StatusCode)
					}
				}
				if e.Type == "tool.request" {
					t.Fatal("executed tool beyond budget")
				}
				if e.Type == "run.completed" || e.Type == "run.failed" {
					terminal = e.Type
				}
			}
			want := "run.completed"
			if name == "unexpected-final-tool" {
				want = "run.failed"
			}
			if terminal != want {
				t.Fatalf("terminal=%s want=%s", terminal, want)
			}
		})
	}
}

func TestFinalAnswerDoesNotExposeProviderReasoningEnvelope(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"不要输出内部思考。</think>## 结论\n未取得正式工程依据。", "## 结论\n未取得正式工程依据。"},
		{"<think>核对图纸证据</think>答案是 -2.500m。", "答案是 -2.500m。"},
		{"日志原文包含 </think>，这是引用内容。", "日志原文包含 </think>，这是引用内容。"},
		{"普通最终答复。", "普通最终答复。"},
	}
	for _, c := range cases {
		if got := visibleFinalAnswer(c.raw); got != c.want {
			t.Fatalf("got=%q want=%q", got, c.want)
		}
	}
}

func TestFinalProtocolRetriesOnceWithSummaryOnlyEvidence(t *testing.T) {
	server := httptest.NewServer(New(testToken))
	defer server.Close()
	req := request("final-protocol-retry")
	req.MaxIterations = 2
	response := post(t, server.URL+"/v1/runs", req, testToken, "")
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	modelCalls, completed := 0, false
	const detail = "private full tool body "
	for scanner.Scan() {
		if !strings.HasPrefix(scanner.Text(), "data: ") {
			continue
		}
		var event Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		var reply Response
		switch event.Type {
		case "model.request":
			modelCalls++
			message := Message{Role: "assistant", Content: "资料摘要显示共 419 行。"}
			switch modelCalls {
			case 1:
				message.ToolCalls = []schema.ToolCall{{ID: "read", Type: "function", Function: schema.FunctionCall{Name: "skills_read", Arguments: `{}`}}}
			case 2:
				if len(event.Tools) != 0 {
					t.Fatal("final answer request retained tools")
				}
				message.Content = "<｜DSML｜function_calls><｜DSML｜invoke name=\"skill_http_request\">..."
			case 3:
				encoded, _ := json.Marshal(event.Messages)
				if strings.Contains(string(encoded), detail) || !strings.Contains(string(encoded), "数据行405–419") {
					t.Fatalf("retry should contain summary only: %s", encoded)
				}
				if len(event.Tools) != 0 {
					t.Fatal("summary-only retry retained tools")
				}
				message.Content = "共 419 行，末段数据行是 405–419。"
			default:
				t.Fatalf("unexpected model request %d", modelCalls)
			}
			reply = Response{RequestID: event.RequestID, Message: &message}
		case "tool.request":
			result := `{"Summary":"数据行405–419，最后序号419","detail":"` + strings.Repeat(detail, 100) + `"}`
			reply = Response{RequestID: event.RequestID, Result: &result}
		case "run.completed":
			completed = true
			if !strings.Contains(event.Final, "419") {
				t.Fatalf("unexpected corrected answer %q", event.Final)
			}
		case "run.failed":
			t.Fatalf("run failed: %s", event.Error)
		}
		if reply.RequestID != "" {
			callback := post(t, server.URL+"/v1/runs/final-protocol-retry/responses", reply, testToken, runToken)
			callback.Body.Close()
			if callback.StatusCode != http.StatusNoContent {
				t.Fatalf("callback status %d", callback.StatusCode)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !completed || modelCalls != 3 {
		t.Fatalf("completed=%v model calls=%d", completed, modelCalls)
	}
}

func TestFinalProtocolAfterStreamingStartsFailsWithoutRetry(t *testing.T) {
	server := httptest.NewServer(New(testToken))
	defer server.Close()
	req := request("final-protocol-after-delta")
	req.MaxIterations = 1
	response := post(t, server.URL+"/v1/runs", req, testToken, "")
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	modelCalls, failed, completed, leakedDelta := 0, false, false, false
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
			if modelCalls > 1 {
				t.Fatal("must not retry after answer text has streamed")
			}
			requestID := event.RequestID
			for _, delta := range []string{strings.Repeat("答", 140), "<｜DSML｜function_calls><｜DSML｜invoke name=\"skill_http_request\">"} {
				callback := post(t, server.URL+"/v1/runs/final-protocol-after-delta/responses", Response{RequestID: requestID, Delta: delta}, testToken, runToken)
				callback.Body.Close()
				if callback.StatusCode != http.StatusNoContent {
					t.Fatalf("delta callback status %d", callback.StatusCode)
				}
			}
			content := strings.Repeat("答", 140) + "<｜DSML｜function_calls><｜DSML｜invoke name=\"skill_http_request\">"
			message := Message{Role: "assistant", Content: content}
			callback := post(t, server.URL+"/v1/runs/final-protocol-after-delta/responses", Response{RequestID: requestID, Message: &message}, testToken, runToken)
			callback.Body.Close()
			if callback.StatusCode != http.StatusNoContent {
				t.Fatalf("final callback status %d", callback.StatusCode)
			}
		case "message.delta":
			leakedDelta = true
			if strings.Contains(event.Delta, "DSML") || strings.Contains(event.Delta, "invoke") {
				t.Fatal("protocol text was streamed")
			}
		case "run.completed":
			completed = true
		case "run.failed":
			failed = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if modelCalls != 1 || !failed || completed || !leakedDelta {
		t.Fatalf("calls=%d failed=%v completed=%v streamed=%v", modelCalls, failed, completed, leakedDelta)
	}
}

func TestStreamFinalNormalizationPreservesAlreadyEmittedBody(t *testing.T) {
	r := &activeRun{}
	r.finalDeltas.WriteString("正文引用 </think>\n# 示例标题")
	if got := r.normalizeFinal(" 正文引用 </think>\n# 示例标题 \n"); got != "正文引用 </think>\n# 示例标题" {
		t.Fatalf("stream final erased visible body: %q", got)
	}
	legacy := &activeRun{}
	if got := legacy.normalizeFinal("内部旧草稿</think>\n# 答案"); got != "# 答案" {
		t.Fatalf("legacy cleanup changed: %q", got)
	}
}

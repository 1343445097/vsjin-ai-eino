package runtimewire

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

// Real wire HTTP callbacks exercise decisions, tool observations and final SSE;
// no provider or paid requests are used. Extra synthesis breaks the call bounds.
func TestRunReusesVisibleAnswerThroughHTTP(t *testing.T) {
	for _, scenario := range []string{"direct", "after-tool", "image", "literal-think", "empty", "unclosed-think", "protocol"} {
		t.Run(scenario, func(t *testing.T) {
			server := httptest.NewServer(New(testToken))
			defer server.Close()
			req := request("reuse-" + scenario)
			req.MaxIterations = 4
			if scenario == "image" {
				req.Messages[1] = Message{Role: "user", ContentParts: []ContentPart{{Type: "text", Text: "解释图中标高并保留来源"}, {Type: "image_url", ImageURL: &ImageURLPart{URL: tinyPNGDataURL, Detail: "high"}}}}
			}
			response := post(t, server.URL+"/v1/runs", req, testToken, "")
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("start status=%d", response.StatusCode)
			}
			answer := "标高为45.000m，须结合适用条件核验。【图片｜标高图｜asset-public】〔公开施工规范 第三条〕"
			if scenario == "literal-think" {
				answer = "正文引用 </think>\n# 标题"
			}
			scanner := bufio.NewScanner(response.Body)
			calls, toolCalls, forcedCalls := 0, 0, 0
			completed := false
			var deltas strings.Builder
			for scanner.Scan() {
				if !strings.HasPrefix(scanner.Text(), "data: ") {
					continue
				}
				var event Event
				if err := json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &event); err != nil {
					t.Fatal(err)
				}
				var callback Response
				switch event.Type {
				case "model.request":
					calls++
					if calls > 3 {
						t.Fatal("unexpected repeated synthesis")
					}
					if scenario == "image" {
						images := 0
						for _, message := range event.Messages {
							for _, part := range message.ContentParts {
								if part.Type == "image_url" && part.ImageURL != nil && part.ImageURL.URL == tinyPNGDataURL && part.ImageURL.Detail == "high" {
									images++
								}
							}
						}
						if images != 1 {
							t.Fatal("authorized image lost or duplicated")
						}
					}
					message := Message{Role: "assistant", Content: "<think>PRIVATE_REASONING</think>" + answer}
					if len(event.Tools) == 0 {
						forcedCalls++
					} else if scenario == "after-tool" && calls == 1 {
						// Visible-looking text with a tool call is not a final answer.
						message.Content = "MIXED_CONTENT_MUST_NOT_LEAK"
						message.ToolCalls = []schema.ToolCall{{ID: "evidence", Type: "function", Function: schema.FunctionCall{Name: "skills_read", Arguments: `{"skill":"drawing-analysis"}`}}}
					} else if calls == 1 {
						switch scenario {
						case "empty":
							message.Content = "  "
						case "unclosed-think":
							message.Content = "<think>PRIVATE_REASONING"
						case "protocol":
							message.Content = `<｜DSML｜function_calls><｜DSML｜invoke name="skills_read">UNSAFE_PROTOCOL</｜DSML｜invoke></｜DSML｜function_calls>`
						}
					}
					if scenario == "after-tool" && calls == 2 {
						found := false
						for _, message := range event.Messages {
							if message.Role == "tool" && message.ToolCallID == "evidence" && strings.Contains(message.Content, "PUBLIC_EVIDENCE") {
								found = true
							}
						}
						if !found {
							t.Fatal("tool evidence did not reach answer generation")
						}
					}
					callback = Response{RequestID: event.RequestID, Message: &message}
				case "tool.request":
					toolCalls++
					if scenario != "after-tool" || event.Call == nil || event.Call.ID != "evidence" {
						t.Fatal("unexpected tool execution")
					}
					result := "PUBLIC_EVIDENCE：图纸标高45.000m，保留来源及适用条件。"
					callback = Response{RequestID: event.RequestID, Result: &result}
				case "message.delta":
					deltas.WriteString(event.Delta)
				case "run.completed":
					completed = true
					if event.Final != answer || deltas.String() != answer {
						t.Fatalf("visible answer/citations changed or unsafe content leaked: final=%q deltas=%q", event.Final, deltas.String())
					}
				case "run.failed":
					t.Fatalf("unexpected run failure: %s", event.Error)
				}
				if callback.RequestID != "" {
					r := post(t, server.URL+"/v1/runs/"+req.RunID+"/responses", callback, testToken, runToken)
					_ = r.Body.Close()
					if r.StatusCode != http.StatusNoContent {
						t.Fatalf("callback status=%d", r.StatusCode)
					}
				}
			}
			if err := scanner.Err(); err != nil {
				t.Fatal(err)
			}
			wantCalls, wantTools, wantForced := 1, 0, 0
			switch scenario {
			case "after-tool":
				wantCalls, wantTools = 2, 1
			case "empty", "unclosed-think", "protocol":
				wantCalls, wantForced = 2, 1
			}
			if !completed || calls != wantCalls || toolCalls != wantTools || forcedCalls != wantForced {
				t.Fatalf("completed=%v model=%d/%d tools=%d/%d forced=%d/%d", completed, calls, wantCalls, toolCalls, wantTools, forcedCalls, wantForced)
			}
		})
	}
}

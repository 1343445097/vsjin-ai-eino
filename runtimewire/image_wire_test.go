package runtimewire

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const tinyPNGDataURL = "data:image/png;base64,aGVsbG8="

func rawRunBody(runID, content string) string {
	return rawRunBodyWithMessages(runID, fmt.Sprintf(`[{"role":"user","content":%s}]`, content))
}

func rawRunBodyWithMessages(runID, messages string) string {
	return fmt.Sprintf(`{"version":"1","runId":%q,"runToken":%q,"messages":%s,"tools":[{"type":"function","function":{"name":"skills_read","description":"read authorized skill","parameters":{"type":"object","properties":{}}}}],"maxIterations":2,"timeoutSeconds":5}`, runID, runToken, messages)
}

func rawPost(t *testing.T, url, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestContentPartsRoundTripAndImageOnlyOriginalQuestionSurvivesToolRound(t *testing.T) {
	server := httptest.NewServer(New(testToken))
	defer server.Close()

	body := rawRunBodyWithMessages("image-round", `[{"role":"system","content":"authorized system instruction"},{"role":"system","content":"authorized skill catalog"},{"role":"user","content":[{"type":"text","text":"请识别"},{"type":"image_url","image_url":{"url":"`+tinyPNGDataURL+`","detail":"high"}},{"type":"text","text":"图中标高"}]}]`)
	response := rawPost(t, server.URL+"/v1/runs", body)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("image request rejected: status=%d", response.StatusCode)
	}

	scanner := bufio.NewScanner(response.Body)
	modelCalls, toolCalls := 0, 0
	startedWithImages, startedWithDeltas, completed := false, false, false
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
				if feature == "message.images" {
					startedWithImages = true
				}
				if feature == "model.delta" {
					startedWithDeltas = true
				}
			}
		case "model.request":
			modelCalls++
			if modelCalls == 1 {
				assertImageParts(t, event.Messages, "aGVsbG8=")
				invalid, _ := json.Marshal(map[string]any{"requestId": event.RequestID, "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "image_url", "image_url": map[string]string{"url": tinyPNGDataURL}}}}})
				if status := postCallbackStatus(t, server.URL, event.RunID, string(invalid)); status != http.StatusConflict {
					t.Fatalf("assistant image callback status=%d", status)
				}
				call := map[string]any{"id": "image-tool", "type": "function", "function": map[string]string{"name": "skills_read", "arguments": "{}"}}
				callback, _ := json.Marshal(map[string]any{"requestId": event.RequestID, "message": map[string]any{"role": "assistant", "content": "读取资料", "tool_calls": []any{call}}})
				postCallback(t, server.URL, event.RunID, string(callback))
			} else {
				imageCount := 0
				seenNonSystem := false
				for _, message := range event.Messages {
					if message.Role == "system" {
						if seenNonSystem {
							t.Fatalf("final request contains a non-leading system message: %#v", event.Messages)
						}
					} else {
						seenNonSystem = true
					}
					for _, part := range message.ContentParts {
						if part.Type == "image_url" {
							imageCount++
						}
					}
				}
				if imageCount != 1 {
					t.Fatalf("final request must retain exactly one authorized image, got %d", imageCount)
				}
				if len(event.Messages) < 3 || event.Messages[0].Role != "system" || !strings.Contains(event.Messages[0].Content, "authorized system instruction") || !strings.Contains(event.Messages[0].Content, "authorized skill catalog") || !strings.Contains(event.Messages[0].Content, "直接回答本轮用户原问题") {
					t.Fatalf("final request did not coalesce system instructions at the front: %#v", event.Messages)
				}
				last := event.Messages[len(event.Messages)-1]
				if last.Role != "user" || len(last.ContentParts) != 2 || last.ContentParts[0].Type != "text" || last.ContentParts[0].Text != "请识别" || last.ContentParts[1].Type != "text" || last.ContentParts[1].Text != "图中标高" {
					t.Fatalf("final request did not repeat every question text part without its image: %#v", last)
				}
				assertImageParts(t, event.Messages, "aGVsbG8=")
				foundTool := false
				for _, msg := range event.Messages {
					if msg.Role == "tool" && msg.Content == "图像资料已分析" {
						foundTool = true
					}
				}
				if !foundTool {
					t.Fatal("tool result was lost before final answer")
				}
				callback, _ := json.Marshal(map[string]any{"requestId": event.RequestID, "message": map[string]string{"role": "assistant", "content": "图中标高为 45.000m。"}})
				postCallback(t, server.URL, event.RunID, string(callback))
			}
		case "tool.request":
			toolCalls++
			callback, _ := json.Marshal(map[string]string{"requestId": event.RequestID, "result": "图像资料已分析"})
			postCallback(t, server.URL, event.RunID, string(callback))
		case "run.completed":
			completed = event.Final == "图中标高为 45.000m。"
		case "run.failed":
			t.Fatalf("run failed: %s", event.Error)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !startedWithImages || !startedWithDeltas || !completed || modelCalls != 2 || toolCalls != 1 {
		t.Fatalf("startedWithImages=%v startedWithDeltas=%v completed=%v models=%d tools=%d", startedWithImages, startedWithDeltas, completed, modelCalls, toolCalls)
	}
}

func TestImageOnlyQuestionSurvivesFinalSelection(t *testing.T) {
	server := httptest.NewServer(New(testToken))
	defer server.Close()
	response := rawPost(t, server.URL+"/v1/runs", rawRunBody("image-only", `[{"type":"image_url","image_url":{"url":"`+tinyPNGDataURL+`"}}]`))
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("image-only request rejected: status=%d", response.StatusCode)
	}
	scanner := bufio.NewScanner(response.Body)
	modelCalls, completed := 0, false
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
			last := event.Messages[len(event.Messages)-1]
			encoded, err := json.Marshal(last)
			if err != nil || last.Role != "user" {
				t.Fatalf("image-only question did not trigger generation: %s", encoded)
			}
			if modelCalls == 1 {
				var content []struct {
					Type string `json:"type"`
				}
				if err := json.Unmarshal(mustRawField(t, encoded, "content"), &content); err != nil || len(content) != 1 || content[0].Type != "image_url" {
					t.Fatalf("initial generation lost image-only question: %s (%v)", encoded, err)
				}
			} else {
				imageCount := 0
				for _, message := range event.Messages {
					for _, part := range message.ContentParts {
						if part.Type == "image_url" {
							imageCount++
						}
					}
				}
				if imageCount != 1 || last.Content != "请根据本轮原始图片回答用户的问题。" || len(last.ContentParts) != 0 {
					t.Fatalf("final generation duplicated or dropped image-only question: images=%d last=%#v", imageCount, last)
				}
			}
			answer := "图片中没有可识别的标高文字。"
			callback, _ := json.Marshal(map[string]any{"requestId": event.RequestID, "message": map[string]string{"role": "assistant", "content": answer}})
			postCallback(t, server.URL, event.RunID, string(callback))
		case "run.completed":
			completed = true
			if event.Final != "图片中没有可识别的标高文字。" {
				t.Fatal("image-only visible answer was not preserved")
			}
		case "run.failed":
			t.Fatalf("run failed: %s", event.Error)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !completed || modelCalls != 1 {
		t.Fatalf("completed=%v model calls=%d", completed, modelCalls)
	}
}

func TestFinalAnswerDoesNotInventImagePromptForEmptyTextOnlyTurn(t *testing.T) {
	messages := finalAnswerMessages([]Message{{Role: "user"}})
	last := messages[len(messages)-1]
	if last.Role != "user" || last.Content != "" || len(last.ContentParts) != 0 {
		t.Fatalf("empty text-only turn changed during final selection: %#v", last)
	}
}

func TestTextOnlyContentRemainsStringOnWire(t *testing.T) {
	encoded, err := json.Marshal(Message{Role: "user", Content: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"role":"user","content":"hello"}` {
		t.Fatalf("text-only wire changed: %s", encoded)
	}
	var decoded Message
	if err := json.Unmarshal(encoded, &decoded); err != nil || decoded.Content != "hello" {
		t.Fatalf("text-only decode changed: %#v, %v", decoded, err)
	}
	empty, err := json.Marshal(Message{Role: "user"})
	if err != nil || string(empty) != `{"role":"user"}` {
		t.Fatalf("empty text-only wire changed: %s (%v)", empty, err)
	}
}

func mustRawField(t *testing.T, encoded []byte, field string) json.RawMessage {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatal(err)
	}
	return object[field]
}

func TestRejectsUnsafeOrOversizedImageContent(t *testing.T) {
	cases := []struct {
		name, content string
	}{
		{"external URL", `[{"type":"image_url","image_url":{"url":"https://example.test/image.png"}}]`},
		{"malformed base64", `[{"type":"image_url","image_url":{"url":"data:image/png;base64,%%%"}}]`},
		{"unsupported image type", `[{"type":"image_url","image_url":{"url":"data:image/gif;base64,aGVsbG8="}}]`},
		{"unknown part", `[{"type":"audio_url","audio_url":{"url":"data:audio/wav;base64,aGVsbG8="}}]`},
		{"five images", `[` + strings.TrimSuffix(strings.Repeat(`{"type":"image_url","image_url":{"url":"`+tinyPNGDataURL+`"}},`, 5), ",") + `]`},
		{"image in assistant message", `[{"type":"image_url","image_url":{"url":"` + tinyPNGDataURL + `"}}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := tc.content
			body := rawRunBody("reject-image", content)
			if tc.name == "image in assistant message" {
				body = strings.Replace(body, `"role":"user"`, `"role":"assistant"`, 1)
			}
			assertRunRejected(t, body)
		})
	}
}

func TestRejectsImageByteBudgets(t *testing.T) {
	t.Run("decoded image over 8 MiB", func(t *testing.T) {
		data := strings.Repeat("A", ((8<<20)+2)/3*4)
		assertRunRejected(t, rawRunBody("reject-image-size", `[{"type":"image_url","image_url":{"url":"data:image/png;base64,`+data+`"}}]`))
	})
	t.Run("aggregate encoded images over 16 MiB", func(t *testing.T) {
		data := strings.Repeat("A", 6<<20)
		parts := make([]string, 3)
		for i := range parts {
			parts[i] = `{"type":"image_url","image_url":{"url":"data:image/png;base64,` + data + `"}}`
		}
		assertRunRejected(t, rawRunBody("reject-aggregate", `[`+strings.Join(parts, ",")+`]`))
	})
	t.Run("text remains capped at 4 MiB", func(t *testing.T) {
		assertRunRejected(t, rawRunBody("reject-text-size", `"`+strings.Repeat("x", (4<<20)+1)+`"`))
	})
	t.Run("wire body is capped at 24 MiB", func(t *testing.T) {
		assertRunRejected(t, rawRunBody("reject-wire-size", `"`+strings.Repeat("x", 24<<20)+`"`))
	})
}

func assertRunRejected(t *testing.T, body string) {
	t.Helper()
	server := httptest.NewServer(New(testToken))
	defer server.Close()
	resp := rawPost(t, server.URL+"/v1/runs", body)
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid request status=%d", resp.StatusCode)
	}
	for _, secret := range []string{tinyPNGDataURL, "aGVsbG8=", "%%%"} {
		if strings.Contains(string(data), secret) {
			t.Fatal("error response exposed image data")
		}
	}
}

func assertImageParts(t *testing.T, messages []Message, encoded string) {
	t.Helper()
	for _, message := range messages {
		if message.Role != "user" {
			continue
		}
		encodedMessage, err := json.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		var wire struct {
			Content []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				ImageURL struct {
					URL    string `json:"url"`
					Detail string `json:"detail"`
				} `json:"image_url"`
			} `json:"content"`
		}
		if err := json.Unmarshal(encodedMessage, &wire); err != nil || len(wire.Content) != 3 || wire.Content[0].Type != "text" || wire.Content[0].Text != "请识别" || wire.Content[1].Type != "image_url" || !strings.HasSuffix(wire.Content[1].ImageURL.URL, ","+encoded) || wire.Content[2].Type != "text" || wire.Content[2].Text != "图中标高" {
			t.Fatalf("user image parts changed: %s (%v)", encodedMessage, err)
		}
		if wire.Content[1].ImageURL.Detail != "high" {
			t.Fatalf("image detail changed: %s", encodedMessage)
		}
		return
	}
	t.Fatal("model request omitted user message")
}

func postCallback(t *testing.T, serverURL, runID, body string) {
	t.Helper()
	if status := postCallbackStatus(t, serverURL, runID, body); status != http.StatusNoContent {
		t.Fatalf("callback status=%d", status)
	}
}

func postCallbackStatus(t *testing.T, serverURL, runID, body string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, serverURL+"/v1/runs/"+runID+"/responses", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("X-VSJin-Run-Token", runToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

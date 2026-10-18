package runtimewire

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestFinalAnswerMessagesPreservesTextPrefix(t *testing.T) {
	for _, tc := range []struct {
		name     string
		messages []Message
		question *Message
	}{
		{
			name: "multi-tool history with intervening system",
			messages: []Message{
				{Role: "system", Content: "authorized initial instruction"},
				{Role: "system", Content: "authorized skill catalog"},
				{Role: "user", Content: "USER_FACT: inspect the project", Name: "requester"},
				{Role: "assistant", ToolCalls: []schema.ToolCall{{ID: "read", Type: "function", Function: schema.FunctionCall{Name: "skills_read", Arguments: `{}`}}}, ReasoningContent: "read authorized skill"},
				{Role: "tool", ToolCallID: "read", Content: "TOOL_FACT: skill evidence"},
				{Role: "system", Content: "authorized mid-history context"},
				{Role: "assistant", ToolCalls: []schema.ToolCall{{ID: "lookup", Type: "function", Function: schema.FunctionCall{Name: "skills_read", Arguments: `{}`}}}},
				{Role: "tool", ToolCallID: "lookup", Content: "TOOL_FACT: project evidence"},
			},
			question: &Message{Role: "user", Content: "USER_FACT: inspect the project", Name: "requester"},
		},
		{
			name: "text parts and metadata remain unchanged",
			messages: []Message{
				{Role: "system", ContentParts: []ContentPart{{Type: "text", Text: "initial instruction"}}},
				{Role: "user", ContentParts: []ContentPart{{Type: "text", Text: "USER_FACT: first"}, {Type: "text", Text: " second"}}, Name: "requester"},
			},
			question: &Message{Role: "user", ContentParts: []ContentPart{{Type: "text", Text: "USER_FACT: first"}, {Type: "text", Text: " second"}}, Name: "requester"},
		},
		{
			name: "repeat latest question only",
			messages: []Message{
				{Role: "user", Content: "earlier question"},
				{Role: "assistant", Content: "earlier answer"},
				{Role: "user", Content: "USER_FACT: current question"},
			},
			question: &Message{Role: "user", Content: "USER_FACT: current question"},
		},
		{name: "empty input"},
		{name: "no user", messages: []Message{{Role: "system", Content: "initial instruction"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, err := json.Marshal(tc.messages)
			if err != nil {
				t.Fatal(err)
			}
			got := finalAnswerMessages(tc.messages)
			wantLen := len(tc.messages) + 1
			if tc.question != nil {
				wantLen++
			}
			if len(got) != wantLen {
				t.Fatalf("final message count = %d, want %d", len(got), wantLen)
			}
			for i := range tc.messages {
				if !reflect.DeepEqual(got[i], tc.messages[i]) {
					t.Fatalf("original prefix changed at message %d: got %#v, want %#v", i, got[i], tc.messages[i])
				}
			}
			directive := got[len(tc.messages)]
			if directive.Role != "system" || !strings.Contains(directive.Content, "本轮工具已关闭") {
				t.Fatalf("final directive must retain system authority: %#v", directive)
			}
			if strings.Contains(directive.Content, "USER_FACT") || strings.Contains(directive.Content, "TOOL_FACT") {
				t.Fatalf("user or tool facts promoted into final directive: %#v", directive)
			}
			if tc.question != nil && !reflect.DeepEqual(got[len(got)-1], *tc.question) {
				t.Fatalf("repeated question changed: got %#v, want %#v", got[len(got)-1], *tc.question)
			}
			after, err := json.Marshal(tc.messages)
			if err != nil || string(after) != string(before) {
				t.Fatalf("input mutated: before=%s after=%s err=%v", before, after, err)
			}
		})
	}
}

func TestFinalAnswerMessagesKeepsEarlierImageCompatibility(t *testing.T) {
	messages := []Message{
		{Role: "system", Content: "initial instruction"},
		{Role: "user", ContentParts: []ContentPart{{Type: "image_url", ImageURL: &ImageURLPart{URL: tinyPNGDataURL, Detail: "high"}}}},
		{Role: "assistant", Content: "earlier image answer"},
		{Role: "system", Content: "mid-history context"},
		{Role: "user", Content: "compare with the earlier image"},
	}
	got := finalAnswerMessages(messages)
	if len(got) != 5 || got[0].Role != "system" || !strings.HasPrefix(got[0].Content, "initial instruction\n\nmid-history context\n\n") || !strings.Contains(got[0].Content, "本轮工具已关闭") {
		t.Fatalf("image history lost the coalesced opening system: %#v", got)
	}
	for _, message := range got[1:] {
		if message.Role == "system" {
			t.Fatal("image history gained a non-leading system message")
		}
	}
	if !reflect.DeepEqual(got[1], messages[1]) {
		t.Fatal("earlier image or its detail changed")
	}
	if !reflect.DeepEqual(got[len(got)-1], messages[4]) {
		t.Fatal("latest text question changed in image history")
	}
}

func TestTextFinalRequestPreservesPrefixAcrossMultipleToolRounds(t *testing.T) {
	for _, mode := range []string{"budget", "draft"} {
		t.Run(mode, func(t *testing.T) {
			srv := httptest.NewServer(New(testToken))
			defer srv.Close()
			req := request("cache-prefix-" + mode)
			req.MaxIterations = 4
			if mode == "draft" {
				req.MaxIterations = 6
			}
			req.Messages = append(req.Messages, Message{Role: "system", Content: "authorized mid-history context"})
			res := post(t, srv.URL+"/v1/runs", req, testToken, "")
			defer res.Body.Close()
			scanner := bufio.NewScanner(res.Body)
			var previous []Message
			toolRequests, modelRequests := 0, 0
			finalSeen, completed := false, false
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
					modelRequests++
					message := Message{Role: "assistant", Content: "最终答复"}
					if len(event.Tools) == 0 {
						finalSeen = true
						if len(event.Messages) < len(previous)+2 || !reflect.DeepEqual(event.Messages[:len(previous)], previous) {
							t.Fatal("final request rewrote the preceding tool-round prefix")
						}
						last := event.Messages[len(event.Messages)-1]
						if !reflect.DeepEqual(last, req.Messages[1]) || event.Messages[len(event.Messages)-2].Role != "system" {
							t.Fatal("final request lost the original question or trusted trailing directive")
						}
						observations := 0
						for _, m := range event.Messages {
							if m.Role == "tool" {
								observations++
							}
							if m.Role == "assistant" && m.Content == "unpublished draft" {
								t.Fatal("unpublished draft inserted into final history")
							}
						}
						if observations != 3 {
							t.Fatalf("final request has %d tool observations, want 3", observations)
						}
					} else {
						if finalSeen {
							t.Fatal("tools re-enabled after final request")
						}
						previous = event.Messages
						if modelRequests <= 3 {
							message.Content = ""
							message.ToolCalls = []schema.ToolCall{{ID: fmt.Sprintf("read-%d", modelRequests), Type: "function", Function: schema.FunctionCall{Name: "skills_read", Arguments: `{"skill":"drawing-analysis"}`}}}
						} else {
							message.Content = "unpublished draft"
						}
					}
					reply = Response{RequestID: event.RequestID, Message: &message}
				case "tool.request":
					toolRequests++
					result := fmt.Sprintf("TOOL_FACT: observation %d", toolRequests)
					reply = Response{RequestID: event.RequestID, Result: &result}
				case "run.completed":
					completed = true
				case "run.failed":
					t.Fatal(event.Error)
				}
				if reply.RequestID != "" {
					callback := post(t, srv.URL+"/v1/runs/"+req.RunID+"/responses", reply, testToken, runToken)
					callback.Body.Close()
					if callback.StatusCode != 204 {
						t.Fatalf("callback status = %d", callback.StatusCode)
					}
				}
			}
			if err := scanner.Err(); err != nil {
				t.Fatal(err)
			}
			wantModels := 4
			if mode == "draft" {
				wantModels = 5
			}
			if !completed || !finalSeen || toolRequests != 3 || modelRequests != wantModels {
				t.Fatalf("completed=%v final=%v tools=%d models=%d, want models=%d", completed, finalSeen, toolRequests, modelRequests, wantModels)
			}
		})
	}
}

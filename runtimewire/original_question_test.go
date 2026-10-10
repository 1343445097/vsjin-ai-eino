package runtimewire

import (
	"bufio"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFinalGenerationPreservesOriginalRequest(t *testing.T) {
	for _, tc := range []struct {
		name, question string
		tools          bool
		rounds         int
	}{
		{"greeting", "你好", true, 4},
		{"capabilities", "你能做什么？", true, 4},
		{"general", "施工缝和后浇带有什么区别？", true, 4},
		{"without_tools", "谢谢", false, 4},
		{"last_round", "你好", true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(New(testToken))
			defer srv.Close()
			req := request(tc.name)
			req.Messages[1].Content = tc.question
			req.MaxIterations = tc.rounds
			if !tc.tools {
				req.Tools = nil
			}
			res := post(t, srv.URL+"/v1/runs", req, testToken, "")
			defer res.Body.Close()
			scanner := bufio.NewScanner(res.Body)
			calls := 0
			completed := false
			for scanner.Scan() {
				if !strings.HasPrefix(scanner.Text(), "data: ") {
					continue
				}
				var e Event
				if err := json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &e); err != nil {
					t.Fatal(err)
				}
				switch e.Type {
				case "model.request":
					calls++
					if len(e.Tools) == 0 {
						last := e.Messages[len(e.Messages)-1]
						if last.Role != "user" || last.Content != tc.question {
							t.Errorf("final generation replaced user intent: got %q want %q", last.Content, tc.question)
						}
						for _, m := range e.Messages {
							if m.Role == "assistant" && m.Content == "你好！有什么可以帮你？" {
								t.Error("already completed answer was inserted as prior conversation")
							}
						}
					}
					msg := Message{Role: "assistant", Content: "你好！有什么可以帮你？"}
					reply := post(t, srv.URL+"/v1/runs/"+tc.name+"/responses", Response{RequestID: e.RequestID, Message: &msg}, testToken, runToken)
					reply.Body.Close()
				case "run.completed":
					completed = true
				case "run.failed":
					t.Fatal(e.Error)
				}
			}
			if err := scanner.Err(); err != nil {
				t.Fatal(err)
			}
			if !completed {
				t.Fatal("no final response")
			}
			if calls > 2 {
				t.Fatalf("unexpected extra generation: %d", calls)
			}
		})
	}
}

package server

import (
	"bytes"
	"context"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/hertz-contrib/sse"
	"github.com/vsjin/vsjin-ai/a2ui"
	"strings"
	"testing"
)

func TestBusinessContextDoesNotGrantHostTools(t *testing.T) {
	s, _, cleanup := newTestServer(t, simpleReplyAgent("ok"))
	defer cleanup()
	messages := s.buildRunMessages("test", []*schema.Message{schema.UserMessage("查询图纸")})
	if len(messages) != 2 {
		t.Fatalf("unexpected context count %d", len(messages))
	}
	prompt := messages[0].Content
	if !strings.Contains(prompt, "VSJin AI") {
		t.Fatal("missing product identity")
	}
	for _, forbidden := range []string{"Project root:", "grep(", "execute tool", "Go example", "eino-examples"} {
		if strings.Contains(prompt, forbidden) {
			t.Errorf("teaching/host instructions retained: %s", forbidden)
		}
	}
}

func TestHTTPAuthenticationAndForeignOrigins(t *testing.T) {
	s, _, cleanup := newTestServer(t, simpleReplyAgent("ok"))
	defer cleanup()
	s.cfg.APIToken = "test-access"
	h := s.Handler()
	if r := ut.PerformRequest(h.Engine, "GET", "/healthz", nil); r.Code != 200 {
		t.Fatalf("health %d", r.Code)
	}
	if r := ut.PerformRequest(h.Engine, "GET", "/sessions", nil); r.Code != 401 {
		t.Fatalf("unauthenticated list %d", r.Code)
	}
	headers := []ut.Header{{Key: "Authorization", Value: "Bearer test-access"}}
	if r := ut.PerformRequest(h.Engine, "GET", "/sessions", nil, headers...); r.Code != 200 {
		t.Fatalf("authorized list %d", r.Code)
	}
	if r := ut.PerformRequest(h.Engine, "POST", "/sessions", nil, ut.Header{Key: "Origin", Value: "https://foreign.invalid"}, ut.Header{Key: "Host", Value: "localhost:18900"}); r.Code != 403 {
		t.Fatalf("foreign origin %d", r.Code)
	}
	if r := ut.PerformRequest(h.Engine, "GET", "/v1/runtime", nil, headers...); r.Code != 200 || !strings.Contains(r.Body.String(), "VSJin AI") {
		t.Fatalf("runtime metadata %d", r.Code)
	}
}

func TestEmptyFinalResponseFailsTurn(t *testing.T) {
	s, _, cleanup := newTestServer(t, simpleReplyAgent(""))
	defer cleanup()
	sess, err := s.cfg.Store.GetOrCreate("empty-final")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loop := s.newLoop(sess, sess.ID, false)
	ts := s.getTurnState(sess.ID)
	ts.iterReady = make(chan iterEnvelope[*schema.Message], 1)
	ts.iterDone = make(chan iterResult[*schema.Message], 1)
	loop.Push(&ChatItem{Query: "hello"})
	loop.Run(ctx)
	envelope := <-ts.iterReady
	envelope.done <- iterResult[*schema.Message]{lastContent: ""}
	if state := loop.Wait(); state.ExitReason == nil {
		t.Fatal("empty response must fail the turn")
	}
}

type recordingPublisher struct{ data []byte }

func (r *recordingPublisher) Publish(event *sse.Event) error {
	r.data = append(r.data, event.Data...)
	return nil
}

func TestAgentCancellationHasCancelledTerminal(t *testing.T) {
	s, _, cleanup := newTestServer(t, simpleReplyAgent(""))
	defer cleanup()
	persisted := make(chan error, 1)
	persisted <- &adk.CancelError{Info: &adk.AgentCancelInfo{}}
	recorder := &recordingPublisher{}
	s.publishTerminal(&safeSSEStream{stream: recorder}, iterEnvelope[*schema.Message]{persisted: persisted}, nil, "")
	if !strings.Contains(string(recorder.data), "run.cancelled") {
		t.Fatalf("agent cancellation misclassified: %s", recorder.data)
	}
}

func TestSSETerminalRequiresFinalReplyAndPersistence(t *testing.T) {
	for _, tc := range []struct{ reply, terminal string }{{"正常最终答复", "run.completed"}, {"", "run.failed"}} {
		t.Run(tc.terminal, func(t *testing.T) {
			s, _, cleanup := newTestServer(t, simpleReplyAgent(tc.reply))
			defer cleanup()
			sess, err := s.cfg.Store.GetOrCreate("terminal-test")
			if err != nil {
				t.Fatal(err)
			}
			loop := s.newLoop(sess, sess.ID, false)
			ts := s.getTurnState(sess.ID)
			ts.iterReady = make(chan iterEnvelope[*schema.Message], 1)
			ts.iterDone = make(chan iterResult[*schema.Message], 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			loop.Push(&ChatItem{Query: "测试最终答复"})
			loop.Run(ctx)
			envelope := <-ts.iterReady
			var buf bytes.Buffer
			content, intermediates, interrupt, msgIdx, streamErr := a2ui.StreamToWriter(&buf, sess.ID, envelope.history, envelope.events)
			envelope.done <- iterResult[*schema.Message]{lastContent: content, intermediates: intermediates, interruptID: interrupt, msgIdx: msgIdx, err: streamErr}
			recorder := &recordingPublisher{}
			s.publishTerminal(&safeSSEStream{stream: recorder}, envelope, streamErr, interrupt)
			if !strings.Contains(string(recorder.data), tc.terminal) {
				t.Fatalf("missing explicit terminal: %s", recorder.data)
			}
			if tc.reply != "" {
				messages := sess.GetMessages()
				if len(messages) != 2 || messages[1].Content != tc.reply {
					t.Fatal("completed before persisting final response")
				}
			}
			loop.Stop()
			loop.Wait()
		})
	}
}

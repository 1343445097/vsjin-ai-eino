package main

import (
	"context"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/vsjin/vsjin-ai/internal/appconfig"
	"github.com/vsjin/vsjin-ai/policy"
	"github.com/vsjin/vsjin-ai/skills"
	"strings"
	"testing"
)

type scriptedModel struct {
	t     *testing.T
	calls int
}

func (m *scriptedModel) Generate(_ context.Context, messages []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	m.calls++
	tools := model.GetCommonOptions(nil, opts...).Tools
	if len(tools) != 2 {
		m.t.Errorf("expected only two authorized generic tools, got %d", len(tools))
	}
	for _, info := range tools {
		if info.Name != "skills_read" && info.Name != "skill_http_request" {
			m.t.Errorf("unexpected built-in tool: %s", info.Name)
		}
	}
	all := ""
	for _, msg := range messages {
		all += msg.Content + "\n"
	}
	if m.calls == 1 {
		for _, name := range []string{"VSJin", "drawing-analysis", "scai-document-rag", "site-issue-reporting"} {
			if !strings.Contains(all, name) {
				m.t.Errorf("missing first-turn catalog/identity: %s", name)
			}
		}
		return schema.AssistantMessage("先读取图纸分析技能。", []schema.ToolCall{{ID: "read-1", Type: "function", Function: schema.FunctionCall{Name: "skills_read", Arguments: `{"skill":"drawing-analysis"}`}}}), nil
	}
	if !strings.Contains(all, "skill_http_request") || messages[len(messages)-1].Role != schema.Tool {
		m.t.Errorf("skill result was not returned to model")
	}
	return schema.AssistantMessage("已读取图纸分析技能；请提供图纸或接入业务资料，才能核对适用标高。", nil), nil
}

func (m *scriptedModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	r, w := schema.Pipe[*schema.Message](1)
	w.Send(msg, nil)
	w.Close()
	return r, nil
}

func TestExpertCatalogToolAndFinalReply(t *testing.T) {
	dir, cleanup, err := policy.Materialize()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	catalog, err := skills.Load(dir, []string{"knowledge.base", "knowledge.cad", "scai.reporting"})
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Skills) != 3 {
		t.Fatalf("default authorized skills=%d", len(catalog.Skills))
	}
	cm := &scriptedModel{t: t}
	agent, err := buildAgentWithModel(context.Background(), appconfig.Config{MaxIterations: 4}, catalog, cm)
	if err != nil {
		t.Fatal(err)
	}
	events := agent.Run(context.Background(), &adk.TypedAgentInput[*schema.Message]{Messages: []*schema.Message{schema.UserMessage("核对门窗防雷适用标高")}})
	var final string
	var toolResults int
	for {
		event, ok := events.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			t.Fatal(event.Err)
		}
		if event.Output != nil && event.Output.MessageOutput != nil {
			msg := event.Output.MessageOutput.Message
			if msg != nil {
				if msg.Role == schema.Tool {
					toolResults++
				}
				if msg.Role == schema.Assistant && len(msg.ToolCalls) == 0 {
					final = msg.Content
				}
			}
		}
	}
	if cm.calls != 2 || toolResults != 1 || !strings.Contains(final, "适用标高") {
		t.Fatalf("not a real ADK read-then-answer: calls=%d toolResults=%d final=%q", cm.calls, toolResults, final)
	}
}

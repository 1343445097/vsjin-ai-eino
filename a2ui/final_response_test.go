package a2ui

import (
	"bytes"
	"errors"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"testing"
)

func TestToolPreambleIsNotFinalResponse(t *testing.T) {
	iter, gen := adk.NewAsyncIteratorPair[*adk.TypedAgentEvent[*schema.Message]]()
	go func() {
		defer gen.Close()
		gen.Send(&adk.TypedAgentEvent[*schema.Message]{Output: &adk.TypedAgentOutput[*schema.Message]{MessageOutput: &adk.TypedMessageVariant[*schema.Message]{Role: schema.Assistant, Message: schema.AssistantMessage("正在查询", []schema.ToolCall{{ID: "call-1", Function: schema.FunctionCall{Name: "skills_read", Arguments: `{"skill":"drawing-analysis"}`}}})}}})
		gen.Send(&adk.TypedAgentEvent[*schema.Message]{Action: adk.NewExitAction()})
	}()
	var buf bytes.Buffer
	content, _, _, _, err := StreamToWriter(&buf, "test", nil, iter)
	if err == nil || content != "" {
		t.Fatalf("tool-only run reported final response: content=%q err=%v", content, err)
	}
}

func TestPartialModelStreamErrorIsNotSuccess(t *testing.T) {
	iter, gen := adk.NewAsyncIteratorPair[*adk.TypedAgentEvent[*schema.Message]]()
	reader, writer := schema.Pipe[*schema.Message](2)
	go func() {
		defer gen.Close()
		gen.Send(&adk.TypedAgentEvent[*schema.Message]{Output: &adk.TypedAgentOutput[*schema.Message]{MessageOutput: &adk.TypedMessageVariant[*schema.Message]{Role: schema.Assistant, IsStreaming: true, MessageStream: reader}}})
	}()
	go func() {
		defer writer.Close()
		writer.Send(schema.AssistantMessage("部分回答", nil), nil)
		writer.Send(nil, errors.New("stream interrupted"))
	}()
	var buf bytes.Buffer
	_, _, _, _, err := StreamToWriter(&buf, "test", nil, iter)
	if err == nil {
		t.Fatal("partial stream failure silently became success")
	}
}

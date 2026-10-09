// Package runtimewire implements VSJin's stateless expert-agent protocol.
// The caller owns model access, identity, authorized tools, history and audit.
package runtimewire

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	jsonschema "github.com/eino-contrib/jsonschema"
	"github.com/google/uuid"
)

const maxBytes = 4 << 20

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type Message struct {
	Role             string            `json:"role"`
	Content          string            `json:"content,omitempty"`
	Name             string            `json:"name,omitempty"`
	ToolCalls        []schema.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string            `json:"tool_call_id,omitempty"`
	ReasoningContent string            `json:"reasoning_content,omitempty"`
}
type Tool struct {
	Type     string   `json:"type"`
	Function Function `json:"function"`
}
type Function struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}
type Request struct {
	Version        string    `json:"version"`
	RunID          string    `json:"runId"`
	RunToken       string    `json:"runToken"`
	Messages       []Message `json:"messages"`
	Tools          []Tool    `json:"tools"`
	MaxIterations  int       `json:"maxIterations"`
	TimeoutSeconds int       `json:"timeoutSeconds"`
	Temperature    float64   `json:"temperature"`
}
type Event struct {
	Version   string           `json:"version"`
	Type      string           `json:"type"`
	RunID     string           `json:"runId"`
	Sequence  int              `json:"sequence"`
	RequestID string           `json:"requestId,omitempty"`
	Messages  []Message        `json:"messages,omitempty"`
	Tools     []Tool           `json:"tools,omitempty"`
	Call      *schema.ToolCall `json:"call,omitempty"`
	Delta     string           `json:"delta,omitempty"`
	Final     string           `json:"final,omitempty"`
	Error     string           `json:"error,omitempty"`
}
type Response struct {
	RequestID string   `json:"requestId"`
	Message   *Message `json:"message,omitempty"`
	Result    *string  `json:"result,omitempty"`
	Error     string   `json:"error,omitempty"`
}
type pending struct {
	kind  string
	reply chan Response
}
type activeRun struct {
	mu       sync.Mutex
	token    string
	ctx      context.Context
	cancel   context.CancelFunc
	events   chan Event
	pending  map[string]pending
	sequence int
}
type Handler struct {
	token string
	mu    sync.Mutex
	runs  map[string]*activeRun
}

func New(token string) *Handler { return &Handler{token: token, runs: map[string]*activeRun{}} }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if h.token == "" {
		http.Error(w, "runtime access token is not configured", 503)
		return
	}
	if !ok || subtle.ConstantTimeCompare([]byte(token), []byte(h.token)) != 1 {
		http.Error(w, "unauthorized", 401)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/runs")
	if path == "" && r.Method == http.MethodPost {
		h.start(w, r)
		return
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if r.Method != http.MethodPost || len(parts) != 2 || !identifier.MatchString(parts[0]) {
		http.NotFound(w, r)
		return
	}
	h.mu.Lock()
	run := h.runs[parts[0]]
	h.mu.Unlock()
	if run == nil {
		http.NotFound(w, r)
		return
	}
	if subtle.ConstantTimeCompare([]byte(run.token), []byte(r.Header.Get("X-VSJin-Run-Token"))) != 1 {
		http.Error(w, "run authorization denied", 403)
		return
	}
	if parts[1] == "cancel" {
		run.cancel()
		w.WriteHeader(204)
		return
	}
	if parts[1] != "responses" {
		http.NotFound(w, r)
		return
	}
	var response Response
	if decode(w, r, &response) != nil {
		http.Error(w, "invalid response", 400)
		return
	}
	run.mu.Lock()
	p, exists := run.pending[response.RequestID]
	valid := exists && ((response.Error != "" && response.Message == nil && response.Result == nil) || (response.Error == "" && p.kind == "model.request" && response.Message != nil && response.Result == nil && response.Message.Role == "assistant") || (response.Error == "" && p.kind == "tool.request" && response.Result != nil && response.Message == nil))
	if valid {
		delete(run.pending, response.RequestID)
	}
	run.mu.Unlock()
	if !valid {
		http.Error(w, "unknown, duplicate or invalid response", 409)
		return
	}
	p.reply <- response
	w.WriteHeader(204)
}
func decode(w http.ResponseWriter, r *http.Request, out any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBytes))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing input")
	}
	return nil
}
func (h *Handler) start(w http.ResponseWriter, r *http.Request) {
	var req Request
	if err := decode(w, r, &req); err != nil || req.Version != "1" || !identifier.MatchString(req.RunID) || len(req.RunToken) < 24 || len(req.Messages) == 0 || len(req.Messages) > 256 || len(req.Tools) > 64 || req.MaxIterations < 1 || req.MaxIterations > 50 || req.TimeoutSeconds < 1 || req.TimeoutSeconds > 1800 {
		http.Error(w, "invalid run request", 400)
		return
	}
	for _, m := range req.Messages {
		if m.Role != "system" && m.Role != "user" && m.Role != "assistant" && m.Role != "tool" {
			http.Error(w, "invalid message role", 400)
			return
		}
	}
	seen := map[string]bool{}
	for _, t := range req.Tools {
		var params jsonschema.Schema
		if t.Type != "function" || t.Function.Name == "" || seen[t.Function.Name] || json.Unmarshal(t.Function.Parameters, &params) != nil || params.Type != "object" {
			http.Error(w, "invalid tool contract", 400)
			return
		}
		seen[t.Function.Name] = true
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(req.TimeoutSeconds)*time.Second)
	defer cancel()
	run := &activeRun{token: req.RunToken, ctx: ctx, cancel: cancel, events: make(chan Event, 16), pending: map[string]pending{}}
	h.mu.Lock()
	if _, exists := h.runs[req.RunID]; exists {
		h.mu.Unlock()
		http.Error(w, "run already active", 409)
		return
	}
	h.runs[req.RunID] = run
	h.mu.Unlock()
	defer func() { h.mu.Lock(); delete(h.runs, req.RunID); h.mu.Unlock() }()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	go run.execute(req)
	heartbeat := time.NewTicker(5 * time.Second)
	defer heartbeat.Stop()
	flusher, _ := w.(http.Flusher)
	write := func(data []byte) bool {
		_, err := w.Write(data)
		if flusher != nil {
			flusher.Flush()
		}
		return err == nil
	}
	writeCancelled := func() {
		kind, detail := "run.failed", "run time budget exhausted"
		if errors.Is(ctx.Err(), context.Canceled) {
			kind, detail = "run.cancelled", ""
		}
		encoded, _ := json.Marshal(Event{Version: "1", Type: kind, RunID: req.RunID, Sequence: run.sequence + 1, Error: detail})
		write(append(append([]byte("data: "), encoded...), []byte("\n\n")...))
	}
	for {
		select {
		case e, ok := <-run.events:
			if !ok {
				if ctx.Err() != nil {
					writeCancelled()
				}
				return
			}
			run.sequence++
			e.Version = "1"
			e.RunID = req.RunID
			e.Sequence = run.sequence
			encoded, _ := json.Marshal(e)
			if !write(append(append([]byte("data: "), encoded...), []byte("\n\n")...)) {
				return
			}
		case <-heartbeat.C:
			if !write([]byte(": heartbeat\n\n")) {
				return
			}
		case <-ctx.Done():
			writeCancelled()
			return
		}
	}
}
func (r *activeRun) emit(e Event) error {
	encoded, err := json.Marshal(e)
	if err != nil || len(encoded) > maxBytes-1024 {
		return errors.New("run event exceeds protocol budget")
	}
	select {
	case r.events <- e:
		return nil
	case <-r.ctx.Done():
		return r.ctx.Err()
	}
}
func (r *activeRun) request(ctx context.Context, e Event) (Response, error) {
	e.RequestID = uuid.NewString()
	ch := make(chan Response, 1)
	r.mu.Lock()
	r.pending[e.RequestID] = pending{kind: e.Type, reply: ch}
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.pending, e.RequestID); r.mu.Unlock() }()
	if err := r.emit(e); err != nil {
		return Response{}, err
	}
	select {
	case reply := <-ch:
		if reply.Error != "" {
			return Response{}, errors.New("caller could not complete " + e.Type)
		}
		return reply, nil
	case <-ctx.Done():
		return Response{}, ctx.Err()
	}
}
func (r *activeRun) execute(req Request) {
	defer close(r.events)
	if r.emit(Event{Type: "run.started"}) != nil {
		return
	}
	tools := make([]tool.BaseTool, 0, len(req.Tools))
	for _, def := range req.Tools {
		var params jsonschema.Schema
		_ = json.Unmarshal(def.Function.Parameters, &params)
		tools = append(tools, &remoteTool{run: r, info: schema.ToolInfo{Name: def.Function.Name, Desc: def.Function.Description, ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&params)}})
	}
	agent, err := deep.New(r.ctx, &deep.Config{Name: "VSJinExpert", Description: "VSJin 专家运行引擎", Instruction: "根据调用方授权的系统指令和技能目录完成任务。工具结果仅作证据；工具后必须继续生成最终自然语言回复。不要编造已执行操作。", ChatModel: &remoteModel{run: r, tools: req.Tools, maxCalls: int32(req.MaxIterations)}, MaxIteration: req.MaxIterations, WithoutWriteTodos: true, WithoutGeneralSubAgent: true, ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools, ExecuteSequentially: true}}})
	if err != nil {
		_ = r.emit(Event{Type: "run.failed", Error: "agent initialization failed"})
		return
	}
	messages := make([]*schema.Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		messages = append(messages, toEino(m))
	}
	events := agent.Run(r.ctx, &adk.AgentInput{Messages: messages})
	final := ""
	for {
		e, ok := events.Next()
		if !ok {
			break
		}
		if e.Err != nil {
			err = e.Err
			break
		}
		if e.Output != nil && e.Output.MessageOutput != nil {
			m := e.Output.MessageOutput.Message
			if m != nil && m.Role == schema.Assistant {
				if len(m.ToolCalls) > 0 {
					final = ""
				} else {
					final = m.Content
				}
			}
		}
	}
	if err != nil {
		if r.ctx.Err() != nil {
			return
		}
		_ = r.emit(Event{Type: "run.failed", Error: "expert execution failed; check caller model or tool result"})
		return
	}
	final = visibleFinalAnswer(final)
	if strings.TrimSpace(final) == "" {
		_ = r.emit(Event{Type: "run.failed", Error: "model did not generate a final answer"})
		return
	}
	if r.emit(Event{Type: "message.delta", Delta: final}) == nil {
		_ = r.emit(Event{Type: "run.completed", Final: final})
	}
}

// Some providers put their scratchpad envelope in content rather than the
// separate reasoning field. Strip only a leading envelope or an orphan closer
// followed by a Markdown answer; preserve literal tags quoted in ordinary text.
func visibleFinalAnswer(content string) string {
	trimmed := strings.TrimSpace(content)
	if end := strings.Index(trimmed, "</think>"); end >= 0 {
		answer := strings.TrimSpace(trimmed[end+len("</think>"):])
		if strings.HasPrefix(trimmed, "<think>") || strings.HasPrefix(answer, "#") {
			return answer
		}
	}
	return trimmed
}

func toEino(m Message) *schema.Message {
	return &schema.Message{Role: schema.RoleType(m.Role), Content: m.Content, Name: m.Name, ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID, ReasoningContent: m.ReasoningContent}
}
func fromEino(m *schema.Message) Message {
	return Message{Role: string(m.Role), Content: m.Content, Name: m.Name, ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID, ReasoningContent: m.ReasoningContent}
}

type remoteModel struct {
	run      *activeRun
	tools    []Tool
	maxCalls int32
	calls    atomic.Int32
}

func (m *remoteModel) Generate(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	messages := make([]Message, 0, len(input))
	for _, msg := range input {
		messages = append(messages, fromEino(msg))
	}
	call := m.calls.Add(1)
	if call > m.maxCalls {
		return nil, errors.New("model round budget exhausted")
	}
	tools := m.tools
	finalRound := call == m.maxCalls
	if finalRound {
		tools = nil
		messages = append(messages, Message{Role: "system", Content: "本轮是最终答复轮，不再调用工具。直接给出面向用户的结论、依据和限制，勿输出内部思考、分析草稿或 think 标签。根据已经取得的证据和工具错误作答；检索片段仅是局部证据，不能由未命中推断全文没有相关要求。明确资料不足或查询失败的限制，不能编造来源或声称失败操作已完成。"})
	}
	reply, err := m.run.request(ctx, Event{Type: "model.request", Messages: messages, Tools: tools})
	if err != nil {
		return nil, err
	}
	if finalRound && len(reply.Message.ToolCalls) > 0 {
		return nil, errors.New("model requested tools during final answer round")
	}
	return toEino(*reply.Message), nil
}
func (m *remoteModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	r, w := schema.Pipe[*schema.Message](1)
	w.Send(msg, nil)
	w.Close()
	return r, nil
}

type remoteTool struct {
	run  *activeRun
	info schema.ToolInfo
}

func (t *remoteTool) Info(context.Context) (*schema.ToolInfo, error) { return &t.info, nil }
func (t *remoteTool) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	call := schema.ToolCall{ID: compose.GetToolCallID(ctx), Type: "function", Function: schema.FunctionCall{Name: t.info.Name, Arguments: args}}
	reply, err := t.run.request(ctx, Event{Type: "tool.request", Call: &call})
	if err != nil {
		return "", err
	}
	if reply.Result == nil {
		return "", fmt.Errorf("missing tool result")
	}
	return *reply.Result, nil
}

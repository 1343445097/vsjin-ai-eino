// Package runtimewire implements VSJin's stateless expert-agent protocol.
// The caller owns model access, identity, authorized tools, history and audit.
package runtimewire

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
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

const (
	maxWireBytes         = 24 << 20
	maxTextBytes         = 4 << 20
	maxEncodedImageBytes = 16 << 20
	maxDecodedImageBytes = 8 << 20
	maxImagesPerMessage  = 4
	finalSummaryMaxBytes = 12 << 10
)

var errFinalProtocolDetected = errors.New("model returned a tool protocol instead of a final answer")

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type Message struct {
	Role             string            `json:"role"`
	Content          string            `json:"-"`
	ContentParts     []ContentPart     `json:"-"`
	Name             string            `json:"name,omitempty"`
	ToolCalls        []schema.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string            `json:"tool_call_id,omitempty"`
	ReasoningContent string            `json:"reasoning_content,omitempty"`
}

type ContentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *ImageURLPart `json:"image_url,omitempty"`
}

type ImageURLPart struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

func (m Message) MarshalJSON() ([]byte, error) {
	type wireMessage struct {
		Role             string            `json:"role"`
		Content          any               `json:"content,omitempty"`
		Name             string            `json:"name,omitempty"`
		ToolCalls        []schema.ToolCall `json:"tool_calls,omitempty"`
		ToolCallID       string            `json:"tool_call_id,omitempty"`
		ReasoningContent string            `json:"reasoning_content,omitempty"`
	}
	var content any
	if len(m.ContentParts) > 0 {
		content = m.ContentParts
	} else if m.Content != "" {
		content = m.Content
	}
	return json.Marshal(wireMessage{Role: m.Role, Content: content, Name: m.Name, ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID, ReasoningContent: m.ReasoningContent})
}

func (m *Message) UnmarshalJSON(data []byte) error {
	type wireMessage struct {
		Role             string            `json:"role"`
		Content          json.RawMessage   `json:"content"`
		Name             string            `json:"name,omitempty"`
		ToolCalls        []schema.ToolCall `json:"tool_calls,omitempty"`
		ToolCallID       string            `json:"tool_call_id,omitempty"`
		ReasoningContent string            `json:"reasoning_content,omitempty"`
	}
	var wire wireMessage
	if err := decodeStrict(data, &wire); err != nil {
		return errors.New("invalid message")
	}
	*m = Message{Role: wire.Role, Name: wire.Name, ToolCalls: wire.ToolCalls, ToolCallID: wire.ToolCallID, ReasoningContent: wire.ReasoningContent}
	if len(wire.Content) == 0 || string(wire.Content) == "null" {
		return nil
	}
	if wire.Content[0] == '"' {
		if err := json.Unmarshal(wire.Content, &m.Content); err != nil {
			return errors.New("invalid message content")
		}
		return nil
	}
	if wire.Content[0] != '[' {
		return errors.New("invalid message content")
	}
	var rawParts []json.RawMessage
	if err := json.Unmarshal(wire.Content, &rawParts); err != nil || len(rawParts) == 0 {
		return errors.New("invalid message content parts")
	}
	m.ContentParts = make([]ContentPart, 0, len(rawParts))
	for _, raw := range rawParts {
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &header); err != nil {
			return errors.New("invalid message content part")
		}
		switch header.Type {
		case "text":
			var part struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if err := decodeStrict(raw, &part); err != nil || part.Text == "" {
				return errors.New("invalid text content part")
			}
			m.ContentParts = append(m.ContentParts, ContentPart{Type: "text", Text: part.Text})
		case "image_url":
			var part struct {
				Type     string        `json:"type"`
				ImageURL *ImageURLPart `json:"image_url"`
			}
			if err := decodeStrict(raw, &part); err != nil || part.ImageURL == nil {
				return errors.New("invalid image content part")
			}
			if _, _, err := decodeImageDataURL(part.ImageURL.URL); err != nil || (part.ImageURL.Detail != "" && part.ImageURL.Detail != "auto" && part.ImageURL.Detail != "low" && part.ImageURL.Detail != "high") {
				return errors.New("invalid image content part")
			}
			m.ContentParts = append(m.ContentParts, ContentPart{Type: "image_url", ImageURL: part.ImageURL})
		default:
			return errors.New("unsupported message content part")
		}
	}
	return validateMessageParts(*m)
}

func decodeStrict(data []byte, out any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("trailing content")
	}
	return nil
}

func validateMessageParts(m Message) error {
	images := 0
	for _, part := range m.ContentParts {
		if part.Type == "image_url" {
			images++
		}
	}
	if images > maxImagesPerMessage {
		return errors.New("too many images in message")
	}
	return nil
}

func messageHasImages(m Message) bool {
	for _, part := range m.ContentParts {
		if part.Type == "image_url" {
			return true
		}
	}
	return false
}

func validateRequestContent(messages []Message) error {
	var textBytes, encodedImageBytes int
	for _, message := range messages {
		textBytes += len(message.Content)
		for _, part := range message.ContentParts {
			if part.Type == "text" {
				textBytes += len(part.Text)
				continue
			}
			if part.Type != "image_url" || part.ImageURL == nil {
				return errors.New("invalid message content")
			}
			_, _, err := decodeImageDataURL(part.ImageURL.URL)
			if err != nil {
				return err
			}
			_, encoded, _ := strings.Cut(part.ImageURL.URL, ",")
			encodedImageBytes += len(encoded)
		}
	}
	if textBytes > maxTextBytes || encodedImageBytes > maxEncodedImageBytes {
		return errors.New("run content exceeds protocol budget")
	}
	return nil
}

func decodeImageDataURL(value string) (string, []byte, error) {
	prefix, encoded, ok := strings.Cut(value, ",")
	if !ok || !strings.HasSuffix(prefix, ";base64") {
		return "", nil, errors.New("invalid image data URL")
	}
	mimeType := strings.TrimPrefix(strings.TrimSuffix(prefix, ";base64"), "data:")
	if mimeType != "image/png" && mimeType != "image/jpeg" && mimeType != "image/webp" {
		return "", nil, errors.New("unsupported image type")
	}
	if encoded == "" || strings.ContainsAny(encoded, " \t\r\n") {
		return "", nil, errors.New("invalid image base64")
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(decoded) > maxDecodedImageBytes || base64.StdEncoding.EncodeToString(decoded) != encoded {
		return "", nil, errors.New("invalid image base64")
	}
	return mimeType, decoded, nil
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
	Version          string            `json:"version"`
	RunID            string            `json:"runId"`
	RunToken         string            `json:"runToken"`
	Messages         []Message         `json:"messages"`
	Tools            []Tool            `json:"tools"`
	InitialToolCalls []schema.ToolCall `json:"initialToolCalls,omitempty"`
	MaxIterations    int               `json:"maxIterations"`
	TimeoutSeconds   int               `json:"timeoutSeconds"`
	Temperature      float64           `json:"temperature"`
}
type Event struct {
	Version   string           `json:"version"`
	Type      string           `json:"type"`
	RunID     string           `json:"runId"`
	Sequence  int              `json:"sequence"`
	Features  []string         `json:"features,omitempty"`
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
	Delta     string   `json:"delta,omitempty"`
	Error     string   `json:"error,omitempty"`
}
type pending struct {
	kind  string
	reply chan Response
}
type activeRun struct {
	mu          sync.Mutex
	deltaMu     sync.Mutex
	token       string
	ctx         context.Context
	cancel      context.CancelFunc
	events      chan Event
	pending     map[string]pending
	sequence    int
	finalDeltas strings.Builder
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
	if decodeLimit(w, r, &response, maxTextBytes) != nil {
		http.Error(w, "invalid response", 400)
		return
	}
	run.mu.Lock()
	p, exists := run.pending[response.RequestID]
	delta := response.Delta != ""
	valid := exists && ((delta && response.Error == "" && p.kind == "model.request" && response.Message == nil && response.Result == nil) || (response.Error != "" && response.Message == nil && response.Result == nil && !delta) || (response.Error == "" && p.kind == "model.request" && response.Message != nil && response.Result == nil && response.Message.Role == "assistant" && !messageHasImages(*response.Message) && !delta) || (response.Error == "" && p.kind == "tool.request" && response.Result != nil && response.Message == nil && !delta))
	if valid && !delta {
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
	return decodeLimit(w, r, out, maxWireBytes)
}

func decodeLimit(w http.ResponseWriter, r *http.Request, out any, limit int64) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing input")
	}
	return nil
}

func validInitialToolCalls(calls []schema.ToolCall, declared map[string]bool, existingIDs map[string]bool) bool {
	if len(calls) > 2 {
		return false
	}
	ids := make(map[string]bool, len(existingIDs)+len(calls))
	for id := range existingIDs {
		ids[id] = true
	}
	for _, call := range calls {
		if call.Type != "function" || len(call.Extra) > 0 || !identifier.MatchString(call.ID) || ids[call.ID] || !declared[call.Function.Name] || len(call.Function.Arguments) == 0 || len(call.Function.Arguments) > maxTextBytes {
			return false
		}
		var args any
		decoder := json.NewDecoder(strings.NewReader(call.Function.Arguments))
		decoder.UseNumber()
		if decoder.Decode(&args) != nil || decoder.Decode(new(any)) != io.EOF {
			return false
		}
		object, ok := args.(map[string]any)
		if !ok || hasUnsafeBootstrapArgument(object) {
			return false
		}
		ids[call.ID] = true
	}
	return true
}

func hasUnsafeBootstrapArgument(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(key))
			if strings.Contains(normalized, "image") || isRawIdentityField(normalized) || hasUnsafeBootstrapArgument(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if hasUnsafeBootstrapArgument(child) {
				return true
			}
		}
	case string:
		return strings.HasPrefix(strings.ToLower(strings.TrimSpace(typed)), "data:image/")
	}
	return false
}

func isRawIdentityField(key string) bool {
	if strings.Contains(key, "identity") {
		return true
	}
	switch key {
	case "user", "userid", "userids", "username", "useremail", "userphone", "tenant", "tenantid", "tenantids", "department", "departmentid", "departmentids", "dept", "deptid", "employee", "employeeid", "employeeids", "organization", "organizationid", "org", "orgid", "principal", "principalid", "account", "accountid", "email", "phone":
		return true
	default:
		return false
	}
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
		if messageHasImages(m) && m.Role != "user" {
			http.Error(w, "images are only allowed in user messages", 400)
			return
		}
	}
	if validateRequestContent(req.Messages) != nil {
		http.Error(w, "invalid run request", 400)
		return
	}
	seen := map[string]bool{}
	toolNames := map[string]bool{}
	for _, t := range req.Tools {
		var params jsonschema.Schema
		if t.Type != "function" || t.Function.Name == "" || seen[t.Function.Name] || json.Unmarshal(t.Function.Parameters, &params) != nil || params.Type != "object" {
			http.Error(w, "invalid tool contract", 400)
			return
		}
		seen[t.Function.Name] = true
		toolNames[t.Function.Name] = true
	}
	usedIDs := map[string]bool{}
	for _, message := range req.Messages {
		if message.ToolCallID != "" {
			usedIDs[message.ToolCallID] = true
		}
		for _, call := range message.ToolCalls {
			usedIDs[call.ID] = true
		}
	}
	if !validInitialToolCalls(req.InitialToolCalls, toolNames, usedIDs) {
		http.Error(w, "invalid initial tool calls", 400)
		return
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
	if err != nil || len(encoded) > maxWireBytes-1024 {
		return errors.New("run event exceeds protocol budget")
	}
	select {
	case r.events <- e:
		return nil
	case <-r.ctx.Done():
		return r.ctx.Err()
	}
}

func (r *activeRun) emitFinalDelta(delta string) error {
	if delta == "" {
		return nil
	}
	if err := r.emit(Event{Type: "message.delta", Delta: delta}); err != nil {
		return err
	}
	r.deltaMu.Lock()
	r.finalDeltas.WriteString(delta)
	r.deltaMu.Unlock()
	return nil
}

func (r *activeRun) emitFinalResponse(final string) error {
	r.deltaMu.Lock()
	streamed := r.finalDeltas.String()
	r.deltaMu.Unlock()
	if streamed != "" {
		if streamed != final {
			return errors.New("model deltas do not match the final response")
		}
		return nil
	}
	return r.emit(Event{Type: "message.delta", Delta: final})
}
func (r *activeRun) request(ctx context.Context, e Event) (Response, error) {
	return r.requestWithDeltas(ctx, e, nil)
}

func (r *activeRun) requestWithDeltas(ctx context.Context, e Event, onDelta func(string) error) (Response, error) {
	e.RequestID = uuid.NewString()
	ch := make(chan Response, 32)
	r.mu.Lock()
	r.pending[e.RequestID] = pending{kind: e.Type, reply: ch}
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.pending, e.RequestID); r.mu.Unlock() }()
	if err := r.emit(e); err != nil {
		return Response{}, err
	}
	for {
		select {
		case reply := <-ch:
			if reply.Delta != "" {
				if onDelta != nil {
					if err := onDelta(reply.Delta); err != nil {
						return Response{}, err
					}
				}
				continue
			}
			if reply.Error != "" {
				return Response{}, errors.New("caller could not complete " + e.Type)
			}
			return reply, nil
		case <-ctx.Done():
			return Response{}, ctx.Err()
		}
	}
}
func (r *activeRun) execute(req Request) {
	defer close(r.events)
	if r.emit(Event{Type: "run.started", Features: []string{"model.delta", "message.images", "tool.bootstrap"}}) != nil {
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
	if len(req.InitialToolCalls) > 0 {
		calls := append([]schema.ToolCall(nil), req.InitialToolCalls...)
		messages = append(messages, &schema.Message{Role: schema.Assistant, ToolCalls: calls})
		for _, call := range calls {
			result := `{"isError":true,"error":"bootstrap tool failed"}`
			reply, callErr := r.request(r.ctx, Event{Type: "tool.request", Call: &call})
			if callErr == nil && reply.Result != nil {
				result = *reply.Result
			}
			messages = append(messages, schema.ToolMessage(result, call.ID, schema.WithToolName(call.Function.Name)))
		}
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
	final = r.normalizeFinal(final)
	if strings.TrimSpace(final) == "" {
		_ = r.emit(Event{Type: "run.failed", Error: "model did not generate a final answer"})
		return
	}
	if err := r.emitFinalResponse(final); err != nil {
		_ = r.emit(Event{Type: "run.failed", Error: "model deltas do not match the final response"})
		return
	}
	_ = r.emit(Event{Type: "run.completed", Final: final})
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

// Streaming cannot retract text that was already shown. Only remove an
// unambiguous leading envelope; keep literal markers inside the visible body.
// Whole-response callers retain the legacy provider-cleanup heuristic.
func (r *activeRun) normalizeFinal(content string) string {
	r.deltaMu.Lock()
	streamed := r.finalDeltas.Len() > 0
	r.deltaMu.Unlock()
	if !streamed {
		return visibleFinalAnswer(content)
	}
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "<think>") {
		end := strings.Index(trimmed, "</think>")
		if end < 0 {
			return ""
		}
		return strings.TrimSpace(trimmed[end+len("</think>"):])
	}
	if strings.HasPrefix(trimmed, "</think>") {
		answer := strings.TrimSpace(strings.TrimPrefix(trimmed, "</think>"))
		if strings.HasPrefix(answer, "#") {
			return answer
		}
	}
	return trimmed
}

func toEino(m Message) *schema.Message {
	message := &schema.Message{Role: schema.RoleType(m.Role), Content: m.Content, Name: m.Name, ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID, ReasoningContent: m.ReasoningContent}
	if m.Role == "user" && len(m.ContentParts) > 0 {
		message.UserInputMultiContent = make([]schema.MessageInputPart, 0, len(m.ContentParts))
		for _, part := range m.ContentParts {
			switch part.Type {
			case "text":
				message.UserInputMultiContent = append(message.UserInputMultiContent, schema.MessageInputPart{Type: schema.ChatMessagePartTypeText, Text: part.Text})
			case "image_url":
				mimeType, data, _ := decodeImageDataURL(part.ImageURL.URL)
				encoded := base64.StdEncoding.EncodeToString(data)
				message.UserInputMultiContent = append(message.UserInputMultiContent, schema.MessageInputPart{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{Base64Data: &encoded, MIMEType: mimeType}, Detail: schema.ImageURLDetail(part.ImageURL.Detail)}})
			}
		}
	} else if len(m.ContentParts) > 0 {
		var content strings.Builder
		for _, part := range m.ContentParts {
			if part.Type == "text" {
				content.WriteString(part.Text)
			}
		}
		message.Content = content.String()
	}
	return message
}
func fromEino(m *schema.Message) (Message, error) {
	if len(m.AssistantGenMultiContent) > 0 {
		return Message{}, errors.New("assistant multimodal output is unsupported")
	}
	message := Message{Role: string(m.Role), Content: m.Content, Name: m.Name, ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID, ReasoningContent: m.ReasoningContent}
	for _, part := range m.UserInputMultiContent {
		switch part.Type {
		case schema.ChatMessagePartTypeText:
			message.ContentParts = append(message.ContentParts, ContentPart{Type: "text", Text: part.Text})
		case schema.ChatMessagePartTypeImageURL:
			if part.Image == nil || part.Image.Base64Data == nil {
				return Message{}, errors.New("model input image is not inline data")
			}
			mimeType := part.Image.MIMEType
			if mimeType != "image/png" && mimeType != "image/jpeg" && mimeType != "image/webp" {
				return Message{}, errors.New("model input image type is unsupported")
			}
			dataURL := "data:" + mimeType + ";base64," + *part.Image.Base64Data
			if _, _, err := decodeImageDataURL(dataURL); err != nil {
				return Message{}, errors.New("model input image is invalid")
			}
			message.ContentParts = append(message.ContentParts, ContentPart{Type: "image_url", ImageURL: &ImageURLPart{URL: dataURL, Detail: string(part.Image.Detail)}})
		default:
			return Message{}, errors.New("model input contains unsupported content")
		}
	}
	if err := validateMessageParts(message); err != nil {
		return Message{}, err
	}
	return message, nil
}

type remoteModel struct {
	run      *activeRun
	tools    []Tool
	maxCalls int32
	calls    atomic.Int32
}

func (m *remoteModel) Generate(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	message, _, err := m.generate(ctx, input, func(delta string) error {
		return m.run.emitFinalDelta(delta)
	})
	return message, err
}

func (m *remoteModel) generate(ctx context.Context, input []*schema.Message, onDelta func(string) error) (*schema.Message, bool, error) {
	messages := make([]Message, 0, len(input))
	for _, msg := range input {
		converted, err := fromEino(msg)
		if err != nil {
			return nil, false, err
		}
		messages = append(messages, converted)
	}
	call := m.calls.Add(1)
	if call > m.maxCalls {
		return nil, false, errors.New("model round budget exhausted")
	}
	tools := m.tools
	finalRound := call == m.maxCalls || len(tools) == 0
	if finalRound {
		tools = nil
		messages = finalAnswerMessages(messages)
	}
	roundDelta := onDelta
	if !finalRound {
		roundDelta = nil
	}
	var reply Response
	var err error
	if finalRound {
		reply, err = m.requestFinalAnswer(ctx, messages, onDelta)
	} else {
		reply, err = m.run.requestWithDeltas(ctx, Event{Type: "model.request", Messages: messages, Tools: tools}, roundDelta)
	}
	if err != nil {
		return nil, finalRound, err
	}
	if finalRound && len(reply.Message.ToolCalls) > 0 {
		return nil, finalRound, errors.New("model requested tools during final answer round")
	}
	if !finalRound && len(reply.Message.ToolCalls) == 0 {
		return m.generateFinalAnswer(ctx, messages, onDelta)
	}
	message := toEino(*reply.Message)
	if finalRound {
		message.Content = m.run.normalizeFinal(message.Content)
	}
	return message, finalRound, nil
}

// Keep the real user request as the generation trigger. A synthetic request to
// summarize a previous draft changes greetings and ordinary questions into a
// different task. Tool messages remain available; unpublished drafts do not
// become fictional conversation history.
func finalAnswerMessages(messages []Message) []Message {
	instruction := "直接回答本轮用户原问题，仅用中文；本轮工具已关闭，不要输出工具协议、调用计划或内部思考。按问题需要选择自然的答复形式：问候、致谢直接简短回应；能力问题依据授权技能目录回答；常识和解释类问题直接解释，不强制套用业务报告格式。只有问题涉及实际业务查询时才根据已取得的证据作答并说明必要限制；未执行查询不能声称已查询，检索未命中不能推断全文没有相关要求，失败操作不能声称已完成。以下重复的用户消息仍是本轮原问题，不是新的总结任务。"
	var systemText []string
	result := make([]Message, 0, len(messages)+1)
	var originalUser *Message
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			user := messages[i]
			originalUser = &user
			break
		}
	}
	for _, message := range messages {
		if message.Role == "system" {
			if text := messageText(message); text != "" {
				systemText = append(systemText, text)
			}
			continue
		}
		result = append(result, message)
	}
	systemText = append(systemText, instruction)
	result = append([]Message{{Role: "system", Content: strings.Join(systemText, "\n\n")}}, result...)
	if originalUser != nil {
		question := *originalUser
		hadImage := false
		if len(question.ContentParts) > 0 {
			textParts := make([]ContentPart, 0, len(question.ContentParts))
			if question.Content != "" {
				textParts = append(textParts, ContentPart{Type: "text", Text: question.Content})
			}
			for _, part := range question.ContentParts {
				if part.Type == "text" {
					textParts = append(textParts, part)
				} else if part.Type == "image_url" {
					hadImage = true
				}
			}
			question.Content = ""
			question.ContentParts = textParts
		}
		if hadImage && len(question.ContentParts) == 0 && strings.TrimSpace(question.Content) == "" {
			question.Content = "请根据本轮原始图片回答用户的问题。"
		}
		result = append(result, question)
	}
	return result
}

func messageText(message Message) string {
	if len(message.ContentParts) == 0 {
		return message.Content
	}
	var content strings.Builder
	if message.Content != "" {
		content.WriteString(message.Content)
	}
	for _, part := range message.ContentParts {
		if part.Type == "text" {
			content.WriteString(part.Text)
		}
	}
	return content.String()
}

func (m *remoteModel) generateFinalAnswer(ctx context.Context, messages []Message, onDelta func(string) error) (*schema.Message, bool, error) {
	call := m.calls.Add(1)
	if call > m.maxCalls {
		return nil, false, errors.New("model round budget exhausted before final answer")
	}
	finalMessages := finalAnswerMessages(messages)
	reply, err := m.requestFinalAnswer(ctx, finalMessages, onDelta)
	if err != nil {
		return nil, true, err
	}
	if len(reply.Message.ToolCalls) > 0 {
		return nil, true, errors.New("model requested tools during final answer round")
	}
	message := toEino(*reply.Message)
	message.Content = m.run.normalizeFinal(message.Content)
	return message, true, nil
}

func (m *remoteModel) requestFinalAnswer(ctx context.Context, messages []Message, onDelta func(string) error) (Response, error) {
	gate := &finalDeltaGate{forward: onDelta}
	reply, err := m.run.requestWithDeltas(ctx, Event{Type: "model.request", Messages: messages}, gate.accept)
	protocol := gate.protocol || errors.Is(err, errFinalProtocolDetected) || err == nil && reply.Message != nil && containsDSMLToolCall(reply.Message.Content)
	if !protocol {
		if err == nil {
			if flushErr := gate.flush(); flushErr != nil {
				return Response{}, flushErr
			}
		}
		return reply, err
	}
	if gate.emitted {
		return Response{}, errFinalProtocolDetected
	}
	retryMessages := summaryOnlyFinalMessages(messages)
	retry, retryErr := m.run.requestWithDeltas(ctx, Event{Type: "model.request", Messages: retryMessages}, nil)
	if retryErr != nil {
		return Response{}, retryErr
	}
	if retry.Message == nil || containsDSMLToolCall(retry.Message.Content) || len(retry.Message.ToolCalls) > 0 {
		return Response{}, errFinalProtocolDetected
	}
	return retry, nil
}

type finalDeltaGate struct {
	forward  func(string) error
	probe    strings.Builder
	emitted  bool
	protocol bool
}

func (g *finalDeltaGate) accept(delta string) error {
	if g.protocol {
		return nil
	}
	g.probe.WriteString(delta)
	if containsDSMLToolCall(g.probe.String()) {
		g.protocol = true
		return nil
	}
	value := g.probe.String()
	lower := strings.ToLower(value)
	markers := []string{"<｜dsml｜", "<|dsml|", "dsml"}
	markerAt := -1
	for _, marker := range markers {
		if at := strings.Index(lower, marker); at >= 0 && (markerAt < 0 || at < markerAt) {
			markerAt = at
		}
	}
	if markerAt >= 0 {
		if markerAt > 0 {
			if err := g.send(value[:markerAt]); err != nil {
				return err
			}
			value = value[markerAt:]
		}
		if len(value) <= 512 {
			g.probe.Reset()
			g.probe.WriteString(value)
			return nil
		}
	}
	hold := possibleDSMLPrefixSuffix(value)
	safe := value[:len(value)-hold]
	if err := g.send(safe); err != nil {
		return err
	}
	g.probe.Reset()
	g.probe.WriteString(value[len(value)-hold:])
	return nil
}

func (g *finalDeltaGate) send(value string) error {
	if value == "" {
		return nil
	}
	if g.forward != nil {
		if err := g.forward(value); err != nil {
			return err
		}
	}
	g.emitted = true
	return nil
}

func possibleDSMLPrefixSuffix(value string) int {
	lower := strings.ToLower(value)
	max := 0
	for _, marker := range []string{"<｜dsml｜", "<|dsml|", "dsml"} {
		limit := len(marker) - 1
		if len(lower) < limit {
			limit = len(lower)
		}
		for size := 1; size <= limit; size++ {
			if strings.HasSuffix(lower, marker[:size]) && size > max {
				max = size
			}
		}
	}
	return max
}

func (g *finalDeltaGate) flush() error {
	if g.protocol || g.probe.Len() == 0 {
		return nil
	}
	if err := g.send(g.probe.String()); err != nil {
		return err
	}
	g.probe.Reset()
	return nil
}

func containsDSMLToolCall(content string) bool {
	lower := strings.ToLower(content)
	marker := strings.Contains(lower, "<｜dsml｜") || strings.Contains(lower, "<|dsml|") || strings.Contains(lower, "dsml")
	return marker && strings.Contains(lower, "invoke") && strings.Contains(lower, "skill_http_request")
}

func summaryOnlyFinalMessages(messages []Message) []Message {
	var systems []Message
	var originalUser *Message
	var summaries []string
	for _, message := range messages {
		switch message.Role {
		case "system":
			if text := messageText(message); text != "" {
				systems = append(systems, Message{Role: "system", Content: text})
			}
		case "user":
			copy := message
			originalUser = &copy
		case "tool":
			if summary := toolResultSummary(message.Content); summary != "" {
				summaries = append(summaries, summary)
			}
		}
	}
	instruction := "本轮最终答复纠正：所有工具均已关闭。只用中文回答原问题，不输出工具协议、调用计划或内部思考。仅根据原问题、授权系统指令和下列工具 Summary 作答；证据不足或无法确认已完整读取时，明确说明不足，不得声称完成。"
	if len(systems) == 0 {
		systems = append(systems, Message{Role: "system", Content: instruction})
	} else {
		systems[0].Content += "\n\n" + instruction
	}
	out := append([]Message(nil), systems...)
	if originalUser != nil {
		out = append(out, *originalUser)
	}
	summary := strings.Join(summaries, "\n")
	if summary == "" {
		summary = "没有可用的工具 Summary；若原问题无法由现有信息回答，必须说明证据不足。"
	}
	if len(summary) > finalSummaryMaxBytes {
		summary = summary[:finalSummaryMaxBytes]
		for !json.Valid([]byte(fmt.Sprintf("%q", summary))) {
			summary = summary[:len(summary)-1]
		}
	}
	out = append(out, Message{Role: "user", Content: "已取得的工具摘要：\n" + summary})
	return out
}

func toolResultSummary(content string) string {
	var root any
	if json.Unmarshal([]byte(content), &root) != nil {
		return ""
	}
	var summaries []string
	var visit func(any)
	visit = func(value any) {
		switch item := value.(type) {
		case map[string]any:
			for key, child := range item {
				if strings.EqualFold(key, "summary") && child != nil {
					encoded, err := json.Marshal(child)
					if err == nil {
						summaries = append(summaries, string(encoded))
					}
					continue
				}
				visit(child)
			}
		case []any:
			for _, child := range item {
				visit(child)
			}
		}
	}
	visit(root)
	return strings.Join(summaries, "\n")
}
func (m *remoteModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	r, w := schema.Pipe[*schema.Message](32)
	go func() {
		defer w.Close()
		var streamed strings.Builder
		msg, finalRound, err := m.generate(ctx, input, func(delta string) error {
			if err := m.run.emitFinalDelta(delta); err != nil {
				return err
			}
			streamed.WriteString(delta)
			if w.Send(schema.AssistantMessage(delta, nil), nil) {
				return context.Canceled
			}
			return nil
		})
		if err != nil {
			w.Send(nil, err)
			return
		}
		if finalRound && streamed.Len() > 0 {
			if streamed.String() != msg.Content {
				w.Send(nil, errors.New("model deltas do not match its final response"))
			}
			return
		}
		if w.Send(msg, nil) {
			return
		}
	}()
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

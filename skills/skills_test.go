package skills

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/tool"
)

func writePack(t *testing.T, root, name, frontmatter, body, operations string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(frontmatter+"\n\n"+body), 0o600); err != nil {
		t.Fatal(err)
	}
	if operations != "" {
		if err := os.WriteFile(filepath.Join(dir, "http-tools.json"), []byte(operations), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

const sampleOperations = `{"groupKeys":["knowledge.read"],"operations":[{"id":"lookup.read","method":"POST","path":"/v1/lookup","groupKey":"knowledge.read","description":"Read one record","inputSchema":{"type":"object","properties":{"query":{"type":"string"},"scopeType":{"type":"string","enum":["project"]},"scopeId":{"type":"string"}},"required":["query","scopeType","scopeId"],"additionalProperties":false}},{"id":"other.read","method":"GET","path":"/v1/other","groupKey":"other.read","description":"Other read","inputSchema":{"type":"object","properties":{},"additionalProperties":false}}]}`

func TestLoadFiltersGroupsAndIncludesAuthorizedInstructionOnlyPack(t *testing.T) {
	root := t.TempDir()
	writePack(t, root, "docs", "---\nname: docs\ndescription: docs description\ngroupKey: knowledge.read\n---", "docs body", sampleOperations)
	writePack(t, root, "guidance", "---\nname: guidance\ndescription: guidance description\n---", "guidance body", `{"groupKeys":["guidance.only"],"operations":[]}`)
	catalog, err := Load(root, []string{"knowledge.read", "guidance.only"})
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Skills) != 2 || len(catalog.Operations) != 1 {
		t.Fatalf("got %d skills and %d operations", len(catalog.Skills), len(catalog.Operations))
	}
	if got := catalog.Prompt(); !strings.Contains(got, "guidance description") || strings.Contains(got, "Other read") || !strings.Contains(got, "skill_http_request({method, path, body})") {
		t.Fatalf("prompt did not reflect allowed groups: %s", got)
	}
	if got, err := catalog.Read("docs"); err != nil || got != "docs body" {
		t.Fatalf("Read() = %q, %v", got, err)
	}
}

func TestLoadRejectsSymlinkedPack(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writePack(t, outside, "external", "---\nname: external\ndescription: external\n---", "secret", "")
	if err := os.Symlink(filepath.Join(outside, "external"), filepath.Join(root, "linked")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Load(root, []string{"*"}); err == nil {
		t.Fatal("Load accepted a symlink escaping the policy root")
	}
}

func TestLoadRejectsTraversalName(t *testing.T) {
	if _, err := (Catalog{Skills: []Skill{{Name: "../outside"}}}).Read("../outside"); err == nil {
		t.Fatal("Read accepted a traversal name")
	}
}

func TestPolicyPacksExposeDefaultBuiltinsAndGroupGatedReadContracts(t *testing.T) {
	root := filepath.Join("..", "policy", "skills")
	wantBase := map[string]string{
		"knowledge.search":                 "/knowledge-base/skill-tools/knowledge.search",
		"knowledge.get_chunk_context":      "/knowledge-base/skill-tools/knowledge.get_chunk_context",
		"knowledge.get_source":             "/knowledge-base/skill-tools/knowledge.get_source",
		"knowledge.get_file_preview":       "/knowledge-base/skill-tools/knowledge.get_file_preview",
		"knowledge.get_spreadsheet_schema": "/knowledge-base/skill-tools/knowledge.get_spreadsheet_schema",
		"knowledge.query_spreadsheet_rows": "/knowledge-base/skill-tools/knowledge.query_spreadsheet_rows",
	}
	wantDefault := map[string]string{
		"cad.find_views":                   "/knowledge-base/skill-tools/cad.find_views",
		"cad.search_text":                  "/knowledge-base/skill-tools/cad.search_text",
		"cad.list_sheets":                  "/knowledge-base/skill-tools/cad.list_sheets",
		"cad.get_sheet":                    "/knowledge-base/skill-tools/cad.get_sheet",
		"knowledge.search":                 wantBase["knowledge.search"],
		"knowledge.get_chunk_context":      wantBase["knowledge.get_chunk_context"],
		"knowledge.get_source":             wantBase["knowledge.get_source"],
		"knowledge.get_file_preview":       wantBase["knowledge.get_file_preview"],
		"knowledge.get_spreadsheet_schema": wantBase["knowledge.get_spreadsheet_schema"],
		"knowledge.query_spreadsheet_rows": wantBase["knowledge.query_spreadsheet_rows"],
	}
	catalog, err := Load(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Skills) != 3 || len(catalog.Operations) != 10 {
		t.Fatalf("default groups loaded %d skills and %d operations, want 3 and 10", len(catalog.Skills), len(catalog.Operations))
	}
	for _, name := range []string{"drawing-analysis", "scai-document-rag", "site-issue-reporting"} {
		if !strings.Contains(catalog.Prompt(), name) {
			t.Errorf("default prompt omitted builtin %q: %s", name, catalog.Prompt())
		}
	}
	for _, operation := range catalog.Operations {
		path, exists := wantDefault[operation.ID]
		wantGroup := "knowledge.base"
		if strings.HasPrefix(operation.ID, "cad.") {
			wantGroup = "knowledge.cad"
		}
		if !exists || path != operation.Path || operation.Method != "POST" || operation.GroupKey != wantGroup {
			t.Errorf("unexpected default operation contract: %+v", operation)
		}
		properties, _ := operation.InputSchema["properties"].(map[string]any)
		scopeType, _ := properties["scopeType"].(map[string]any)
		scopeID, _ := properties["scopeId"].(map[string]any)
		if scopeType["type"] != "string" || scopeID["type"] != "string" {
			t.Errorf("operation %s has incorrect trusted scope field types", operation.ID)
		}
	}
	cadEnabled, err := Load(root, []string{"knowledge.base", "knowledge.cad", "scai.reporting"})
	if err != nil || len(cadEnabled.Skills) != 3 || len(cadEnabled.Operations) != 10 {
		t.Fatalf("explicit groups loaded %d skills and %d operations, err=%v", len(cadEnabled.Skills), len(cadEnabled.Operations), err)
	}
	wantCAD := map[string]string{
		"cad.find_views":  "/knowledge-base/skill-tools/cad.find_views",
		"cad.search_text": "/knowledge-base/skill-tools/cad.search_text",
		"cad.list_sheets": "/knowledge-base/skill-tools/cad.list_sheets",
		"cad.get_sheet":   "/knowledge-base/skill-tools/cad.get_sheet",
	}
	for _, operation := range cadEnabled.Operations {
		if operation.GroupKey == "knowledge.cad" {
			path, exists := wantCAD[operation.ID]
			if !exists || path != operation.Path || operation.Method != "POST" {
				t.Errorf("unexpected CAD operation contract: %+v", operation)
			}
		}
	}
	reporting, err := Load(root, []string{"scai.reporting"})
	if err != nil || len(reporting.Skills) != 1 || len(reporting.Operations) != 0 {
		t.Fatalf("reporting pack = %d skills, %d operations, err=%v", len(reporting.Skills), len(reporting.Operations), err)
	}
	body, err := reporting.Read("site-issue-reporting")
	if err != nil || !strings.Contains(body, "业务适配器") || !strings.Contains(body, "不得声称已保存草稿") {
		t.Fatalf("reporting instructions do not state the adapter boundary: %q, %v", body, err)
	}
}

func TestLoadAcceptsIdenticalSharedOperationAndRejectsConflicts(t *testing.T) {
	root := t.TempDir()
	shared := `{"groupKeys":["knowledge.base"],"operations":[{"id":"knowledge.search","method":"POST","path":"/knowledge/search","groupKey":"knowledge.base","description":"Search","inputSchema":{"type":"object","properties":{},"additionalProperties":false}}]}`
	writePack(t, root, "drawing", "---\nname: drawing\ndescription: Drawing\n---", "drawing body", shared)
	writePack(t, root, "documents", "---\nname: documents\ndescription: Documents\n---", "documents body", shared)
	catalog, err := Load(root, []string{"knowledge.base"})
	if err != nil || len(catalog.Skills) != 2 || len(catalog.Operations) != 1 {
		t.Fatalf("identical shared operation result: skills=%d operations=%d err=%v", len(catalog.Skills), len(catalog.Operations), err)
	}
	conflict := strings.Replace(shared, `"description":"Search"`, `"description":"Different search contract"`, 1)
	writePack(t, root, "conflict", "---\nname: conflict\ndescription: Conflict\n---", "conflict body", conflict)
	if _, err := Load(root, []string{"knowledge.base"}); err == nil {
		t.Fatal("Load accepted a conflicting duplicate operation ID")
	}
}

func TestLoadRejectsMethodsOtherThanGetAndPost(t *testing.T) {
	root := t.TempDir()
	ops := `{"groupKeys":["knowledge.base"],"operations":[{"id":"write","method":"PUT","path":"/v1/write","groupKey":"knowledge.base","description":"Write","inputSchema":{"type":"object","properties":{},"additionalProperties":false}}]}`
	writePack(t, root, "invalid", "---\nname: invalid\ndescription: Invalid\n---", "body", ops)
	if _, err := Load(root, []string{"knowledge.base"}); err == nil {
		t.Fatal("Load accepted an unsupported HTTP method")
	}
}

func TestToolsExposeGenericNamesAndReportUnconfiguredHTTP(t *testing.T) {
	catalog := Catalog{Skills: []Skill{{Name: "docs", Description: "Read docs", Body: "private details"}}, Operations: []Operation{{ID: "lookup.read", Method: "POST", Path: "/v1/lookup", GroupKey: "knowledge.read", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}, "additionalProperties": false}}}}
	tools := NewTools(catalog, ClientConfig{})
	if len(tools) != 2 {
		t.Fatalf("got %d tools, want 2", len(tools))
	}
	invokable, ok := tools[0].(tool.InvokableTool)
	if !ok {
		t.Fatal("skills_read is not invokable")
	}
	info, err := tools[0].Info(context.Background())
	if err != nil || info.Name != "skills_read" {
		t.Fatalf("first tool info = %#v, %v", info, err)
	}
	if _, err := info.ParamsOneOf.ToJSONSchema(); err != nil {
		t.Fatalf("skills_read did not expose a valid Eino JSON schema: %v", err)
	}
	readParams, _ := info.ParamsOneOf.ToJSONSchema()
	readSchemaJSON, _ := json.Marshal(readParams)
	if !strings.Contains(string(readSchemaJSON), `"skill"`) || strings.Contains(string(readSchemaJSON), `"name"`) {
		t.Fatalf("skills_read schema does not advertise the canonical skill argument: %s", readSchemaJSON)
	}
	read, err := invokable.InvokableRun(context.Background(), `{"skill":"docs"}`)
	if _, err := invokable.InvokableRun(context.Background(), `{"name":"docs"}`); err == nil {
		t.Fatal("skills_read accepted the legacy name argument instead of skill")
	}
	if err != nil || !strings.Contains(read, "private details") {
		t.Fatalf("skills_read = %q, %v", read, err)
	}
	requestInfo, err := tools[1].Info(context.Background())
	if err != nil || requestInfo.Name != "skill_http_request" {
		t.Fatalf("second tool info = %#v, %v", requestInfo, err)
	}
	if _, err := requestInfo.ParamsOneOf.ToJSONSchema(); err != nil {
		t.Fatalf("skill_http_request did not expose a valid Eino JSON schema: %v", err)
	}
	requestParams, _ := requestInfo.ParamsOneOf.ToJSONSchema()
	requestSchemaJSON, _ := json.Marshal(requestParams)
	for _, field := range []string{`"method"`, `"path"`, `"body"`} {
		if !strings.Contains(string(requestSchemaJSON), field) {
			t.Fatalf("skill_http_request schema omitted %s: %s", field, requestSchemaJSON)
		}
	}
	if strings.Contains(string(requestSchemaJSON), `"operationId"`) {
		t.Fatalf("skill_http_request schema exposes legacy operationId: %s", requestSchemaJSON)
	}
	request := tools[1].(tool.InvokableTool)
	result, err := request.InvokableRun(context.Background(), `{"method":"POST","path":"/v1/lookup","body":{"query":"hello"}}`)
	if err != nil || !strings.Contains(strings.ToLower(result), "not configured") {
		t.Fatalf("unconfigured request = %q, %v", result, err)
	}
}

func TestHTTPRequestInjectsScopeAndKeepsCredentialsServerSide(t *testing.T) {
	const token = "secret-token-value"
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/lookup" {
			t.Errorf("request target = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("authorization header = %q", r.Header.Get("Authorization"))
		}
		data, _ := io.ReadAll(r.Body)
		gotBody = string(data)
		w.Write([]byte(`{"code":0,"data":{"ok":true},"msg":"ok"}`))
	}))
	defer server.Close()
	catalog := Catalog{Operations: []Operation{{ID: "lookup.read", Method: "POST", Path: "/v1/lookup", GroupKey: "knowledge.read", Description: "Read", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}, "scopeType": map[string]any{"type": "string", "enum": []any{"project"}}, "scopeId": map[string]any{"type": "string"}}, "required": []any{"query", "scopeType", "scopeId"}, "additionalProperties": false}}}}
	request := NewTools(catalog, ClientConfig{BaseURL: server.URL, Token: token, ProjectID: "project-42", Timeout: time.Second})[1].(tool.InvokableTool)
	result, err := request.InvokableRun(context.Background(), `{"method":"POST","path":"/v1/lookup","body":{"query":"hello"}}`)
	if err != nil || result != `{"ok":true}` {
		t.Fatalf("request result = %q, %v", result, err)
	}
	if !strings.Contains(gotBody, `"scopeType":"project"`) || !strings.Contains(gotBody, `"scopeId":"project-42"`) {
		t.Fatalf("trusted scope was not injected: %s", gotBody)
	}
	if strings.Contains(result, token) || strings.Contains(result, server.URL) {
		t.Fatalf("result exposed client configuration: %s", result)
	}
}

func TestHTTPRequestUnwrapsHRSSResponseAndMasksBusinessFailure(t *testing.T) {
	op := Operation{ID: "lookup.read", Method: "POST", Path: "/v1/lookup", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}
	for _, test := range []struct {
		name string
		body string
		want string
		fail bool
	}{
		{name: "success returns data", body: `{"code":0,"data":{"hits":[1]},"msg":"ok"}`, want: `{"hits":[1]}`},
		{name: "business failure is masked", body: `{"code":23,"data":{"secret":"do not expose"},"msg":"private downstream detail"}`, fail: true},
		{name: "malformed envelope is rejected", body: `{"code":"0","data":{},"msg":"ok"}`, fail: true},
		{name: "missing envelope is rejected", body: `{"ok":true}`, fail: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(test.body)) }))
			defer server.Close()
			request := NewTools(Catalog{Operations: []Operation{op}}, ClientConfig{BaseURL: server.URL})[1].(tool.InvokableTool)
			result, err := request.InvokableRun(context.Background(), `{"method":"POST","path":"/v1/lookup","body":{}}`)
			if test.fail {
				if err == nil || strings.Contains(err.Error(), "private downstream detail") || strings.Contains(err.Error(), "secret") {
					t.Fatalf("business/malformed response was not safely rejected: result=%q err=%v", result, err)
				}
				return
			}
			if err != nil || result != test.want {
				t.Fatalf("result = %q, err = %v; want %q", result, err, test.want)
			}
		})
	}
}

func TestHTTPRequestSupportsConfiguredXTokenHeader(t *testing.T) {
	const token = "hrss-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Token") != token || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected credential headers: X-Token=%q Authorization=%q", r.Header.Get("X-Token"), r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"ok":true},"msg":"ok"}`))
	}))
	defer server.Close()
	op := Operation{ID: "lookup.read", Method: "POST", Path: "/v1/lookup", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}
	request := NewTools(Catalog{Operations: []Operation{op}}, ClientConfig{BaseURL: server.URL, Token: token, TokenHeader: "X-Token"})[1].(tool.InvokableTool)
	if _, err := request.InvokableRun(context.Background(), `{"method":"POST","path":"/v1/lookup","body":{}}`); err != nil {
		t.Fatal(err)
	}
}

func TestGETHTTPRequestEncodesValidatedBodyAsQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Body == nil {
			t.Errorf("GET request method/body = %s/%v", r.Method, r.Body)
		}
		if got := r.URL.Query().Get("query"); got != "sheet A/1" {
			t.Errorf("query parameter = %q", got)
		}
		if got := r.URL.Query().Get("limit"); got != "2" {
			t.Errorf("limit parameter = %q", got)
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"ok":true},"msg":"ok"}`))
	}))
	defer server.Close()
	op := Operation{ID: "lookup.read", Method: "GET", Path: "/v1/lookup", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"}}, "required": []any{"query"}, "additionalProperties": false}}
	request := NewTools(Catalog{Operations: []Operation{op}}, ClientConfig{BaseURL: server.URL})[1].(tool.InvokableTool)
	result, err := request.InvokableRun(context.Background(), `{"method":"GET","path":"/v1/lookup","body":{"query":"sheet A/1","limit":2}}`)
	if err != nil || result != `{"ok":true}` {
		t.Fatalf("result = %q, err = %v", result, err)
	}
}

func TestHTTPRequestRejectsInvalidArgumentsBeforeSending(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; w.Write([]byte(`{}`)) }))
	defer server.Close()
	op := Operation{ID: "lookup.read", Method: "POST", Path: "/v1/lookup", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}, "required": []any{"query"}, "additionalProperties": false}}
	request := NewTools(Catalog{Operations: []Operation{op}}, ClientConfig{BaseURL: server.URL})[1].(tool.InvokableTool)
	for _, args := range []string{
		`{"method":"POST","path":"/v1/lookup","body":{"unknown":true}}`,
		`{"method":"POST","path":"/v1/lookup","body":{"query":3}}`,
		`{"method":"POST","path":"/v1/lookup","body":{"query":"x","userId":"attacker"}}`,
		`{"method":"POST","path":"/v1/lookup","body":{"query":"x","query":"y"}}`,
		`{"method":"GET","path":"/v1/lookup","body":{"query":"x"}}`,
		`{"method":"POST","path":"/v1/unlisted","body":{"query":"x"}}`,
	} {
		if _, err := request.InvokableRun(context.Background(), args); err == nil {
			t.Errorf("accepted invalid arguments %s", args)
		}
	}
	if calls != 0 {
		t.Fatalf("made %d HTTP requests for invalid arguments", calls)
	}
}

func TestHTTPResponseIsBoundedAndDownstreamErrorsAreMasked(t *testing.T) {
	secretURL := "https://private.example/internal"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(strings.Repeat("x", 512*1024+1))) }))
	defer server.Close()
	op := Operation{ID: "lookup.read", Method: "POST", Path: "/v1/lookup", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}
	request := NewTools(Catalog{Operations: []Operation{op}}, ClientConfig{BaseURL: server.URL, Token: "no-echo"})[1].(tool.InvokableTool)
	result, err := request.InvokableRun(context.Background(), `{"method":"GET","path":"/v1/lookup","body":{}}`)
	if err == nil || strings.Contains(result, "xxx") || strings.Contains(err.Error(), secretURL) {
		t.Fatalf("oversized response was not bounded: len=%d err=%v", len(result), err)
	}
}

func TestHTTPRequestRejectsBodiesOver64KiBBeforeSending(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; w.Write([]byte(`{}`)) }))
	defer server.Close()
	op := Operation{ID: "lookup.read", Method: "POST", Path: "/v1/lookup", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}, "required": []any{"query"}, "additionalProperties": false}}
	request := NewTools(Catalog{Operations: []Operation{op}}, ClientConfig{BaseURL: server.URL})[1].(tool.InvokableTool)
	args, _ := json.Marshal(map[string]any{"method": "POST", "path": "/v1/lookup", "body": map[string]any{"query": strings.Repeat("x", maxRequestBytes)}})
	if _, err := request.InvokableRun(context.Background(), string(args)); err == nil {
		t.Fatal("accepted a request body larger than 64 KiB")
	}
	if calls != 0 {
		t.Fatalf("made %d HTTP requests for an oversized body", calls)
	}
}

func TestHTTPDoesNotFollowRedirectsAndMasksRedirectLocation(t *testing.T) {
	called := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called = true; w.Write([]byte(`{}`)) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/private", http.StatusFound)
	}))
	defer redirect.Close()
	op := Operation{ID: "lookup.read", Method: "GET", Path: "/v1/lookup", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}
	request := NewTools(Catalog{Operations: []Operation{op}}, ClientConfig{BaseURL: redirect.URL})[1].(tool.InvokableTool)
	_, err := request.InvokableRun(context.Background(), `{"method":"GET","path":"/v1/lookup","body":{}}`)
	if err == nil || strings.Contains(err.Error(), target.URL) || called {
		t.Fatalf("redirect was followed or exposed: err=%v targetCalled=%v", err, called)
	}
}

func TestHTTPTimeoutErrorDoesNotExposeEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	op := Operation{ID: "lookup.read", Method: "GET", Path: "/v1/lookup", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}
	request := NewTools(Catalog{Operations: []Operation{op}}, ClientConfig{BaseURL: server.URL, Timeout: 10 * time.Millisecond})[1].(tool.InvokableTool)
	_, err := request.InvokableRun(context.Background(), `{"method":"GET","path":"/v1/lookup","body":{}}`)
	if err == nil || strings.Contains(err.Error(), server.URL) {
		t.Fatalf("timeout error = %v", err)
	}
}

func TestSuccessfulHTTPResultRedactsConfiguredSecrets(t *testing.T) {
	token := "secret-token-value"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{"echo":"` + token + `"},"msg":"ok"}`))
	}))
	defer server.Close()
	op := Operation{ID: "lookup.read", Method: "GET", Path: "/v1/lookup", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}
	request := NewTools(Catalog{Operations: []Operation{op}}, ClientConfig{BaseURL: server.URL, Token: token})[1].(tool.InvokableTool)
	result, err := request.InvokableRun(context.Background(), `{"method":"GET","path":"/v1/lookup","body":{}}`)
	if err != nil || strings.Contains(result, token) || strings.Contains(result, server.URL) {
		t.Fatalf("result = %q, err = %v", result, err)
	}
}

func TestDownstreamHTTPErrorBodyIsMasked(t *testing.T) {
	token := "credential-value"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("downstream leaked " + token))
	}))
	defer server.Close()
	op := Operation{ID: "lookup.read", Method: "GET", Path: "/v1/lookup", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}
	request := NewTools(Catalog{Operations: []Operation{op}}, ClientConfig{BaseURL: server.URL, Token: token})[1].(tool.InvokableTool)
	_, err := request.InvokableRun(context.Background(), `{"method":"GET","path":"/v1/lookup","body":{}}`)
	if err == nil || strings.Contains(err.Error(), token) || strings.Contains(err.Error(), server.URL) || strings.Contains(err.Error(), "downstream leaked") {
		t.Fatalf("downstream error was not masked: %v", err)
	}
}

package skills

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	einojsonschema "github.com/eino-contrib/jsonschema"
)

const (
	maxRequestBytes  = 64 * 1024
	maxResponseBytes = 512 * 1024
	maxTimeout       = 45 * time.Second
)

type catalogTools struct {
	catalog    Catalog
	client     ClientConfig
	clientHTTP *http.Client
}

type invokable struct {
	info schema.ToolInfo
	run  func(context.Context, string) (string, error)
}

func (t *invokable) Info(context.Context) (*schema.ToolInfo, error) { return &t.info, nil }
func (t *invokable) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	return t.run(ctx, args)
}

// NewTools creates the two generic Eino tools. An empty BaseURL leaves the
// HTTP tool available for an explicit, truthful unconfigured response.
func NewTools(c Catalog, cfg ClientConfig) []tool.BaseTool {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}
	if cfg.Timeout > maxTimeout {
		cfg.Timeout = maxTimeout
	}
	transport, ok := http.DefaultTransport.(*http.Transport)
	if ok {
		transport = transport.Clone()
	} else {
		transport = &http.Transport{Proxy: http.ProxyFromEnvironment}
	}
	client := &http.Client{Timeout: cfg.Timeout, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	state := &catalogTools{catalog: c, client: cfg, clientHTTP: client}
	readSchema := toEinoSchema(map[string]any{"type": "object", "properties": map[string]any{"skill": map[string]any{"type": "string"}}, "required": []string{"skill"}, "additionalProperties": false})
	requestSchema := toEinoSchema(map[string]any{"type": "object", "properties": map[string]any{
		"method": map[string]any{"type": "string", "enum": operationMethods(c.Operations)},
		"path":   map[string]any{"type": "string", "enum": operationPaths(c.Operations)},
		"body":   map[string]any{"type": "object"},
	}, "required": []string{"method", "path", "body"}, "additionalProperties": false})
	return []tool.BaseTool{
		&invokable{info: schema.ToolInfo{Name: "skills_read", Desc: "Read the full instructions for an available skill by name.", ParamsOneOf: schema.NewParamsOneOfByJSONSchema(readSchema)}, run: state.read},
		&invokable{info: schema.ToolInfo{Name: "skill_http_request", Desc: "Call a declared skill HTTP operation using its exact method and path plus a JSON body. The service and project scope are fixed by trusted runtime configuration.", ParamsOneOf: schema.NewParamsOneOfByJSONSchema(requestSchema)}, run: state.request},
	}
}

func toEinoSchema(value map[string]any) *einojsonschema.Schema {
	data, _ := json.Marshal(value)
	var result einojsonschema.Schema
	_ = json.Unmarshal(data, &result)
	return &result
}

func operationMethods(ops []Operation) []any {
	seen := map[string]bool{}
	values := make([]any, 0, len(ops))
	for _, op := range ops {
		if !seen[op.Method] {
			seen[op.Method] = true
			values = append(values, op.Method)
		}
	}
	return values
}

func operationPaths(ops []Operation) []any {
	seen := map[string]bool{}
	values := make([]any, 0, len(ops))
	for _, op := range ops {
		if !seen[op.Path] {
			seen[op.Path] = true
			values = append(values, op.Path)
		}
	}
	return values
}

func (t *catalogTools) read(_ context.Context, input string) (string, error) {
	args, err := decodeObject(input)
	if err != nil || len(args) != 1 {
		return "", fmt.Errorf("invalid skill read arguments")
	}
	name, ok := args["skill"].(string)
	if !ok {
		return "", fmt.Errorf("invalid skill read arguments")
	}
	return t.catalog.Read(name)
}

func (t *catalogTools) request(ctx context.Context, input string) (string, error) {
	args, err := decodeObject(input)
	if err != nil || len(args) != 3 {
		return "", fmt.Errorf("invalid skill HTTP arguments")
	}
	method, methodOK := args["method"].(string)
	path, pathOK := args["path"].(string)
	if !methodOK || !pathOK {
		return "", fmt.Errorf("invalid skill HTTP arguments")
	}
	body, ok := args["body"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("invalid skill HTTP arguments")
	}
	var op *Operation
	if method != "GET" && method != "POST" {
		return "", fmt.Errorf("operation is not available")
	}
	for i := range t.catalog.Operations {
		if t.catalog.Operations[i].Method == method && t.catalog.Operations[i].Path == path {
			op = &t.catalog.Operations[i]
			break
		}
	}
	if op == nil {
		return "", fmt.Errorf("operation is not available")
	}
	if hasIdentityOverride(body) {
		return "", fmt.Errorf("identity fields are controlled by the runtime")
	}
	if strings.TrimSpace(t.client.BaseURL) == "" {
		return "Service is not configured for skill HTTP operations.", nil
	}
	requestBody := cloneObject(body)
	if err := injectTrustedScope(requestBody, op.InputSchema, t.client.ProjectID); err != nil {
		return "", err
	}
	if err := validateValue(requestBody, op.InputSchema, "body"); err != nil {
		return "", err
	}
	endpoint, err := operationURL(t.client.BaseURL, op.Path)
	if err != nil {
		return "", fmt.Errorf("skill HTTP service is unavailable")
	}
	var requestBodyReader io.Reader
	if op.Method == http.MethodGet {
		query, err := encodeQuery(requestBody)
		if err != nil || len(query) > maxRequestBytes {
			return "", fmt.Errorf("request body exceeds the allowed size")
		}
		parsed, err := url.Parse(endpoint)
		if err != nil {
			return "", fmt.Errorf("skill HTTP service is unavailable")
		}
		parsed.RawQuery = query
		endpoint = parsed.String()
	} else {
		encoded, err := json.Marshal(requestBody)
		if err != nil || len(encoded) > maxRequestBytes {
			return "", fmt.Errorf("request body exceeds the allowed size")
		}
		requestBodyReader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, op.Method, endpoint, requestBodyReader)
	if err != nil {
		return "", fmt.Errorf("skill HTTP service is unavailable")
	}
	if op.Method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}
	if t.client.Token != "" {
		header := t.client.TokenHeader
		if header == "" {
			header = "Authorization"
		}
		token := t.client.Token
		if strings.EqualFold(header, "Authorization") {
			token = "Bearer " + token
		}
		req.Header.Set(header, token)
	}
	resp, err := t.clientHTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("skill HTTP service request failed")
	}
	defer resp.Body.Close()
	limited, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return "", fmt.Errorf("skill HTTP service request failed")
	}
	if len(limited) > maxResponseBytes {
		return "", fmt.Errorf("skill HTTP response exceeds the allowed size")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("skill HTTP service returned status %d", resp.StatusCode)
	}
	result, err := decodeHRSSResponse(limited)
	if err != nil {
		return "", fmt.Errorf("skill HTTP service returned an invalid or unsuccessful response")
	}
	for _, secret := range []string{t.client.Token, t.client.BaseURL} {
		if secret != "" {
			result = strings.ReplaceAll(result, secret, "[redacted]")
		}
	}
	return result, nil
}

func encodeQuery(body map[string]any) (string, error) {
	query := url.Values{}
	for key, value := range body {
		values, err := queryValues(value)
		if err != nil {
			return "", err
		}
		for _, item := range values {
			query.Add(key, item)
		}
	}
	return query.Encode(), nil
}

func queryValues(value any) ([]string, error) {
	switch current := value.(type) {
	case []any:
		values := make([]string, 0, len(current))
		for _, item := range current {
			encoded, err := queryScalar(item)
			if err != nil {
				return nil, err
			}
			values = append(values, encoded)
		}
		return values, nil
	default:
		encoded, err := queryScalar(value)
		if err != nil {
			return nil, err
		}
		return []string{encoded}, nil
	}
}

func queryScalar(value any) (string, error) {
	if _, ok := value.(map[string]any); ok {
		encoded, err := json.Marshal(value)
		return string(encoded), err
	}
	if value == nil {
		return "null", nil
	}
	if text, ok := value.(string); ok {
		return text, nil
	}
	return fmt.Sprint(value), nil
}

func decodeHRSSResponse(raw []byte) (string, error) {
	envelope, err := decodeObject(string(raw))
	if err != nil || len(envelope) != 3 {
		return "", fmt.Errorf("invalid response envelope")
	}
	code, ok := envelope["code"].(json.Number)
	if !ok {
		return "", fmt.Errorf("invalid response code")
	}
	codeValue, err := code.Int64()
	if err != nil || codeValue != 0 {
		return "", fmt.Errorf("business operation failed")
	}
	if _, ok := envelope["msg"].(string); !ok {
		return "", fmt.Errorf("invalid response message")
	}
	data, exists := envelope["data"]
	if !exists {
		return "", fmt.Errorf("missing response data")
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("invalid response data")
	}
	return string(encoded), nil
}

func operationURL(base, path string) (string, error) {
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("invalid base URL")
	}
	if strings.Contains(path, "..") || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return "", fmt.Errorf("invalid operation path")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + path
	parsed.RawPath = ""
	return parsed.String(), nil
}

func cloneObject(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

var identityField = map[string]bool{
	"userid": true, "authorityid": true, "authversion": true,
	"jwt": true, "token": true, "authorization": true, "enterpriseid": true,
	"scopeid": true, "scopetype": true, "projectid": true,
}

var nonIdentityCharacters = regexp.MustCompile(`[^a-zA-Z0-9]`)

func hasIdentityOverride(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if identityField[normalizeKey(key)] || hasIdentityOverride(child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if hasIdentityOverride(child) {
				return true
			}
		}
	}
	return false
}

func normalizeKey(value string) string {
	return strings.ToLower(nonIdentityCharacters.ReplaceAllString(value, ""))
}

func injectTrustedScope(body, schema map[string]any, projectID string) error {
	properties, _ := schema["properties"].(map[string]any)
	required := requiredSet(schema["required"])
	for _, key := range []string{"scopeId", "projectId"} {
		if _, declared := properties[key]; !declared {
			continue
		}
		if !required[key] {
			continue
		}
		if projectID == "" {
			return fmt.Errorf("project scope is not configured")
		}
		body[key] = projectID
	}
	if required["scopeType"] {
		if _, declared := properties["scopeType"]; !declared {
			return fmt.Errorf("operation scope is not configured")
		}
		prop, _ := properties["scopeType"].(map[string]any)
		enum, _ := prop["enum"].([]any)
		if len(enum) != 1 {
			return fmt.Errorf("operation scope is not configured")
		}
		value, ok := enum[0].(string)
		if !ok {
			return fmt.Errorf("operation scope is not configured")
		}
		body["scopeType"] = value
	}
	return nil
}

func requiredSet(value any) map[string]bool {
	result := map[string]bool{}
	if values, ok := value.([]any); ok {
		for _, item := range values {
			if name, ok := item.(string); ok {
				result[name] = true
			}
		}
	}
	return result
}

func validateValue(value any, spec map[string]any, path string) error {
	typeName, _ := spec["type"].(string)
	if !matchesType(value, typeName) {
		return fmt.Errorf("invalid request: %s has the wrong type", path)
	}
	if enums, ok := spec["enum"].([]any); ok {
		matched := false
		for _, candidate := range enums {
			if fmt.Sprint(candidate) == fmt.Sprint(value) && fmt.Sprintf("%T", candidate) == fmt.Sprintf("%T", value) {
				matched = true
			}
		}
		if !matched {
			return fmt.Errorf("invalid request: %s is not allowed", path)
		}
	}
	switch current := value.(type) {
	case map[string]any:
		properties, _ := spec["properties"].(map[string]any)
		if max, ok := spec["maxProperties"].(float64); ok && float64(len(current)) > max {
			return fmt.Errorf("invalid request: %s has too many fields", path)
		}
		for required := range requiredSet(spec["required"]) {
			if _, exists := current[required]; !exists {
				return fmt.Errorf("invalid request: %s.%s is required", path, required)
			}
		}
		additional, _ := spec["additionalProperties"].(bool)
		for key, child := range current {
			raw, known := properties[key]
			if !known {
				if !additional {
					return fmt.Errorf("invalid request: %s contains an unknown field", path)
				}
				continue
			}
			childSpec, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("invalid operation schema")
			}
			if err := validateValue(child, childSpec, path+"."+key); err != nil {
				return err
			}
		}
	case []any:
		if min, ok := spec["minItems"].(float64); ok && float64(len(current)) < min {
			return fmt.Errorf("invalid request: %s has too few items", path)
		}
		if max, ok := spec["maxItems"].(float64); ok && float64(len(current)) > max {
			return fmt.Errorf("invalid request: %s has too many items", path)
		}
		if itemSpec, ok := spec["items"].(map[string]any); ok {
			for i, child := range current {
				if err := validateValue(child, itemSpec, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	}
	if number, ok := value.(json.Number); ok {
		f, _ := number.Float64()
		if min, ok := spec["minimum"].(float64); ok && f < min {
			return fmt.Errorf("invalid request: %s is below minimum", path)
		}
		if max, ok := spec["maximum"].(float64); ok && f > max {
			return fmt.Errorf("invalid request: %s is above maximum", path)
		}
	}
	if text, ok := value.(string); ok {
		if min, ok := spec["minLength"].(float64); ok && float64(len([]rune(text))) < min {
			return fmt.Errorf("invalid request: %s is too short", path)
		}
		if max, ok := spec["maxLength"].(float64); ok && float64(len([]rune(text))) > max {
			return fmt.Errorf("invalid request: %s is too long", path)
		}
		if pattern, ok := spec["pattern"].(string); ok {
			matched, err := regexp.MatchString(pattern, text)
			if err != nil || !matched {
				return fmt.Errorf("invalid request: %s has invalid format", path)
			}
		}
		if format, ok := spec["format"].(string); ok && format == "date-time" {
			if _, err := time.Parse(time.RFC3339, text); err != nil {
				return fmt.Errorf("invalid request: %s has invalid format", path)
			}
		}
	}
	return nil
}

func matchesType(value any, typeName string) bool {
	switch typeName {
	case "":
		return true
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "integer":
		n, ok := value.(json.Number)
		if !ok {
			return false
		}
		_, err := n.Int64()
		return err == nil
	case "number":
		_, ok := value.(json.Number)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	default:
		return false
	}
}

func decodeObject(raw string) (map[string]any, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeUniqueValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing JSON")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected object")
	}
	return object, nil
}

func decodeUniqueValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return token, nil
	}
	switch delim {
	case '{':
		object := map[string]any{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, fmt.Errorf("invalid object key")
			}
			if _, exists := object[key]; exists {
				return nil, fmt.Errorf("duplicate JSON field")
			}
			value, err := decodeUniqueValue(decoder)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		_, err = decoder.Token()
		return object, err
	case '[':
		array := []any{}
		for decoder.More() {
			value, err := decodeUniqueValue(decoder)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		_, err = decoder.Token()
		return array, err
	default:
		return nil, fmt.Errorf("invalid JSON delimiter")
	}
}

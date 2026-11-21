package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr               string `json:"httpAddr"`
	HTTPTimeoutSeconds     int    `json:"httpTimeoutSeconds"`
	MCPMaxResponseBytes    int    `json:"mcpMaxResponseBytes"`
	MCPResponseProfile     string `json:"mcpResponseProfile"`
	MCPForbiddenFieldsPath string `json:"mcpForbiddenFieldsPath"`
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id,omitempty"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type toolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type toolResult struct {
	Content []toolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "goforconf-mcp: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadHTTPConfig()
	if err != nil {
		return err
	}
	if err := loadForbiddenDataFields(cfg.ForbiddenFieldsPath); err != nil {
		return err
	}
	baseURL := cfg.BaseURL
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	maxResponseBytes := cfg.MaxResponseBytes

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	writer := bufio.NewWriter(os.Stdout)
	defer writer.Flush()

	client := &http.Client{Timeout: timeout}
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var request rpcRequest
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.UseNumber()
		if err := decoder.Decode(&request); err != nil {
			writeRPC(writer, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
			continue
		}

		response, shouldRespond := handleRPC(client, baseURL, maxResponseBytes, request, cfg.ResponseProfile)
		if shouldRespond {
			writeRPC(writer, response)
		}
	}
	return scanner.Err()
}

func handleRPC(client *http.Client, baseURL string, maxResponseBytes int, request rpcRequest, profile string) (rpcResponse, bool) {
	if request.ID == nil && strings.HasPrefix(request.Method, "notifications/") {
		return rpcResponse{}, false
	}

	result, err := dispatchRPC(client, baseURL, maxResponseBytes, request, profile)
	response := rpcResponse{JSONRPC: "2.0", ID: request.ID}
	if err != nil {
		response.Error = &rpcError{Code: -32000, Message: err.Error()}
		return response, true
	}
	response.Result = result
	return response, true
}

func dispatchRPC(client *http.Client, baseURL string, maxResponseBytes int, request rpcRequest, profile string) (any, error) {
	switch request.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]any{
				"tools": map[string]any{"listChanged": false},
			},
			"serverInfo": map[string]any{
				"name":    "goforconf-mcp",
				"version": "0.1.0",
			},
		}, nil
	case "tools/list":
		return map[string]any{"tools": toolsForProfile(profile)}, nil
	case "tools/call":
		var params toolCallParams
		decoder := json.NewDecoder(bytes.NewReader(request.Params))
		decoder.UseNumber()
		if err := decoder.Decode(&params); err != nil {
			return nil, fmt.Errorf("parse tools/call params: %w", err)
		}
		if params.Arguments == nil {
			params.Arguments = map[string]any{}
		}
		if _, ok := params.Arguments["responseProfile"]; !ok {
			params.Arguments["responseProfile"] = profile
		}
		return callTool(client, baseURL, maxResponseBytes, params)
	default:
		return nil, fmt.Errorf("unsupported method: %s", request.Method)
	}
}

func callTool(client *http.Client, baseURL string, maxResponseBytes int, params toolCallParams) (toolResult, error) {
	var err error
	params, err = applyResponseProfile(params)
	if err != nil {
		return toolResult{}, err
	}
	var path string
	query := url.Values{}

	switch params.Name {
	case "health":
		path = "/health"
	case "get_data_service_info":
		path = "/data/info"
	case "get_jira_issue":
		path = "/jira/issue"
		copyArg(query, params.Arguments, "key")
		copyArg(query, params.Arguments, "commentsLimit")
	case "describe_data_source":
		return callPostTool(client, baseURL, maxResponseBytes, params, "/data/schema/describe")
	case "execute_data_query":
		return callPostTool(client, baseURL, maxResponseBytes, params, "/data/query")
	case "execute_data_query_text":
		return callPostTool(client, baseURL, maxResponseBytes, params, "/data/query/text")
	case "search_code_fts":
		path = "/code/fts-search"
		copyArg(query, params.Arguments, "q")
		copyArg(query, params.Arguments, "mode")
		copyArg(query, params.Arguments, "limit")
	case "search_xml_fts":
		path = "/xml/fts-search"
		copyArg(query, params.Arguments, "q")
		copyArg(query, params.Arguments, "limit")
	case "semantic_search":
		path = "/semantic/search"
		copyArg(query, params.Arguments, "q")
		copyArg(query, params.Arguments, "kind")
		copyArg(query, params.Arguments, "model")
		copyArg(query, params.Arguments, "limit")
		copyArg(query, params.Arguments, "scanLimit")
	case "analyze_action":
		path = "/analysis/action"
		copyActionArgs(query, params.Arguments)
	case "get_action_evidence":
		path = "/analysis/action/evidence"
		copyActionArgs(query, params.Arguments)
		copyArg(query, params.Arguments, "evidenceLimit")
		copyArg(query, params.Arguments, "traceLimit")
		copyArg(query, params.Arguments, "nextHopLimit")
	case "investigate_action":
		path = "/analysis/investigate"
		copyActionArgs(query, params.Arguments)
		copyArg(query, params.Arguments, "evidenceLimit")
		copyArg(query, params.Arguments, "traceLimit")
		copyArg(query, params.Arguments, "nextHopLimit")
		copyArg(query, params.Arguments, "relationLimit")
	case "investigate_deep":
		if boolArg(params.Arguments, "compact", false) {
			path = "/analysis/investigate-summary"
		} else {
			path = "/analysis/investigate-deep"
		}
		copyActionArgs(query, params.Arguments)
		copyArg(query, params.Arguments, "evidenceLimit")
		copyArg(query, params.Arguments, "traceLimit")
		copyArg(query, params.Arguments, "nextHopLimit")
		copyArg(query, params.Arguments, "relationLimit")
		copyArg(query, params.Arguments, "followUpLimit")
		copyArg(query, params.Arguments, "semantic")
		copyArg(query, params.Arguments, "semanticLimit")
		copyArg(query, params.Arguments, "semanticScanLimit")
		copyArg(query, params.Arguments, "semanticKind")
		copyArg(query, params.Arguments, "semanticModel")
	case "investigate_summary":
		path = "/analysis/investigate-summary"
		copyActionArgs(query, params.Arguments)
		copyArg(query, params.Arguments, "evidenceLimit")
		copyArg(query, params.Arguments, "traceLimit")
		copyArg(query, params.Arguments, "nextHopLimit")
		copyArg(query, params.Arguments, "relationLimit")
		copyArg(query, params.Arguments, "followUpLimit")
		copyArg(query, params.Arguments, "semantic")
		copyArg(query, params.Arguments, "semanticLimit")
		copyArg(query, params.Arguments, "semanticScanLimit")
		copyArg(query, params.Arguments, "semanticKind")
		copyArg(query, params.Arguments, "semanticModel")
	case "investigate_with_data":
		return callInvestigationWithData(client, baseURL, maxResponseBytes, params)
	case "get_metadata_object":
		path = "/metadata/object"
		copyMetadataObjectArgs(query, params.Arguments)
	case "get_object_forms":
		path = "/metadata/object/forms"
		copyMetadataObjectArgs(query, params.Arguments)
	case "get_object_modules":
		path = "/metadata/object/modules"
		copyMetadataObjectArgs(query, params.Arguments)
	case "get_object_entry_points":
		path = "/metadata/object/entry-points"
		copyMetadataObjectArgs(query, params.Arguments)
	case "get_object_entry_traces":
		path = "/metadata/object/entry-traces"
		copyMetadataObjectArgs(query, params.Arguments)
		copyArg(query, params.Arguments, "entryKind")
		copyArg(query, params.Arguments, "form")
		copyArg(query, params.Arguments, "moduleKind")
		copyArg(query, params.Arguments, "depth")
		copyArg(query, params.Arguments, "limit")
	case "get_object_xml_summary":
		path = "/metadata/object/xml-summary"
		copyMetadataObjectArgs(query, params.Arguments)
		copyArg(query, params.Arguments, "limit")
	case "get_based_on":
		path = "/metadata/object/based-on"
		copyMetadataObjectArgs(query, params.Arguments)
		copyArg(query, params.Arguments, "limit")
	case "get_basis_for":
		path = "/metadata/object/basis-for"
		copyMetadataObjectArgs(query, params.Arguments)
		copyArg(query, params.Arguments, "limit")
	case "analyze_create_based_on":
		path = "/metadata/object/create-based-on-analysis"
		copyMetadataObjectArgs(query, params.Arguments)
		copyArg(query, params.Arguments, "form")
		copyArg(query, params.Arguments, "depth")
		copyArg(query, params.Arguments, "limit")
		copyArg(query, params.Arguments, "codeLimit")
	case "get_document_relations":
		path = "/metadata/document/relations"
		copyArg(query, params.Arguments, "name")
		copyArg(query, params.Arguments, "limit")
	case "get_procedure_source":
		path = "/procedures/source"
		copyArg(query, params.Arguments, "id")
	case "trace_procedure":
		path = "/procedures/trace"
		copyArg(query, params.Arguments, "module")
		copyArg(query, params.Arguments, "name")
		copyArg(query, params.Arguments, "depth")
		copyArg(query, params.Arguments, "limit")
	case "find_callers":
		path = "/calls/callers"
		copyArg(query, params.Arguments, "name")
		copyArg(query, params.Arguments, "targetModule")
		copyArg(query, params.Arguments, "resolvedOnly")
		copyArg(query, params.Arguments, "limit")
	case "trace_callers":
		path = "/calls/trace-callers"
		copyArg(query, params.Arguments, "name")
		copyArg(query, params.Arguments, "targetModule")
		copyArg(query, params.Arguments, "depth")
		copyArg(query, params.Arguments, "limit")
	case "search_syntax_helper":
		path = "/syntax-helper/search"
		copyArg(query, params.Arguments, "q")
		copyArg(query, params.Arguments, "limit")
	case "get_syntax_type":
		path = "/syntax-helper/type"
		copyArg(query, params.Arguments, "name")
	case "get_syntax_member":
		path = "/syntax-helper/member"
		copyArg(query, params.Arguments, "name")
		copyArg(query, params.Arguments, "type")
		copyArg(query, params.Arguments, "limit")
	case "get_syntax_table":
		path = "/syntax-helper/table"
		copyArg(query, params.Arguments, "name")
	case "validate_syntax_member":
		body, err := postJSON(client, baseURL, "/syntax-helper/validate", map[string]any{
			"type":   params.Arguments["type"],
			"member": params.Arguments["member"],
		})
		if err != nil {
			return toolResult{}, err
		}
		text, err := formatToolResponse(params.Name, params.Arguments, body, maxResponseBytes)
		if err != nil {
			return toolResult{}, err
		}
		return toolResult{Content: []toolContent{{Type: "text", Text: text}}}, nil
	case "semantic_search_syntax_helper":
		path = "/syntax-helper/semantic-search"
		copyArg(query, params.Arguments, "q")
		copyArg(query, params.Arguments, "model")
		copyArg(query, params.Arguments, "limit")
		copyArg(query, params.Arguments, "scanLimit")
	default:
		return toolResult{}, fmt.Errorf("unknown tool: %s", params.Name)
	}

	body, err := getJSON(client, baseURL, path, query)
	if err != nil {
		return toolResult{}, err
	}
	text, err := formatToolResponse(params.Name, params.Arguments, body, maxResponseBytes)
	if err != nil {
		return toolResult{}, err
	}
	return toolResult{Content: []toolContent{{Type: "text", Text: text}}}, nil
}

func callPostTool(client *http.Client, baseURL string, maxResponseBytes int, params toolCallParams, path string) (toolResult, error) {
	accessContext, err := validateDataToolArguments(params.Name, params.Arguments)
	if err != nil {
		return toolResult{}, err
	}
	body, err := postJSON(client, baseURL, path, params.Arguments)
	if err != nil {
		return toolResult{}, err
	}
	if err := validateDataToolResponse(params.Name, body, accessContext); err != nil {
		return toolResult{}, err
	}
	formatted, err := formatToolResponse(params.Name, params.Arguments, body, maxResponseBytes)
	if err != nil {
		return toolResult{}, err
	}
	return toolResult{Content: []toolContent{{Type: "text", Text: formatted}}}, nil
}

func callInvestigationWithData(client *http.Client, baseURL string, maxResponseBytes int, params toolCallParams) (toolResult, error) {
	query := url.Values{}
	copyActionArgs(query, params.Arguments)
	for _, name := range []string{"evidenceLimit", "traceLimit", "nextHopLimit", "relationLimit", "followUpLimit", "semantic", "semanticLimit", "semanticScanLimit", "semanticKind", "semanticModel"} {
		copyArg(query, params.Arguments, name)
	}
	baseBody, err := getJSON(client, baseURL, "/analysis/investigate-summary", query)
	if err != nil {
		return toolResult{}, err
	}

	var baseInvestigation any
	if err := json.Unmarshal(baseBody, &baseInvestigation); err != nil {
		return toolResult{}, fmt.Errorf("parse investigation response: %w", err)
	}
	checks, ok := params.Arguments["dataChecks"].([]any)
	if !ok || len(checks) == 0 {
		return toolResult{}, fmt.Errorf("dataChecks must contain at least one check")
	}
	if len(checks) > 5 {
		return toolResult{}, fmt.Errorf("dataChecks maximum is 5")
	}

	results := make([]map[string]any, 0, len(checks))
	for index, rawCheck := range checks {
		check, ok := rawCheck.(map[string]any)
		if !ok {
			return toolResult{}, fmt.Errorf("dataChecks[%d] must be an object", index)
		}
		request := map[string]any{
			"requestId":  check["requestId"],
			"queryText":  check["queryText"],
			"parameters": check["parameters"],
			"maxRows":    check["maxRows"],
		}
		if request["maxRows"] == nil && params.Arguments["maxRows"] != nil {
			request["maxRows"] = params.Arguments["maxRows"]
		}
		result := map[string]any{"name": fmt.Sprint(check["name"]), "purpose": fmt.Sprint(check["purpose"])}
		accessContext, err := validateDataToolArguments("execute_data_query_text", request)
		if err != nil {
			result["status"] = "blocked"
			result["error"] = err.Error()
			results = append(results, result)
			continue
		}
		body, err := postJSON(client, baseURL, "/data/query/text", request)
		if err != nil {
			result["status"] = "error"
			result["error"] = err.Error()
			results = append(results, result)
			continue
		}
		if err := validateDataToolResponse("execute_data_query_text", body, accessContext); err != nil {
			result["status"] = "blocked"
			result["error"] = err.Error()
			results = append(results, result)
			continue
		}
		var response any
		if err := json.Unmarshal(body, &response); err != nil {
			result["status"] = "error"
			result["error"] = "cannot parse data service response"
		} else {
			result["status"] = "ok"
			result["response"] = response
		}
		results = append(results, result)
	}

	combined, err := json.Marshal(map[string]any{
		"investigation":  baseInvestigation,
		"dataChecks":     results,
		"dataCheckCount": len(results),
	})
	if err != nil {
		return toolResult{}, err
	}
	text, err := formatToolResponse(params.Name, params.Arguments, combined, maxResponseBytes)
	if err != nil {
		return toolResult{}, err
	}
	return toolResult{Content: []toolContent{{Type: "text", Text: text}}}, nil
}

func copyActionArgs(query url.Values, args map[string]any) {
	for _, name := range []string{"q", "mode", "kind", "name", "form", "moduleKind", "module", "entryKind", "depth", "limit", "codeLimit"} {
		copyArg(query, args, name)
	}
}

func copyMetadataObjectArgs(query url.Values, args map[string]any) {
	copyArg(query, args, "kind")
	copyArg(query, args, "name")
}

func copyArg(query url.Values, args map[string]any, name string) {
	value, ok := args[name]
	if !ok || value == nil {
		return
	}
	query.Set(name, queryValueString(value))
}

func formatToolResponse(toolName string, args map[string]any, body []byte, maxResponseBytes int) (string, error) {
	if boolArg(args, "compact", false) {
		switch toolName {
		case "get_object_modules":
			if text, err := compactModulesResponse(body, intArg(args, "procedureLimit", 20), intArg(args, "procedureOffset", 0)); err == nil {
				body = []byte(text)
			}
		case "get_object_forms":
			if text, err := compactFormsResponse(body, intArg(args, "procedureLimit", 20), intArg(args, "procedureOffset", 0)); err == nil {
				body = []byte(text)
			}
		}
	}
	// Compact the JSON syntax only: preserve strings, numbers and all fields.
	var packed bytes.Buffer
	if err := json.Compact(&packed, body); err == nil {
		body = packed.Bytes()
	}
	if maxResponseBytes > 0 && len(body) > maxResponseBytes {
		hints := []string{
			"Use investigate_summary instead of investigate_deep, or pass compact=true when supported.",
			"Lower limit, codeLimit, evidenceLimit, traceLimit, nextHopLimit, relationLimit, or followUpLimit.",
			"For object modules/forms, pass compact=true and optionally procedureLimit.",
			"Use get_procedure_source for a specific procedure id instead of requesting whole modules.",
		}
		if toolName == "get_jira_issue" {
			hints = []string{"Pass a smaller commentsLimit to get_jira_issue."}
		}
		message := map[string]any{
			"error":            "mcp_response_too_large",
			"tool":             toolName,
			"responseBytes":    len(body),
			"maxResponseBytes": maxResponseBytes,
			"hints":            hints,
		}
		compact, _ := json.Marshal(message)
		return string(compact), nil
	}
	return string(body), nil
}

func compactModulesResponse(body []byte, procedureLimit, procedureOffset int) (string, error) {
	var raw struct {
		Object  map[string]any   `json:"object"`
		Modules []map[string]any `json:"modules"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", err
	}
	type compactProcedure struct {
		ID        any    `json:"id"`
		Module    string `json:"module"`
		Name      string `json:"name"`
		StartLine any    `json:"startLine"`
		EndLine   any    `json:"endLine"`
	}
	type compactModule struct {
		Kind                any                `json:"kind,omitempty"`
		Name                any                `json:"name,omitempty"`
		FullName            any                `json:"fullName,omitempty"`
		ModuleFullName      any                `json:"moduleFullName,omitempty"`
		ProcedureCount      int                `json:"procedureCount"`
		Procedures          []compactProcedure `json:"procedures,omitempty"`
		HasMore             bool               `json:"hasMore"`
		NextProcedureOffset *int               `json:"nextProcedureOffset,omitempty"`
	}
	response := struct {
		Object          map[string]any  `json:"object"`
		ModuleCount     int             `json:"moduleCount"`
		ProcedureLimit  int             `json:"procedureLimit"`
		ProcedureOffset int             `json:"procedureOffset"`
		Modules         []compactModule `json:"modules"`
	}{Object: raw.Object, ModuleCount: len(raw.Modules), ProcedureLimit: procedureLimit, ProcedureOffset: procedureOffset}
	for _, module := range raw.Modules {
		procedures := mapSlice(module["procedures"])
		item := compactModule{
			Kind:           module["kind"],
			Name:           module["name"],
			FullName:       module["fullName"],
			ModuleFullName: module["moduleFullName"],
			ProcedureCount: len(procedures),
		}
		start, end := procedurePage(len(procedures), procedureOffset, procedureLimit)
		item.HasMore = end < len(procedures)
		if item.HasMore && end > start {
			item.NextProcedureOffset = &end
		}
		for _, procedure := range procedures[start:end] {
			item.Procedures = append(item.Procedures, compactProcedure{
				ID:        procedure["id"],
				Module:    fmt.Sprint(procedure["module"]),
				Name:      fmt.Sprint(procedure["name"]),
				StartLine: procedure["startLine"],
				EndLine:   procedure["endLine"],
			})
		}
		response.Modules = append(response.Modules, item)
	}
	data, err := json.Marshal(response)
	return string(data), err
}

func compactFormsResponse(body []byte, procedureLimit, procedureOffset int) (string, error) {
	var raw struct {
		Object map[string]any   `json:"object"`
		Forms  []map[string]any `json:"forms"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", err
	}
	type compactProcedure struct {
		ID        any    `json:"id"`
		Module    string `json:"module"`
		Name      string `json:"name"`
		StartLine any    `json:"startLine"`
		EndLine   any    `json:"endLine"`
	}
	type compactForm struct {
		Name                any                `json:"name,omitempty"`
		ModuleFullName      any                `json:"moduleFullName,omitempty"`
		ProcedureCount      int                `json:"procedureCount"`
		Procedures          []compactProcedure `json:"procedures,omitempty"`
		HasMore             bool               `json:"hasMore"`
		NextProcedureOffset *int               `json:"nextProcedureOffset,omitempty"`
	}
	response := struct {
		Object          map[string]any `json:"object"`
		FormCount       int            `json:"formCount"`
		ProcedureLimit  int            `json:"procedureLimit"`
		ProcedureOffset int            `json:"procedureOffset"`
		Forms           []compactForm  `json:"forms"`
	}{Object: raw.Object, FormCount: len(raw.Forms), ProcedureLimit: procedureLimit, ProcedureOffset: procedureOffset}
	for _, form := range raw.Forms {
		procedures := mapSlice(form["procedures"])
		item := compactForm{
			Name:           form["name"],
			ModuleFullName: form["moduleFullName"],
			ProcedureCount: len(procedures),
		}
		start, end := procedurePage(len(procedures), procedureOffset, procedureLimit)
		item.HasMore = end < len(procedures)
		if item.HasMore && end > start {
			item.NextProcedureOffset = &end
		}
		for _, procedure := range procedures[start:end] {
			item.Procedures = append(item.Procedures, compactProcedure{
				ID:        procedure["id"],
				Module:    fmt.Sprint(procedure["module"]),
				Name:      fmt.Sprint(procedure["name"]),
				StartLine: procedure["startLine"],
				EndLine:   procedure["endLine"],
			})
		}
		response.Forms = append(response.Forms, item)
	}
	data, err := json.Marshal(response)
	return string(data), err
}

func mapSlice(value any) []map[string]any {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if mapped, ok := item.(map[string]any); ok {
			result = append(result, mapped)
		}
	}
	return result
}

func queryValueString(value any) string {
	switch typed := value.(type) {
	case json.Number:
		return typed.String()
	case float64:
		if math.Trunc(typed) == typed {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case float32:
		value64 := float64(typed)
		if math.Trunc(value64) == value64 {
			return strconv.FormatInt(int64(value64), 10)
		}
		return strconv.FormatFloat(value64, 'f', -1, 32)
	default:
		return fmt.Sprint(value)
	}
}

func boolArg(args map[string]any, name string, defaultValue bool) bool {
	value, ok := args[name]
	if !ok || value == nil {
		return defaultValue
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		parsed, err := strconv.ParseBool(typed)
		if err == nil {
			return parsed
		}
	default:
		text := fmt.Sprint(typed)
		parsed, err := strconv.ParseBool(text)
		if err == nil {
			return parsed
		}
	}
	return defaultValue
}

func intArg(args map[string]any, name string, defaultValue int) int {
	value, ok := args[name]
	if !ok || value == nil {
		return defaultValue
	}
	switch typed := value.(type) {
	case int:
		return typed
	case float64:
		return int(typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err == nil {
			return int(parsed)
		}
	case string:
		parsed, err := strconv.Atoi(typed)
		if err == nil {
			return parsed
		}
	default:
		parsed, err := strconv.Atoi(fmt.Sprint(typed))
		if err == nil {
			return parsed
		}
	}
	return defaultValue
}

func getJSON(client *http.Client, baseURL, path string, query url.Values) ([]byte, error) {
	endpoint, err := url.Parse(strings.TrimRight(baseURL, "/") + path)
	if err != nil {
		return nil, err
	}
	endpoint.RawQuery = query.Encode()

	response, err := client.Get(endpoint.String())
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d from %s: %s", response.StatusCode, endpoint.String(), trimForError(body))
	}
	return body, nil
}

func postJSON(client *http.Client, baseURL, path string, value any) ([]byte, error) {
	endpoint := strings.TrimRight(baseURL, "/") + path
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	response, err := client.Post(endpoint, "application/json", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d from %s: %s", response.StatusCode, endpoint, trimForError(body))
	}
	return body, nil
}

func trimForError(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) > 1000 {
		return text[:1000] + "..."
	}
	return text
}

func writeRPC(writer *bufio.Writer, response rpcResponse) {
	data, _ := json.Marshal(response)
	writer.Write(data)
	writer.WriteByte('\n')
	writer.Flush()
}

type httpConfig struct {
	BaseURL             string
	TimeoutSeconds      int
	MaxResponseBytes    int
	ForbiddenFieldsPath string
	ResponseProfile     string
}

func loadHTTPConfig() (httpConfig, error) {
	cfg := Config{HTTPTimeoutSeconds: 300, MCPMaxResponseBytes: 1000000}
	configPath := os.Getenv("GOFORCONF_CONFIG")
	if configPath == "" {
		configPath = "config.local.json"
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return httpConfig{}, fmt.Errorf("read %s: %w", configPath, err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return httpConfig{}, fmt.Errorf("parse %s: %w", configPath, err)
	}
	if cfg.MCPForbiddenFieldsPath != "" && !filepath.IsAbs(cfg.MCPForbiddenFieldsPath) {
		cfg.MCPForbiddenFieldsPath = filepath.Join(filepath.Dir(configPath), cfg.MCPForbiddenFieldsPath)
	}
	if value := os.Getenv("GOFORCONF_HTTP_URL"); value != "" {
		cfg.HTTPAddr = strings.TrimRight(value, "/")
	}
	if value := os.Getenv("GOFORCONF_HTTP_TIMEOUT_SECONDS"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 {
			return httpConfig{}, fmt.Errorf("invalid GOFORCONF_HTTP_TIMEOUT_SECONDS: %q", value)
		}
		cfg.HTTPTimeoutSeconds = parsed
	}
	if value := os.Getenv("GOFORCONF_MCP_MAX_RESPONSE_BYTES"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			return httpConfig{}, fmt.Errorf("invalid GOFORCONF_MCP_MAX_RESPONSE_BYTES: %q", value)
		}
		cfg.MCPMaxResponseBytes = parsed
	}
	if value := os.Getenv("GOFORCONF_MCP_RESPONSE_PROFILE"); value != "" {
		cfg.MCPResponseProfile = value
	}
	profile, err := normalizeResponseProfile(cfg.MCPResponseProfile)
	if err != nil {
		return httpConfig{}, err
	}

	if cfg.HTTPAddr == "" {
		return httpConfig{}, fmt.Errorf("httpAddr is empty in %s", configPath)
	}
	baseURL := cfg.HTTPAddr
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "http://" + baseURL
	}
	if cfg.HTTPTimeoutSeconds <= 0 {
		cfg.HTTPTimeoutSeconds = 300
	}
	if cfg.MCPMaxResponseBytes < 0 {
		cfg.MCPMaxResponseBytes = 1000000
	}
	return httpConfig{BaseURL: strings.TrimRight(baseURL, "/"), TimeoutSeconds: cfg.HTTPTimeoutSeconds, MaxResponseBytes: cfg.MCPMaxResponseBytes, ForbiddenFieldsPath: cfg.MCPForbiddenFieldsPath, ResponseProfile: profile}, nil
}

func tools() []toolDef {
	return toolsForProfile("economy")
}

func toolsForProfile(profile string) []toolDef {
	definitions := baseTools()
	decorateResponseSchemas(definitions, profile)
	return definitions
}

func baseTools() []toolDef {
	return []toolDef{
		{Name: "health", Description: "Check GoForConf HTTP service health.", InputSchema: objectSchema(nil, nil)},
		{Name: "get_data_service_info", Description: "Check the configured live 1C read-only data service and return infobase information.", InputSchema: objectSchema(nil, nil)},
		{Name: "get_jira_issue", Description: "Get a Jira issue with description, comments and attachment metadata through the GoForConf HTTP server. Jira credentials stay on the server.", InputSchema: objectSchema(map[string]any{
			"key":           stringSchema("Jira issue key, for example IC-123."),
			"commentsLimit": integerSchema("Maximum comments to return, from 1 to 1000. Defaults to 1000."),
		}, []string{"key"})},
		{Name: "describe_data_source", Description: "Describe fields of a 1C query data source before composing a live data query.", InputSchema: objectSchema(map[string]any{
			"source": stringSchema("1C query data source in query-language notation, for example Справочник.Номенклатура or РегистрСведений.КурсыВалют."),
		}, []string{"source"})},
		{Name: "execute_data_query", Description: "Execute a bounded read-only structured query against live 1C data. Supports joins, conditions including in/inHierarchy, sorting, and typed parameters.", InputSchema: structuredDataQuerySchema()},
		{Name: "execute_data_query_text", Description: "Execute bounded read-only 1C query-language text against live data with optional typed parameters.", InputSchema: textDataQuerySchema()},
		{Name: "search_code_fts", Description: "Search indexed BSL procedures/functions. Supports mode=fts or mode=wildcard.", InputSchema: objectSchema(map[string]any{
			"q":     stringSchema("Search query. In wildcard mode, * and % mean any number of characters."),
			"mode":  enumSchema([]string{"fts", "wildcard"}, "Search mode."),
			"limit": integerSchema("Maximum results."),
		}, []string{"q"})},
		{Name: "search_xml_fts", Description: "Search indexed top-level metadata XML files.", InputSchema: objectSchema(map[string]any{
			"q":     stringSchema("Search query."),
			"limit": integerSchema("Maximum results."),
		}, []string{"q"})},
		{Name: "semantic_search", Description: "Search nearest RAG chunks using local semantic embeddings. Returns candidate chunks with score, source file, module/procedure/object, line range, and compact text.", InputSchema: semanticSearchSchema()},
		{Name: "analyze_action", Description: "Return a detailed deterministic action analysis packet.", InputSchema: actionSchema()},
		{Name: "get_action_evidence", Description: "Return a compact LLM-ready evidence packet for an action/query.", InputSchema: actionSchema()},
		{Name: "investigate_action", Description: "Orchestrate a bounded action investigation with object context, relations, traces, evidence, and next-hop evidence.", InputSchema: investigationSchema()},
		{Name: "investigate_deep", Description: "Run a first-pass investigation, then execute a bounded number of recommended follow-up tool steps and return one combined packet. Pass compact=true to return the compact investigate_summary payload instead.", InputSchema: deepInvestigationSchema()},
		{Name: "investigate_summary", Description: "Run a bounded investigation and return a compact LLM-ready summary with key procedures, lines, call chains, facts, assumptions, warnings, and next steps.", InputSchema: deepInvestigationSchema()},
		{Name: "investigate_with_data", Description: "Run code/metadata investigation, then execute up to five narrow read-only live-data checks supplied by the caller. Returns one packet separating code evidence, confirmed data facts, blocked checks, and data errors.", InputSchema: investigationWithDataSchema()},
		{Name: "get_metadata_object", Description: "Return metadata object bundle with parts and children.", InputSchema: metadataObjectSchema()},
		{Name: "get_object_forms", Description: "Return forms for a metadata object, including form module names and indexed procedures. Pass compact=true for procedure counts and limited procedure lists.", InputSchema: metadataObjectCompactSchema()},
		{Name: "get_object_modules", Description: "Return object-level modules such as manager/object modules and their procedures. Pass compact=true for procedure counts and limited procedure lists.", InputSchema: metadataObjectCompactSchema()},
		{Name: "get_object_entry_points", Description: "Return heuristic entry procedures for a metadata object.", InputSchema: metadataObjectSchema()},
		{Name: "get_object_entry_traces", Description: "Return entry points with forward resolved call traces.", InputSchema: objectEntryTracesSchema()},
		{Name: "get_object_xml_summary", Description: "Return exploratory XML summary for a metadata object.", InputSchema: objectXMLSummarySchema()},
		{Name: "get_based_on", Description: "Return objects that the requested object can be entered based on.", InputSchema: metadataRelationsSchema()},
		{Name: "get_basis_for", Description: "Return objects that can be entered based on the requested object.", InputSchema: metadataRelationsSchema()},
		{Name: "analyze_create_based_on", Description: "Return deterministic evidence for create-based-on behavior for an object/form.", InputSchema: createBasedOnSchema()},
		{Name: "get_document_relations", Description: "Return classified document XML references for compatibility diagnostics.", InputSchema: documentRelationsSchema()},
		{Name: "get_procedure_source", Description: "Return source text for a procedure by id.", InputSchema: objectSchema(map[string]any{
			"id": integerSchema("Procedure id."),
		}, []string{"id"})},
		{Name: "trace_procedure", Description: "Trace resolved calls forward from a procedure.", InputSchema: objectSchema(map[string]any{
			"module": stringSchema("Procedure module_full_name."),
			"name":   stringSchema("Procedure name."),
			"depth":  integerSchema("Trace depth. Defaults to 10."),
			"limit":  integerSchema("Maximum child calls per procedure. Defaults to 25."),
		}, []string{"module", "name"})},
		{Name: "find_callers", Description: "Find procedures that call a target procedure name, optionally within a target module.", InputSchema: callersSchema()},
		{Name: "trace_callers", Description: "Trace resolved callers backward from a target procedure.", InputSchema: traceCallersSchema()},
		{Name: "search_syntax_helper", Description: "Search the imported 1C syntax helper reference corpus by type, member, table, field, syntax, description, or example.", InputSchema: objectSchema(map[string]any{
			"q":     stringSchema("Search query."),
			"limit": integerSchema("Maximum results. Defaults to 20."),
		}, []string{"q"})},
		{Name: "get_syntax_type", Description: "Return a 1C syntax helper type with constructors, methods, properties, and events.", InputSchema: objectSchema(map[string]any{
			"name": stringSchema("Type name in Russian or English."),
		}, []string{"name"})},
		{Name: "get_syntax_member", Description: "Return 1C syntax helper member details by member name, optionally scoped to a type.", InputSchema: objectSchema(map[string]any{
			"name":  stringSchema("Member name in Russian or English."),
			"type":  stringSchema("Optional type name in Russian or English."),
			"limit": integerSchema("Maximum matches. Defaults to 50."),
		}, []string{"name"})},
		{Name: "get_syntax_table", Description: "Return a 1C query-language table and its fields from the syntax helper.", InputSchema: objectSchema(map[string]any{
			"name": stringSchema("Table name in Russian or English."),
		}, []string{"name"})},
		{Name: "validate_syntax_member", Description: "Check whether a 1C syntax helper type has a method, property, constructor, or event.", InputSchema: objectSchema(map[string]any{
			"type":   stringSchema("Type name in Russian or English."),
			"member": stringSchema("Member name in Russian or English."),
		}, []string{"type", "member"})},
		{Name: "semantic_search_syntax_helper", Description: "Search nearest syntax helper RAG chunks using local semantic embeddings.", InputSchema: objectSchema(map[string]any{
			"q":         stringSchema("Natural language query or search phrase to embed."),
			"model":     stringSchema("Optional embedding model name. Defaults to selected config embeddingModel."),
			"limit":     integerSchema("Maximum nearest chunks. Defaults to 10; maximum 100."),
			"scanLimit": integerSchema("Maximum stored embeddings to scan linearly. Defaults to 0, which scans the whole index. Set a positive value only to deliberately cut the scan short."),
		}, []string{"q"})},
	}
}

func structuredDataQuerySchema() map[string]any {
	return map[string]any{
		"type":        "object",
		"description": "Structured request is passed through to the 1C Data Gateway.",
		"properties": map[string]any{
			"requestId":  stringSchema("Optional correlation identifier."),
			"source":     stringSchema("Primary 1C query data source."),
			"select":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"joins":      map[string]any{"type": "array", "items": map[string]any{"type": "object", "additionalProperties": true}},
			"where":      map[string]any{"type": "array", "items": map[string]any{"type": "object", "additionalProperties": true}},
			"orderBy":    map[string]any{"type": "array", "items": map[string]any{"type": "object", "additionalProperties": true}},
			"parameters": map[string]any{"type": "object", "additionalProperties": true},
			"limit":      integerSchema("Maximum returned rows; the 1C service applies its own upper bound."),
		},
		"required": []string{"source", "select"}, "additionalProperties": true,
	}
}

func textDataQuerySchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"requestId":  stringSchema("Optional correlation identifier."),
			"queryText":  stringSchema("Read-only 1C query-language text."),
			"parameters": map[string]any{"type": "object", "additionalProperties": true},
			"maxRows":    integerSchema("Maximum returned rows; the 1C service applies its own upper bound."),
		},
		"required": []string{"queryText"}, "additionalProperties": false,
	}
}

func metadataObjectSchema() map[string]any {
	return objectSchema(map[string]any{
		"kind": stringSchema("Metadata object kind, for example Document, Catalog, BusinessProcess, Task."),
		"name": stringSchema("Metadata object name."),
	}, []string{"kind", "name"})
}

func metadataObjectCompactSchema() map[string]any {
	return objectSchema(map[string]any{
		"kind":            stringSchema("Metadata object kind, for example Document, Catalog, BusinessProcess, Task."),
		"name":            stringSchema("Metadata object name."),
		"compact":         booleanSchema("Return compact MCP output with procedure counts and a limited procedure list."),
		"procedureLimit":  integerSchema("Maximum procedures per form/module in compact MCP output. Defaults to 20; use 0 for counts only."),
		"procedureOffset": integerSchema("Skip this many procedures per form/module in compact output. Use nextProcedureOffset to fetch the next page. Defaults to 0."),
	}, []string{"kind", "name"})
}

func objectEntryTracesSchema() map[string]any {
	return objectSchema(map[string]any{
		"kind":       stringSchema("Metadata object kind, for example Document."),
		"name":       stringSchema("Metadata object name."),
		"entryKind":  stringSchema("Optional entry kind filter, for example form_open."),
		"form":       stringSchema("Optional form name."),
		"moduleKind": stringSchema("Optional object/common module kind, for example ObjectModule, ManagerModule, Module, CommandModule."),
		"depth":      integerSchema("Trace depth. Defaults to 10."),
		"limit":      integerSchema("Maximum child calls per entry procedure. Defaults to 25."),
	}, []string{"kind", "name"})
}

func objectXMLSummarySchema() map[string]any {
	return objectSchema(map[string]any{
		"kind":  stringSchema("Metadata object kind, for example Document."),
		"name":  stringSchema("Metadata object name."),
		"limit": integerSchema("Maximum keyword/reference matches."),
	}, []string{"kind", "name"})
}

func metadataRelationsSchema() map[string]any {
	return objectSchema(map[string]any{
		"kind":  stringSchema("Metadata object kind, for example Document, Catalog, BusinessProcess, Task."),
		"name":  stringSchema("Metadata object name."),
		"limit": integerSchema("Maximum relations."),
	}, []string{"kind", "name"})
}

func createBasedOnSchema() map[string]any {
	return objectSchema(map[string]any{
		"kind":      stringSchema("Metadata object kind, for example Document."),
		"name":      stringSchema("Metadata object name."),
		"form":      stringSchema("Optional form name."),
		"depth":     integerSchema("Trace depth. Defaults to 10 for action investigations."),
		"limit":     integerSchema("Trace/result limit. Defaults to 25 for action investigations."),
		"codeLimit": integerSchema("Code search limit."),
	}, []string{"kind", "name"})
}

func documentRelationsSchema() map[string]any {
	return objectSchema(map[string]any{
		"name":  stringSchema("Document name."),
		"limit": integerSchema("Maximum relations."),
	}, []string{"name"})
}

func semanticSearchSchema() map[string]any {
	return objectSchema(map[string]any{
		"q":         stringSchema("Natural language query or search phrase to embed."),
		"kind":      stringSchema("Optional semantic chunk kind filter, for example bsl_procedure or xml_object."),
		"model":     stringSchema("Optional embedding model name. Defaults to selected config embeddingModel."),
		"limit":     integerSchema("Maximum nearest chunks to return. Defaults to 10; maximum 100."),
		"scanLimit": integerSchema("Maximum stored embeddings to scan linearly. Defaults to 0, which scans the whole index. Set a positive value only to deliberately cut the scan short."),
	}, []string{"q"})
}

func callersSchema() map[string]any {
	return objectSchema(map[string]any{
		"name":         stringSchema("Callee procedure/function name."),
		"targetModule": stringSchema("Optional target module_full_name."),
		"resolvedOnly": booleanSchema("Only resolved calls. Defaults to true."),
		"limit":        integerSchema("Maximum callers."),
	}, []string{"name"})
}

func traceCallersSchema() map[string]any {
	return objectSchema(map[string]any{
		"name":         stringSchema("Target procedure/function name."),
		"targetModule": stringSchema("Optional target module_full_name."),
		"depth":        integerSchema("Trace depth. Defaults to 10."),
		"limit":        integerSchema("Maximum callers per procedure. Defaults to 25."),
	}, []string{"name"})
}

func actionSchema() map[string]any {
	return objectSchema(map[string]any{
		"q":             stringSchema("Action query. In wildcard mode, * and % mean any number of characters."),
		"mode":          enumSchema([]string{"fts", "wildcard"}, "Search mode."),
		"kind":          stringSchema("Metadata object kind, for example Document."),
		"name":          stringSchema("Metadata object name."),
		"form":          stringSchema("Optional form name."),
		"moduleKind":    stringSchema("Optional object/common module kind, for example ObjectModule, ManagerModule, Module, CommandModule."),
		"entryKind":     stringSchema("Optional entry kind filter."),
		"depth":         integerSchema("Trace depth. Defaults to 10."),
		"limit":         integerSchema("Trace/result limit. Defaults to 25."),
		"codeLimit":     integerSchema("Code search limit."),
		"evidenceLimit": integerSchema("Evidence snippets limit."),
		"traceLimit":    integerSchema("Trace edge limit."),
		"nextHopLimit":  integerSchema("Next-hop evidence procedure limit. Defaults to 5; use 0 to disable; maximum 20."),
		"expand":        booleanSchema("Enable generic token-based query expansion."),
	}, []string{"q"})
}

func investigationSchema() map[string]any {
	return objectSchema(map[string]any{
		"q":             stringSchema("Action query. In wildcard mode, * and % mean any number of characters."),
		"mode":          enumSchema([]string{"fts", "wildcard"}, "Search mode."),
		"kind":          stringSchema("Metadata object kind, for example Document."),
		"name":          stringSchema("Metadata object name."),
		"form":          stringSchema("Optional form name."),
		"moduleKind":    stringSchema("Optional object/common module kind, for example ObjectModule, ManagerModule, Module, CommandModule."),
		"entryKind":     stringSchema("Optional entry kind filter."),
		"depth":         integerSchema("Trace depth. Defaults to 10."),
		"limit":         integerSchema("Trace/result limit. Defaults to 25."),
		"codeLimit":     integerSchema("Code search limit."),
		"evidenceLimit": integerSchema("Evidence snippets limit."),
		"traceLimit":    integerSchema("Trace edge limit."),
		"nextHopLimit":  integerSchema("Next-hop evidence procedure limit. Defaults to 5; use 0 to disable; maximum 20."),
		"relationLimit": integerSchema("Based-on/basis-for relation limit. Defaults to 20; use 0 to skip; maximum 200."),
		"expand":        booleanSchema("Enable generic token-based query expansion."),
	}, []string{"q"})
}

func deepInvestigationSchema() map[string]any {
	schema := investigationSchema()
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		return schema
	}
	properties["followUpLimit"] = integerSchema("Recommended follow-up steps to execute after the base investigation. Defaults to 3; use 0 to disable; maximum 5.")
	properties["compact"] = booleanSchema("For investigate_deep, return the compact investigate_summary payload instead of the full deep JSON.")
	properties["semantic"] = booleanSchema("For investigate_summary or investigate_deep compact mode, include local semantic/RAG candidates. Defaults to false.")
	properties["semanticLimit"] = integerSchema("Semantic candidate limit. Defaults to 8; maximum 50.")
	properties["semanticScanLimit"] = integerSchema("Maximum stored embeddings to scan linearly. Defaults to 0, which scans the whole index. Set a positive value only to deliberately cut the scan short.")
	properties["semanticKind"] = stringSchema("Optional semantic chunk kind filter, for example bsl_procedure, xml_object, or text_document. By default all kinds are searched.")
	properties["semanticModel"] = stringSchema("Optional embedding model override. Defaults to selected config embeddingModel.")
	return schema
}

func investigationWithDataSchema() map[string]any {
	schema := deepInvestigationSchema()
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		return schema
	}
	properties["dataChecks"] = map[string]any{
		"type":        "array",
		"description": "Narrow live-data checks derived from the code investigation. Do not request personal fields prohibited by the MCP policy.",
		"minItems":    1,
		"maxItems":    5,
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":       stringSchema("Short check name."),
				"purpose":    stringSchema("Hypothesis this query confirms or refutes."),
				"requestId":  stringSchema("Optional correlation identifier."),
				"queryText":  stringSchema("Read-only 1C query-language text with explicit fields; wildcard selection is prohibited."),
				"parameters": map[string]any{"type": "object", "additionalProperties": true},
				"maxRows":    integerSchema("Maximum rows for this check. Keep it narrow."),
			},
			"required": []string{"name", "purpose", "queryText"}, "additionalProperties": false,
		},
	}
	return schema
}

func objectSchema(properties map[string]any, required []string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	if required == nil {
		required = []string{}
	}
	return map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
}

func stringSchema(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func integerSchema(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}

func booleanSchema(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}

func enumSchema(values []string, description string) map[string]any {
	return map[string]any{"type": "string", "enum": values, "description": description}
}

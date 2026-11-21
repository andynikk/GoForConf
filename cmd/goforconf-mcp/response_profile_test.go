package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestResponseProfileHTTPRequests(t *testing.T) {
	for _, tc := range []struct {
		name, tool, path string
		args             map[string]any
		query            map[string]string
		absent           []string
	}{
		{"search", "search_code_fts", "/code/fts-search", map[string]any{"q": "ПриЗаписи"}, map[string]string{"limit": "5", "q": "ПриЗаписи"}, nil},
		{"full", "search_code_fts", "/code/fts-search", map[string]any{"q": "ПриЗаписи", "responseProfile": "full"}, nil, []string{"limit"}},
		{"explicit limit", "search_code_fts", "/code/fts-search", map[string]any{"q": "x", "limit": 40}, map[string]string{"limit": "40"}, nil},
		{"jira", "get_jira_issue", "/jira/issue", map[string]any{"key": "IC-123"}, map[string]string{"commentsLimit": "20"}, nil},
		{"deep", "investigate_deep", "/analysis/investigate-summary", map[string]any{"q": "x"}, map[string]string{"depth": "2", "limit": "5", "codeLimit": "5", "evidenceLimit": "3", "traceLimit": "5", "nextHopLimit": "0", "relationLimit": "0", "followUpLimit": "0"}, nil},
		{"explicit false and zero", "investigate_deep", "/analysis/investigate-deep", map[string]any{"q": "x", "compact": false, "depth": 0, "followUpLimit": 2}, map[string]string{"depth": "0", "followUpLimit": "2"}, nil},
		{"full deep", "investigate_deep", "/analysis/investigate-deep", map[string]any{"q": "x", "responseProfile": "full"}, nil, []string{"depth", "limit", "followUpLimit"}},
		{"semantic scan unaffected", "semantic_search", "/semantic/search", map[string]any{"q": "x"}, map[string]string{"limit": "5"}, []string{"scanLimit"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, _ := json.Marshal(tc.args)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path {
					t.Errorf("path %s, want %s", r.URL.Path, tc.path)
				}
				for key, want := range tc.query {
					if got := r.URL.Query().Get(key); got != want {
						t.Errorf("%s=%q, want %q", key, got, want)
					}
				}
				for _, key := range append(tc.absent, "responseProfile") {
					if r.URL.Query().Has(key) {
						t.Errorf("unexpected %s", key)
					}
				}
				fmt.Fprint(w, "{\n  \"ok\": true\n}")
			}))
			defer server.Close()
			result, err := callTool(server.Client(), server.URL, 1000, toolCallParams{Name: tc.tool, Arguments: tc.args})
			if err != nil {
				t.Fatal(err)
			}
			if result.Content[0].Text != `{"ok":true}` {
				t.Fatalf("not compact: %s", result.Content[0].Text)
			}
			after, _ := json.Marshal(tc.args)
			if string(before) != string(after) {
				t.Fatal("caller arguments mutated")
			}
		})
	}
}

func TestCompactJSONPreservesValuesAndChecksPackedSize(t *testing.T) {
	body := []byte("{\n  \"id\": 9007199254740993,\n  \"source\": \"Строка  с пробелами\\n\\tСледующая\"\n}")
	want := `{"id":9007199254740993,"source":"Строка  с пробелами\n\tСледующая"}`
	got, err := formatToolResponse("get_procedure_source", nil, body, len(want))
	if err != nil || got != want {
		t.Fatalf("got %q, %v", got, err)
	}
	got, err = formatToolResponse("get_procedure_source", nil, body, len(want)-1)
	if err != nil || !strings.Contains(got, "mcp_response_too_large") {
		t.Fatalf("size guard lost: %s %v", got, err)
	}
	if !json.Valid([]byte(got)) {
		t.Fatal("size error is invalid JSON")
	}
}

func TestProcedurePaginationAndSavings(t *testing.T) {
	for _, tool := range []string{"get_object_modules", "get_object_forms"} {
		t.Run(tool, func(t *testing.T) {
			key := "modules"
			if tool == "get_object_forms" {
				key = "forms"
			}
			procedures := make([]any, 30)
			for i := range procedures {
				procedures[i] = map[string]any{"id": i + 1, "name": fmt.Sprintf("Процедура%d", i+1), "module": "Document.Test.Module", "startLine": i*10 + 1, "endLine": i*10 + 9, "signatureText": "Процедура Обработчик(Параметр) Экспорт", "isExport": true}
			}
			body, _ := json.MarshalIndent(map[string]any{"object": map[string]any{"name": "Test"}, key: []any{map[string]any{"name": "Module", "procedures": procedures}}}, "", "  ")
			seen := map[int]bool{}
			for offset := 0; offset < 30; offset += 5 {
				params, _ := applyResponseProfile(toolCallParams{Name: tool, Arguments: map[string]any{"procedureOffset": offset}})
				got, err := formatToolResponse(tool, params.Arguments, body, 1000000)
				if err != nil {
					t.Fatal(err)
				}
				var decoded map[string]any
				if err := json.Unmarshal([]byte(got), &decoded); err != nil {
					t.Fatal(err)
				}
				item := decoded[key].([]any)[0].(map[string]any)
				if item["procedureCount"] != float64(30) || item["hasMore"] != (offset < 25) {
					t.Fatalf("incorrect counts: %v", item)
				}
				if offset < 25 && item["nextProcedureOffset"] != float64(offset+5) {
					t.Fatalf("incorrect cursor: %v", item)
				}
				if offset == 25 && item["nextProcedureOffset"] != nil {
					t.Fatal("last page has cursor")
				}
				for _, raw := range item["procedures"].([]any) {
					id := int(raw.(map[string]any)["id"].(float64))
					if seen[id] {
						t.Fatal("duplicate ID")
					}
					seen[id] = true
				}
				if offset == 0 {
					full, _ := formatToolResponse(tool, nil, body, 1000000)
					t.Logf("fixture bytes: original=%d, minified full=%d, economy first page=%d", len(body), len(full), len(got))
					if len(got) >= len(full) {
						t.Fatal("economy did not shrink result")
					}
				}
			}
			if len(seen) != 30 {
				t.Fatalf("lost procedures: %d", len(seen))
			}
			for _, args := range []map[string]any{{"compact": true, "procedureLimit": 0}, {"compact": true, "procedureOffset": 100, "procedureLimit": 5}} {
				got, err := formatToolResponse(tool, args, body, 1000000)
				if err != nil || !json.Valid([]byte(got)) {
					t.Fatalf("empty page: %q %v", got, err)
				}
			}
		})
	}
}

func TestResponseProfileDataRequest(t *testing.T) {
	for _, tc := range []struct {
		tool, field string
		args        map[string]any
	}{
		{"execute_data_query", "limit", map[string]any{"source": "Справочник.Организации", "select": []any{"Наименование"}}},
		{"execute_data_query_text", "maxRows", map[string]any{"queryText": "ВЫБРАТЬ Наименование ИЗ Справочник.Организации"}},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if request[tc.field] != float64(20) {
					t.Errorf("missing row limit: %v", request)
				}
				if _, ok := request["responseProfile"]; ok {
					t.Error("profile leaked upstream")
				}
				fmt.Fprint(w, `{"columns":[{"name":"Наименование"}],"rows":[]}`)
			}))
			defer server.Close()
			_, err := callTool(server.Client(), server.URL, 10000, toolCallParams{Name: tc.tool, Arguments: tc.args})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestResponseProfileConfigAndSchemas(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"httpAddr":"127.0.0.1:8088","mcpResponseProfile":"full"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOFORCONF_CONFIG", configPath)
	t.Setenv("GOFORCONF_MCP_RESPONSE_PROFILE", "")
	t.Setenv("GOFORCONF_MCP_MAX_RESPONSE_BYTES", "")
	cfg, err := loadHTTPConfig()
	if err != nil || cfg.ResponseProfile != "full" {
		t.Fatalf("config: %+v %v", cfg, err)
	}
	t.Setenv("GOFORCONF_MCP_RESPONSE_PROFILE", "economy")
	cfg, err = loadHTTPConfig()
	if err != nil || cfg.ResponseProfile != "economy" {
		t.Fatalf("env: %+v %v", cfg, err)
	}
	t.Setenv("GOFORCONF_MCP_RESPONSE_PROFILE", "typo")
	if _, err := loadHTTPConfig(); err == nil {
		t.Fatal("invalid profile accepted")
	}
	if p, err := normalizeResponseProfile(""); err != nil || p != "economy" {
		t.Fatal("wrong default")
	}
	for _, bad := range []any{false, 1, nil, "typo"} {
		if _, err := applyResponseProfile(toolCallParams{Arguments: map[string]any{"responseProfile": bad}}); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	for _, tool := range tools() {
		properties := tool.InputSchema["properties"].(map[string]any)
		for name, want := range economyDefaults(tool.Name) {
			if name == "maxRows" && tool.Name == "investigate_with_data" {
				continue
			}
			property, ok := properties[name].(map[string]any)
			if !ok || !reflect.DeepEqual(property["default"], want) {
				t.Errorf("%s.%s schema does not match runtime", tool.Name, name)
			}
		}
	}
}

func TestRPCProfilePrecedence(t *testing.T) {
	for _, tc := range []struct{ configured, arguments, want string }{
		{"full", `{"q":"x"}`, ""},
		{"economy", `{"q":"x"}`, "5"},
		{"economy", `{"q":"x","responseProfile":"full"}`, ""},
		{"full", `{"q":"x","responseProfile":"economy","limit":9}`, "9"},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("limit") != tc.want {
				t.Errorf("profile %s: query %s, want limit=%s", tc.configured, r.URL.RawQuery, tc.want)
			}
			fmt.Fprint(w, `{"items":[]}`)
		}))
		request := rpcRequest{ID: 1, Method: "tools/call", Params: json.RawMessage(`{"name":"search_code_fts","arguments":` + tc.arguments + `}`)}
		response, respond := handleRPC(server.Client(), server.URL, 10000, request, tc.configured)
		server.Close()
		if !respond || response.Error != nil {
			t.Fatalf("RPC: %+v", response)
		}
		catalog, err := dispatchRPC(nil, "", 0, rpcRequest{Method: "tools/list"}, tc.configured)
		if err != nil {
			t.Fatal(err)
		}
		for _, tool := range catalog.(map[string]any)["tools"].([]toolDef) {
			if tool.Name != "search_code_fts" {
				continue
			}
			properties := tool.InputSchema["properties"].(map[string]any)
			if properties["responseProfile"].(map[string]any)["default"] != tc.configured {
				t.Fatal("catalog profile mismatch")
			}
			if tc.configured == "full" && properties["limit"].(map[string]any)["default"] != nil {
				t.Fatal("economy default leaked to full schema")
			}
		}
	}
}

func TestEconomyNestedDataChecks(t *testing.T) {
	queryCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/analysis/investigate-summary" {
			fmt.Fprint(w, `{"facts":[]}`)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		want := float64(20)
		if queryCount == 1 {
			want = 1
		}
		if body["maxRows"] != want {
			t.Errorf("check %d: %v", queryCount, body)
		}
		queryCount++
		fmt.Fprint(w, `{"columns":[{"name":"Код"}],"rows":[]}`)
	}))
	defer server.Close()
	_, err := callTool(server.Client(), server.URL, 10000, toolCallParams{Name: "investigate_with_data", Arguments: map[string]any{
		"q": "x", "dataChecks": []any{
			map[string]any{"name": "default", "queryText": "ВЫБРАТЬ Код ИЗ Справочник.Организации"},
			map[string]any{"name": "explicit", "queryText": "ВЫБРАТЬ Код ИЗ Справочник.Организации", "maxRows": 1},
			map[string]any{"name": "blocked", "queryText": "ВЫБРАТЬ ДатаРождения ИЗ Справочник.ФизическиеЛица"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if queryCount != 2 {
		t.Fatalf("expected two allowed checks, got %d", queryCount)
	}
}

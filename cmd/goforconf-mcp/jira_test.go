package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetJiraIssueToolCallsHTTPAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/jira/issue" || r.URL.Query().Get("key") != "IC-123" || r.URL.Query().Get("commentsLimit") != "25" {
			t.Errorf("unexpected request: %s", r.URL.String())
		}
		fmt.Fprint(w, `{"key":"IC-123","description":"Issue details"}`)
	}))
	defer server.Close()
	result, err := callTool(server.Client(), server.URL, 1_000_000, toolCallParams{
		Name: "get_jira_issue", Arguments: map[string]any{"key": "IC-123", "commentsLimit": 25},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 || !strings.Contains(result.Content[0].Text, "Issue details") {
		t.Fatalf("unexpected tool result: %+v", result)
	}
	found := false
	for _, tool := range tools() {
		if tool.Name == "get_jira_issue" {
			found = true
		}
	}
	if !found {
		t.Fatal("get_jira_issue is missing from tools/list")
	}
}

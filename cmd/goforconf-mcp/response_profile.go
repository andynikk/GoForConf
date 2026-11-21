package main

import (
	"fmt"
	"regexp"
	"strings"
)

func normalizeResponseProfile(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "", "economy":
		return "economy", nil
	case "full":
		return "full", nil
	default:
		return "", fmt.Errorf("responseProfile must be economy or full, got %q", value)
	}
}

// Only omitted arguments are defaulted. In particular, false and zero remain
// explicit choices, and full retains the existing HTTP service defaults.
func applyResponseProfile(params toolCallParams) (toolCallParams, error) {
	profile := "economy"
	if raw, exists := params.Arguments["responseProfile"]; exists {
		value, ok := raw.(string)
		if !ok {
			return params, fmt.Errorf("responseProfile must be a string")
		}
		var err error
		profile, err = normalizeResponseProfile(value)
		if err != nil {
			return params, err
		}
	}
	args := make(map[string]any, len(params.Arguments)+10)
	for name, value := range params.Arguments {
		if name != "responseProfile" {
			args[name] = value
		}
	}
	if profile == "economy" {
		for name, value := range economyDefaults(params.Name) {
			if args[name] == nil {
				args[name] = value
			}
		}
	}
	params.Arguments = args
	return params, nil
}

func economyDefaults(tool string) map[string]any {
	defaults := map[string]any{}
	switch tool {
	case "get_jira_issue":
		defaults["commentsLimit"] = 20
	case "get_object_modules", "get_object_forms":
		defaults["compact"] = true
		defaults["procedureLimit"] = 5
	case "search_code_fts", "search_xml_fts", "semantic_search",
		"search_syntax_helper", "get_syntax_member", "semantic_search_syntax_helper",
		"find_callers", "get_based_on", "get_basis_for", "get_document_relations", "get_object_xml_summary":
		defaults["limit"] = 5
	case "trace_procedure", "trace_callers", "get_object_entry_traces":
		defaults["depth"], defaults["limit"] = 2, 5
	case "analyze_action", "get_action_evidence", "investigate_action", "investigate_deep", "investigate_summary", "investigate_with_data", "analyze_create_based_on":
		defaults["depth"], defaults["limit"], defaults["codeLimit"] = 2, 5, 5
		if tool != "analyze_action" && tool != "analyze_create_based_on" {
			defaults["evidenceLimit"], defaults["traceLimit"], defaults["nextHopLimit"] = 3, 5, 0
		}
		if strings.HasPrefix(tool, "investigate_") {
			defaults["relationLimit"] = 0
		}
		if tool == "investigate_deep" || tool == "investigate_summary" || tool == "investigate_with_data" {
			defaults["followUpLimit"], defaults["semanticLimit"] = 0, 5
		}
		if tool == "investigate_deep" {
			defaults["compact"] = true
		}
		if tool == "investigate_with_data" {
			defaults["maxRows"] = 20
		}
	case "execute_data_query":
		defaults["limit"] = 20
	case "execute_data_query_text":
		defaults["maxRows"] = 20
	}
	return defaults
}

var defaultDescription = regexp.MustCompile(`Defaults to [^.]*\.`)

// Describe exactly the defaults applied by the wrapper, rather than advertising
// the larger HTTP defaults. The HTTP API itself is unchanged.
func decorateResponseSchemas(definitions []toolDef, profile string) {
	for i := range definitions {
		tool := &definitions[i]
		defaults := economyDefaults(tool.Name)
		if len(defaults) == 0 {
			continue
		}
		properties := tool.InputSchema["properties"].(map[string]any)
		properties["responseProfile"] = enumSchema([]string{"economy", "full"}, "full restores HTTP defaults; explicit arguments override either profile.")
		properties["responseProfile"].(map[string]any)["default"] = profile
		if profile != "economy" {
			continue
		}
		for name, value := range defaults {
			property, ok := properties[name].(map[string]any)
			if !ok {
				continue
			}
			property["default"] = value
			description, _ := property["description"].(string)
			property["description"] = strings.TrimSpace(defaultDescription.ReplaceAllString(description, "")) + fmt.Sprintf(" Economy default: %v.", value)
		}
		if tool.Name == "investigate_with_data" {
			checks := properties["dataChecks"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
			checks["maxRows"].(map[string]any)["default"] = 20
		}
	}
}

func procedurePage(total, offset, limit int) (int, int) {
	start := min(max(offset, 0), total)
	end := total
	if limit >= 0 {
		end = start + min(limit, total-start)
	}
	return start, end
}

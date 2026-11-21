package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

type dataFieldPolicy struct {
	alwaysForbidden map[string]string
	contextual      map[string]string
	personalSources []string
	allowedSources  []string
	mixedSources    []string
}

type dataAccessContext struct{ allowedResponseFields map[string]bool }

var activeDataFieldPolicy dataFieldPolicy

func loadForbiddenDataFields(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("mcpForbiddenFieldsPath is empty")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read data field policy %s: %w", path, err)
	}
	policy := dataFieldPolicy{alwaysForbidden: map[string]string{}, contextual: map[string]string{}}
	section := ""
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if strings.HasPrefix(line, "## ") {
			section = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "## ")))
			continue
		}
		if !strings.HasPrefix(line, "-") {
			continue
		}
		item := strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "-")), "`")
		if item == "" || strings.ContainsAny(item, " \t") {
			continue
		}
		switch section {
		case "безусловно запрещённые поля", "безусловно запрещенные поля":
			policy.alwaysForbidden[strings.ToLower(item)] = item
		case "контекстные поля":
			policy.contextual[strings.ToLower(item)] = item
		case "источники физических лиц":
			policy.personalSources = append(policy.personalSources, strings.ToLower(item))
		case "разрешённые источники организаций", "разрешенные источники организаций":
			policy.allowedSources = append(policy.allowedSources, strings.ToLower(item))
		case "смешанные источники":
			policy.mixedSources = append(policy.mixedSources, strings.ToLower(item))
		}
	}
	if len(policy.alwaysForbidden)+len(policy.contextual) == 0 {
		return fmt.Errorf("data field policy %s contains no field list items", path)
	}
	if len(policy.allowedSources) == 0 {
		return fmt.Errorf("data field policy %s contains no allowed organization sources", path)
	}
	activeDataFieldPolicy = policy
	return nil
}

func setForbiddenDataFields(fields []string) {
	policy := dataFieldPolicy{alwaysForbidden: map[string]string{}, contextual: map[string]string{}, allowedSources: []string{"test.allowed"}}
	for _, field := range fields {
		policy.alwaysForbidden[strings.ToLower(field)] = field
	}
	activeDataFieldPolicy = policy
}

var (
	wildcardSelectionPattern = regexp.MustCompile(`(?i)(^|[\s,])(?:[\p{L}_][\p{L}\p{N}_]*\s*\.\s*)?\*($|[\s,])`)
	aliasSourcePattern       = regexp.MustCompile(`(?i)(?:ИЗ|СОЕДИНЕНИЕ)\s+([\p{L}_][\p{L}\p{N}_.]*)\s+КАК\s+([\p{L}_][\p{L}\p{N}_]*)`)
	identifierPattern        = regexp.MustCompile(`[\p{L}_][\p{L}\p{N}_]*`)
)

func validateDataToolArguments(toolName string, args map[string]any) (dataAccessContext, error) {
	ctx := dataAccessContext{allowedResponseFields: map[string]bool{}}
	if toolName != "execute_data_query" && toolName != "execute_data_query_text" {
		return ctx, nil
	}
	if toolName == "execute_data_query_text" {
		query := fmt.Sprint(args["queryText"])
		if wildcardSelectionPattern.MatchString(query) {
			return ctx, fmt.Errorf("data_access_denied: wildcard field selection is prohibited")
		}
		if err := validateExpression(query, queryAliases(query), &ctx); err != nil {
			return ctx, err
		}
		return ctx, nil
	}
	source := strings.ToLower(strings.TrimSpace(fmt.Sprint(args["source"])))
	aliases := map[string]string{}
	if source != "" {
		parts := strings.Split(source, ".")
		aliases[parts[len(parts)-1]] = source
	}
	var expressions []string
	collectQueryExpressions(args, &expressions, "")
	for _, expression := range expressions {
		if wildcardSelectionPattern.MatchString(expression) {
			return ctx, fmt.Errorf("data_access_denied: wildcard field selection is prohibited")
		}
		if err := validateExpressionForDefaultSource(expression, aliases, source, &ctx); err != nil {
			return ctx, err
		}
	}
	return ctx, nil
}

func queryAliases(query string) map[string]string {
	result := map[string]string{}
	for _, match := range aliasSourcePattern.FindAllStringSubmatch(query, -1) {
		alias := strings.ToLower(match[2])
		source := strings.ToLower(match[1])
		if previous, exists := result[alias]; exists && previous != source {
			result[alias] = "" // Same alias in different scopes is ambiguous: fail closed.
		} else {
			result[alias] = source
		}
	}
	return result
}

func validateExpression(text string, aliases map[string]string, ctx *dataAccessContext) error {
	return validateTokens(text, func(tokenStart int) (bool, string) {
		root := qualifierRoot(text, tokenStart)
		if root == "" {
			if source, ok := singleAliasSource(aliases); ok {
				return sourceAllowed(source), source
			}
			return false, "unqualified field with multiple or unknown sources"
		}
		source := aliases[strings.ToLower(root)]
		if source == "" {
			return false, "unknown source alias " + root
		}
		return sourceAllowed(source), source
	}, ctx)
}

func singleAliasSource(aliases map[string]string) (string, bool) {
	unique := map[string]bool{}
	for _, source := range aliases {
		unique[source] = true
	}
	if len(unique) != 1 {
		return "", false
	}
	for source := range unique {
		return source, true
	}
	return "", false
}

func validateExpressionForDefaultSource(text string, aliases map[string]string, defaultSource string, ctx *dataAccessContext) error {
	return validateTokens(text, func(tokenStart int) (bool, string) {
		root := qualifierRoot(text, tokenStart)
		if root == "" {
			return sourceAllowed(defaultSource), defaultSource
		}
		source := aliases[strings.ToLower(root)]
		if source == "" {
			return false, "unknown source alias " + root
		}
		return sourceAllowed(source), source
	}, ctx)
}

func validateTokens(text string, contextualDecision func(int) (bool, string), ctx *dataAccessContext) error {
	for _, location := range identifierPattern.FindAllStringIndex(text, -1) {
		token := text[location[0]:location[1]]
		lower := strings.ToLower(token)
		if canonical, ok := activeDataFieldPolicy.alwaysForbidden[lower]; ok {
			return fmt.Errorf("data_access_denied: forbidden field: %s", canonical)
		}
		canonical, contextual := activeDataFieldPolicy.contextual[lower]
		if !contextual {
			continue
		}
		allowed, source := contextualDecision(location[0])
		if !allowed {
			return fmt.Errorf("data_access_denied: contextual field %s is not allowed for %s", canonical, source)
		}
		ctx.allowedResponseFields[lower] = true
	}
	return nil
}

func qualifierRoot(text string, tokenStart int) string {
	position := tokenStart - 1
	for position >= 0 && unicode.IsSpace(rune(text[position])) {
		position--
	}
	if position < 0 || text[position] != '.' {
		return ""
	}
	prefix := strings.TrimSpace(text[:position])
	matches := identifierPattern.FindAllStringIndex(prefix, -1)
	if len(matches) == 0 {
		return ""
	}
	last := matches[len(matches)-1]
	root := prefix[last[0]:last[1]]
	start := last[0]
	for start > 0 {
		before := strings.TrimSpace(prefix[:start])
		if !strings.HasSuffix(before, ".") {
			break
		}
		before = strings.TrimSpace(strings.TrimSuffix(before, "."))
		previous := identifierPattern.FindAllStringIndex(before, -1)
		if len(previous) == 0 {
			break
		}
		item := previous[len(previous)-1]
		root = before[item[0]:item[1]]
		prefix = before
		start = item[0]
	}
	return root
}

func sourceAllowed(source string) bool {
	source = strings.ToLower(strings.TrimSpace(source))
	for _, allowed := range activeDataFieldPolicy.allowedSources {
		if source == allowed || strings.HasPrefix(source, allowed+".") {
			return true
		}
	}
	return false
}

func collectQueryExpressions(value any, target *[]string, key string) {
	switch typed := value.(type) {
	case map[string]any:
		for childKey, child := range typed {
			lowerKey := strings.ToLower(childKey)
			if lowerKey == "parameters" || lowerKey == "value" || lowerKey == "requestid" || lowerKey == "source" {
				continue
			}
			collectQueryExpressions(child, target, childKey)
		}
	case []any:
		for _, child := range typed {
			collectQueryExpressions(child, target, key)
		}
	case string:
		*target = append(*target, typed)
	}
}

func validateDataToolResponse(toolName string, body []byte, ctx dataAccessContext) error {
	if toolName != "execute_data_query" && toolName != "execute_data_query_text" {
		return nil
	}
	var payload struct {
		Columns []struct {
			Name string `json:"name"`
		} `json:"columns"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil
	}
	var denied []string
	for _, column := range payload.Columns {
		for _, token := range identifierPattern.FindAllString(column.Name, -1) {
			lower := strings.ToLower(token)
			if canonical, ok := activeDataFieldPolicy.alwaysForbidden[lower]; ok {
				denied = append(denied, canonical)
			}
			if canonical, ok := activeDataFieldPolicy.contextual[lower]; ok && !ctx.allowedResponseFields[lower] {
				denied = append(denied, canonical)
			}
		}
	}
	if len(denied) > 0 {
		sort.Strings(denied)
		return fmt.Errorf("data_access_denied: response contains forbidden field(s): %s", strings.Join(denied, ", "))
	}
	return nil
}

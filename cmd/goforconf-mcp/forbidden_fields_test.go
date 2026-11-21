package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func init() {
	setTestDataPolicy()
}

func setTestDataPolicy() {
	activeDataFieldPolicy = dataFieldPolicy{
		alwaysForbidden: map[string]string{"датарождения": "ДатаРождения"},
		contextual:      map[string]string{"email": "Email", "адресэлектроннойпочты": "АдресЭлектроннойПочты", "инн": "ИНН", "телефон": "Телефон"},
		personalSources: []string{"справочник.физическиелица"},
		allowedSources:  []string{"справочник.организации"},
		mixedSources:    []string{"справочник.контрагенты"},
	}
}

func TestLoadForbiddenDataFieldsFromMarkdown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fields.md")
	policy := "# Policy\n\n## Безусловно запрещённые поля\n\n- `ДатаРождения`\n\n## Контекстные поля\n\n- Email\n\n## Разрешённые источники организаций\n\n- Справочник.Организации\n"
	if err := os.WriteFile(path, []byte(policy), 0600); err != nil {
		t.Fatal(err)
	}
	if err := loadForbiddenDataFields(path); err != nil {
		t.Fatal(err)
	}
	if activeDataFieldPolicy.alwaysForbidden["датарождения"] != "ДатаРождения" || activeDataFieldPolicy.contextual["email"] != "Email" {
		t.Fatalf("unexpected loaded policy: %#v", activeDataFieldPolicy)
	}
	setTestDataPolicy()
}

func TestForbiddenDataFieldsPolicyFailsClosed(t *testing.T) {
	if err := loadForbiddenDataFields(filepath.Join(t.TempDir(), "missing.md")); err == nil {
		t.Fatal("missing policy file was accepted")
	}
	emptyPath := filepath.Join(t.TempDir(), "empty.md")
	if err := os.WriteFile(emptyPath, []byte("# No fields\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := loadForbiddenDataFields(emptyPath); err == nil {
		t.Fatal("empty policy file was accepted")
	}
}

func TestForbiddenDataFieldInTextQuery(t *testing.T) {
	args := map[string]any{"queryText": "ВЫБРАТЬ ФизЛица.ДатаРождения ИЗ Справочник.ФизическиеЛица КАК ФизЛица"}
	if _, err := validateDataToolArguments("execute_data_query_text", args); err == nil {
		t.Fatal("forbidden field was accepted")
	}
}

func TestForbiddenDataFieldCaseInsensitive(t *testing.T) {
	args := map[string]any{"queryText": "ВЫБРАТЬ Пользователи.email ИЗ Справочник.Пользователи КАК Пользователи"}
	if _, err := validateDataToolArguments("execute_data_query_text", args); err == nil {
		t.Fatal("case-insensitive forbidden field was accepted")
	}
}

func TestWildcardSelectionIsForbidden(t *testing.T) {
	for _, query := range []string{
		"ВЫБРАТЬ * ИЗ Справочник.Номенклатура",
		"ВЫБРАТЬ Номенклатура.* ИЗ Справочник.Номенклатура КАК Номенклатура",
	} {
		if _, err := validateDataToolArguments("execute_data_query_text", map[string]any{"queryText": query}); err == nil {
			t.Fatalf("wildcard query was accepted: %s", query)
		}
	}
}

func TestAllowedTextQuery(t *testing.T) {
	args := map[string]any{"queryText": "ВЫБРАТЬ Номенклатура.Код, Номенклатура.Наименование ИЗ Справочник.Номенклатура КАК Номенклатура"}
	if _, err := validateDataToolArguments("execute_data_query_text", args); err != nil {
		t.Fatalf("allowed query rejected: %v", err)
	}
}

func TestParametersAreNotTreatedAsFieldNames(t *testing.T) {
	args := map[string]any{
		"queryText":  "ВЫБРАТЬ Номенклатура.Код ИЗ Справочник.Номенклатура КАК Номенклатура ГДЕ Номенклатура.Код = &Код",
		"parameters": map[string]any{"Код": "Телефон"},
	}
	if _, err := validateDataToolArguments("execute_data_query_text", args); err != nil {
		t.Fatalf("parameter value caused false positive: %v", err)
	}
}

func TestForbiddenResponseColumn(t *testing.T) {
	body := []byte(`{"columns":[{"name":"Код"},{"name":"АдресЭлектроннойПочты"}],"rows":[]}`)
	if err := validateDataToolResponse("execute_data_query_text", body, dataAccessContext{allowedResponseFields: map[string]bool{}}); err == nil {
		t.Fatal("forbidden response column was accepted")
	}
}

func TestContextualFieldAllowedForOrganization(t *testing.T) {
	args := map[string]any{"queryText": "ВЫБРАТЬ Организации.ИНН, Организации.Телефон ИЗ Справочник.Организации КАК Организации"}
	ctx, err := validateDataToolArguments("execute_data_query_text", args)
	if err != nil {
		t.Fatalf("organization fields rejected: %v", err)
	}
	body := []byte(`{"columns":[{"name":"ИНН"},{"name":"Телефон"}],"rows":[]}`)
	if err := validateDataToolResponse("execute_data_query_text", body, ctx); err != nil {
		t.Fatalf("organization response rejected: %v", err)
	}
}

func TestContextualFieldDeniedForPersonAndMixedSource(t *testing.T) {
	queries := []string{
		"ВЫБРАТЬ ФизическиеЛица.Email ИЗ Справочник.ФизическиеЛица КАК ФизическиеЛица",
		"ВЫБРАТЬ Контрагенты.ИНН ИЗ Справочник.Контрагенты КАК Контрагенты",
	}
	for _, query := range queries {
		if _, err := validateDataToolArguments("execute_data_query_text", map[string]any{"queryText": query}); err == nil {
			t.Fatalf("unsafe query accepted: %s", query)
		}
	}
}

func TestRepeatedAliasWithDifferentSourcesIsDenied(t *testing.T) {
	query := "ВЫБРАТЬ X.ИНН ИЗ Справочник.Организации КАК X СОЕДИНЕНИЕ Справочник.ФизическиеЛица КАК X ПО ИСТИНА"
	if _, err := validateDataToolArguments("execute_data_query_text", map[string]any{"queryText": query}); err == nil {
		t.Fatal("ambiguous repeated alias was accepted")
	}
}

func TestInvestigationWithDataCombinesCodeAndRuntimeEvidence(t *testing.T) {
	setTestDataPolicy()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/analysis/investigate-summary":
			_, _ = w.Write([]byte(`{"facts":["code fact"]}`))
		case "/data/query/text":
			_, _ = w.Write([]byte(`{"columns":[{"name":"Код"}],"rows":[["001"]],"rowCount":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	result, err := callInvestigationWithData(server.Client(), server.URL, 100000, toolCallParams{
		Name: "investigate_with_data",
		Arguments: map[string]any{
			"q": "check runtime state",
			"dataChecks": []any{map[string]any{
				"name": "link", "purpose": "confirm relation",
				"queryText": "ВЫБРАТЬ Справочник.Код КАК Код ИЗ Справочник.Организации КАК Справочник",
				"maxRows":   1,
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("unexpected combined result: %#v", result)
	}
	var combined struct {
		Investigation struct {
			Facts []string `json:"facts"`
		} `json:"investigation"`
		DataChecks []struct {
			Status string `json:"status"`
		} `json:"dataChecks"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &combined); err != nil {
		t.Fatal(err)
	}
	if len(combined.DataChecks) != 1 || combined.DataChecks[0].Status != "ok" || len(combined.Investigation.Facts) != 1 || combined.Investigation.Facts[0] != "code fact" {
		t.Fatalf("unexpected combined result: %#v", result)
	}
}

func TestInvestigationWithDataReportsBlockedCheck(t *testing.T) {
	setTestDataPolicy()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/analysis/investigate-summary" {
			_, _ = w.Write([]byte(`{"facts":[]}`))
			return
		}
		t.Fatalf("blocked check reached data endpoint")
	}))
	defer server.Close()
	result, err := callInvestigationWithData(server.Client(), server.URL, 100000, toolCallParams{
		Name: "investigate_with_data",
		Arguments: map[string]any{
			"q": "check runtime state",
			"dataChecks": []any{map[string]any{
				"name": "blocked", "purpose": "must not be read",
				"queryText": "ВЫБРАТЬ Люди.ДатаРождения ИЗ Справочник.ФизическиеЛица КАК Люди",
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var combined struct {
		DataChecks []struct {
			Status string `json:"status"`
		} `json:"dataChecks"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &combined); err != nil {
		t.Fatal(err)
	}
	if len(combined.DataChecks) != 1 || combined.DataChecks[0].Status != "blocked" {
		t.Fatalf("blocked check was not reported: %s", result.Content[0].Text)
	}
}

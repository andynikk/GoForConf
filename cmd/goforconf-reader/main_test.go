package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenReaderDBReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot #1?.sqlite")
	// Windows forbids '?' in a path; spaces and '#' still exercise URI escaping.
	if os.PathSeparator == '\\' {
		path = filepath.Join(filepath.Dir(path), "snapshot #1.sqlite")
	}
	seed, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = seed.Exec("CREATE TABLE sample (value TEXT); INSERT INTO sample VALUES ('present')"); err != nil {
		t.Fatal(err)
	}
	if err = seed.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := openReaderDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var value string
	if err = db.QueryRow("SELECT value FROM sample").Scan(&value); err != nil || value != "present" {
		t.Fatalf("read: %q, %v", value, err)
	}
	if _, err = db.Exec("INSERT INTO sample VALUES ('unexpected')"); err == nil {
		t.Fatal("reader wrote to the snapshot")
	}
}
func TestOpenReaderDBMissingDoesNotCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.sqlite")
	db, err := openReaderDB(path)
	if db != nil {
		db.Close()
	}
	if err == nil {
		t.Fatal("expected an error for a missing database")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("database was created or stat failed: %v", err)
	}
}
func TestReaderDoesNotExposeIndexCommands(t *testing.T) {
	err := runReader([]string{"index-files"})
	if err == nil || !strings.Contains(err.Error(), "unknown reader command") {
		t.Fatalf("unexpected result: %v", err)
	}
}

func TestReaderServesProcedureSearchFromSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("CREATE TABLE procedures (id INTEGER, module_full_name TEXT, name TEXT, start_line INTEGER, end_line INTEGER, normalized_name TEXT, signature_text TEXT); INSERT INTO procedures VALUES (7, 'CommonModule.Test', 'FindMe', 2, 5, 'findme', 'FindMe()')")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = openReaderDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mux := http.NewServeMux()
	registerHTTPRoutes(mux, db)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest("GET", "/procedures/search?q=findme", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status %d: %s", response.Code, response.Body.String())
	}
	var result struct {
		Total int `json:"total"`
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	if err = json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || len(result.Items) != 1 || result.Items[0].Name != "FindMe" {
		t.Fatalf("wrong response: %s", response.Body.String())
	}
}

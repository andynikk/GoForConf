package main

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

func main() {
	if err := runReader(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func runReader(args []string) error {
	args, err := parseGlobalArgs(args)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return fmt.Errorf("usage: goforconf-reader [--config path] <serve|trace-procedure|find-callers|trace-callers|jira-issue|encrypt-data-password|protect-data-password>")
	}
	switch args[0] {
	case "serve":
		return serveHTTP(args[1:])
	case "trace-procedure":
		return traceProcedure(args[1:])
	case "find-callers":
		return findCallers(args[1:])
	case "trace-callers":
		return traceCallers(args[1:])
	case "jira-issue":
		return fetchJiraIssue(args[1:])
	case "encrypt-data-password":
		return encryptDataPassword(args[1:])
	case "protect-data-password":
		return protectDataPassword(activeConfigPath)
	default:
		return fmt.Errorf("unknown reader command %q", args[0])
	}
}

func openReaderDB(path string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	path = filepath.ToSlash(abs)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	u := url.URL{Scheme: "file", Path: path}
	query := u.Query()
	query.Set("mode", "ro")
	query.Add("_pragma", "busy_timeout(30000)")
	u.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	if err = db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

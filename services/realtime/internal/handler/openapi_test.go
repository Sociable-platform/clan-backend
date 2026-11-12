package handler

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// openAPIFile is this service's contract, relative to the repository root. The WebSocket is not
// an OpenAPI operation; it is described in openapi/realtime-ws.md.
const openAPIFile = "openapi/realtime.yaml"

// wsPath is the WebSocket endpoint: a route, but documented in realtime-ws.md, not in the OpenAPI file.
const wsPath = "GET /api/v1/ws"

// TestOpenAPIMatchesRoutes fails when a route is implemented but not documented,
// or documented but not implemented. Update openapi/realtime.yaml together with Routes().
func TestOpenAPIMatchesRoutes(t *testing.T) {
	documented := readOperations(t, filepath.Join("..", "..", "..", "..", openAPIFile))

	implemented := map[string]bool{}
	for _, r := range New("test", fakeService{}, WSOptions{Verifier: noVerifier{}, Players: noPlayers{}}, testLog()).Routes() {
		if op := strings.ToUpper(r.Method) + " " + r.Path; op != wsPath {
			implemented[op] = true
		}
	}

	var missingDocs, missingCode []string
	for op := range implemented {
		if !documented[op] {
			missingDocs = append(missingDocs, op)
		}
	}
	for op := range documented {
		if !implemented[op] {
			missingCode = append(missingCode, op)
		}
	}
	sort.Strings(missingDocs)
	sort.Strings(missingCode)
	if len(missingDocs) > 0 {
		t.Errorf("implemented but missing from %s: %v", openAPIFile, missingDocs)
	}
	if len(missingCode) > 0 {
		t.Errorf("documented in %s but not implemented: %v", openAPIFile, missingCode)
	}
}

var (
	pathLine   = regexp.MustCompile(`^  (/[^:\s]*):\s*$`)
	methodLine = regexp.MustCompile(`^    (get|post|put|patch|delete|head|options):\s*$`)
)

// readOperations reads "METHOD /path" pairs from the paths section of our OpenAPI files.
// It relies on the files' layout: paths indented by 2 spaces, methods by 4.
func readOperations(t *testing.T, file string) map[string]bool {
	t.Helper()
	f, err := os.Open(file)
	if err != nil {
		t.Fatalf("open %s: %v", file, err)
	}
	defer f.Close()

	ops := map[string]bool{}
	inPaths, current := false, ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "paths:":
			inPaths = true
		case inPaths && line != "" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "#"):
			inPaths = false // next top-level key, e.g. components:
		case inPaths && pathLine.MatchString(line):
			current = pathLine.FindStringSubmatch(line)[1]
		case inPaths && current != "" && methodLine.MatchString(line):
			ops[strings.ToUpper(methodLine.FindStringSubmatch(line)[1])+" "+current] = true
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(ops) == 0 {
		t.Fatalf("no operations found in %s", file)
	}
	return ops
}

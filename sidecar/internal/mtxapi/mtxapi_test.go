package mtxapi

import (
	"os"
	"strings"
	"testing"
)

const specPath = "../../../spec/mediamtx/openapi.yaml"

// The embedded table must cover the vendored spec exactly: a MediaMTX bump that adds, removes or moves an
// operation fails here until operations.yaml records a decision for it.
func TestTableCoversVendoredSpec(t *testing.T) {
	table, err := Operations()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := ParseSpec(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(spec) == 0 {
		t.Fatal("no operations parsed from the vendored spec")
	}
	for _, p := range Compare(table, spec) {
		t.Error(p)
	}
}

func TestParseSpec(t *testing.T) {
	ops, err := ParseSpec([]byte(`
openapi: 3.0.0
paths:
  /v3/a/{id}:
    parameters: [{name: id, in: path}, {name: shared, in: query}]
    get:
      operationId: aGet
      summary: gets a.
      parameters:
        - name: page
          in: query
        - name: X-Trace
          in: header
    post:
      operationId: aKick
  /v3/b:
    delete:
      operationId: bDelete
`))
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, o := range ops {
		got = append(got, o.Method+" "+o.Path+" "+o.ID+" "+o.Summary+" "+strings.Join(o.Query, ","))
	}
	want := []string{"GET /v3/a/{id} aGet gets a. shared,page", "POST /v3/a/{id} aKick  shared", "DELETE /v3/b bDelete  "}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q, want %q", got, want)
	}

	if _, err := ParseSpec([]byte("paths:\n  /v3/x:\n    get: {summary: no id}\n")); err == nil {
		t.Error("operation without operationId: want an error")
	}
	if _, err := ParseSpec([]byte("openapi: 3.0.0\n")); err == nil {
		t.Error("document without paths: want an error")
	}
}

func TestCompare(t *testing.T) {
	spec := []SpecOperation{
		{ID: "same", Method: "GET", Path: "/v3/same", Query: []string{"page", "itemsPerPage"}},
		{ID: "moved", Method: "GET", Path: "/v3/new"},
		{ID: "added", Method: "POST", Path: "/v3/added"},
		{ID: "newParam", Method: "GET", Path: "/v3/list", Query: []string{"page", "filter"}},
	}
	table := []Operation{
		{ID: "same", Method: "GET", Path: "/v3/same", Query: []string{"itemsPerPage", "page"}},
		{ID: "moved", Method: "GET", Path: "/v3/old"},
		{ID: "removed", Method: "GET", Path: "/v3/removed"},
		{ID: "newParam", Method: "GET", Path: "/v3/list", Query: []string{"page"}},
	}
	got := strings.Join(Compare(table, spec), "\n")
	for _, want := range []string{
		"moved is GET /v3/new in the spec but GET /v3/old in operations.yaml",
		"added (POST /v3/added) is in the spec but not in operations.yaml",
		"removed (GET /v3/removed) is in operations.yaml but not in the spec",
		"newParam declares query parameters [page filter] in the spec but [page] in operations.yaml",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing problem %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "same") {
		t.Errorf("reported a matching operation:\n%s", got)
	}
}

func TestParseTableRejects(t *testing.T) {
	tests := map[string]string{
		"unknown field":       "operations:\n  - {id: a, method: GET, path: /v3/a, area: X, phase: 2, access: viewer, extra: 1}\n",
		"duplicate id":        "operations:\n  - {id: a, method: GET, path: /v3/a, area: X, phase: 2, access: viewer}\n  - {id: a, method: GET, path: /v3/b, area: X, phase: 2, access: viewer}\n",
		"bad method":          "operations:\n  - {id: a, method: get, path: /v3/a, area: X, phase: 2, access: viewer}\n",
		"bad path":            "operations:\n  - {id: a, method: GET, path: /v2/a, area: X, phase: 2, access: viewer}\n",
		"bad access":          "operations:\n  - {id: a, method: GET, path: /v3/a, area: X, phase: 2, access: root}\n",
		"used without phase":  "operations:\n  - {id: a, method: GET, path: /v3/a, area: X, access: operator}\n",
		"unused without note": "operations:\n  - {id: a, method: GET, path: /v3/a, access: unused}\n",
		"unused with a phase": "operations:\n  - {id: a, method: GET, path: /v3/a, phase: 3, access: unused, note: n}\n",
		"empty":               "operations: []\n",
	}
	for name, in := range tests {
		if _, err := ParseTable([]byte(in)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if _, err := ParseTable([]byte("operations:\n  - {id: a, method: GET, path: /v3/a, area: X, phase: 2, access: viewer}\n")); err != nil {
		t.Errorf("valid table: %v", err)
	}
}

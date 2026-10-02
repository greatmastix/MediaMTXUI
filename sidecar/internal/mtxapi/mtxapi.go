// Package mtxapi describes the MediaMTX Control API operations this project knows about. The vendored
// OpenAPI spec (spec/mediamtx/openapi.yaml) says what exists; operations.yaml records how each operation
// is used: UI area, phase and the minimum role the sidecar's API proxy requires.
package mtxapi

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"
)

//go:embed operations.yaml
var operationsYAML []byte

// Access is the minimum role allowed to call an operation through the sidecar.
type Access string

// Access levels, see operations.yaml.
const (
	AccessViewer   Access = "viewer"
	AccessOperator Access = "operator"
	AccessAdmin    Access = "admin"
	AccessSidecar  Access = "sidecar"
	AccessUnused   Access = "unused"
)

// Accesses lists the access levels in display order.
var Accesses = []Access{AccessViewer, AccessOperator, AccessAdmin, AccessSidecar, AccessUnused}

// Operation is one entry of operations.yaml.
type Operation struct {
	ID     string   `yaml:"id"`
	Method string   `yaml:"method"`
	Path   string   `yaml:"path"`
	Query  []string `yaml:"query"`  // declared query parameters; the proxy forwards only these
	Redact []string `yaml:"redact"` // response fields removed before the browser sees them
	Area   string   `yaml:"area"`
	Phase  int      `yaml:"phase"`
	Access Access   `yaml:"access"`
	Note   string   `yaml:"note"`
}

var (
	loadOnce sync.Once
	loaded   []Operation
	errLoad  error
)

// Operations returns the embedded operations table. A malformed table is a build defect, caught by tests.
func Operations() ([]Operation, error) {
	loadOnce.Do(func() { loaded, errLoad = ParseTable(operationsYAML) })
	return loaded, errLoad
}

var methods = []string{"GET", "POST", "PUT", "PATCH", "DELETE"}

// ParseTable decodes and validates an operations table.
func ParseTable(b []byte) ([]Operation, error) {
	var doc struct {
		Operations []Operation `yaml:"operations"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("operations table: %w", err)
	}
	var errs []error
	seen := map[string]bool{}
	for i, op := range doc.Operations {
		where := fmt.Sprintf("operations[%d] %q", i, op.ID)
		switch {
		case op.ID == "":
			errs = append(errs, fmt.Errorf("%s: missing id", where))
		case seen[op.ID]:
			errs = append(errs, fmt.Errorf("%s: duplicate id", where))
		}
		seen[op.ID] = true
		if !slices.Contains(methods, op.Method) {
			errs = append(errs, fmt.Errorf("%s: method %q is not one of %v", where, op.Method, methods))
		}
		if !strings.HasPrefix(op.Path, "/v3/") {
			errs = append(errs, fmt.Errorf("%s: path %q does not start with /v3/", where, op.Path))
		}
		for _, f := range append(slices.Clone(op.Query), op.Redact...) {
			if f == "" {
				errs = append(errs, fmt.Errorf("%s: empty query parameter or redact field", where))
			}
		}
		switch op.Access {
		case AccessUnused:
			if op.Phase != 0 || op.Area != "" || op.Note == "" {
				errs = append(errs, fmt.Errorf("%s: unused operations need a note and no area or phase", where))
			}
		case AccessViewer, AccessOperator, AccessAdmin, AccessSidecar:
			if op.Phase < 1 || op.Phase > 9 || op.Area == "" {
				errs = append(errs, fmt.Errorf("%s: needs an area and a phase between 1 and 9", where))
			}
		default:
			errs = append(errs, fmt.Errorf("%s: access %q is not one of %v", where, op.Access, Accesses))
		}
	}
	if len(doc.Operations) == 0 {
		errs = append(errs, errors.New("operations table is empty"))
	}
	return doc.Operations, errors.Join(errs...)
}

// SpecOperation is an operation declared in an OpenAPI document.
type SpecOperation struct {
	ID      string
	Method  string
	Path    string
	Summary string
	Query   []string // declared query parameters, in document order
}

// ParseSpec lists the operations of an OpenAPI 3 document in document order.
func ParseSpec(b []byte) ([]SpecOperation, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(b, &root); err != nil {
		return nil, fmt.Errorf("openapi spec: %w", err)
	}
	if len(root.Content) == 0 {
		return nil, errors.New("openapi spec: empty document")
	}
	paths := mapValue(root.Content[0], "paths")
	if paths == nil || paths.Kind != yaml.MappingNode {
		return nil, errors.New("openapi spec: no paths mapping")
	}
	var ops []SpecOperation
	for i := 0; i+1 < len(paths.Content); i += 2 {
		path, item := paths.Content[i].Value, paths.Content[i+1]
		for j := 0; j+1 < len(item.Content); j += 2 {
			method := strings.ToUpper(item.Content[j].Value)
			if !slices.Contains(methods, method) {
				continue // parameters, summary, servers, ...
			}
			op := item.Content[j+1]
			id := mapValue(op, "operationId")
			if id == nil || id.Value == "" {
				return nil, fmt.Errorf("openapi spec: %s %s has no operationId", method, path)
			}
			so := SpecOperation{ID: id.Value, Method: method, Path: path}
			if s := mapValue(op, "summary"); s != nil {
				so.Summary = s.Value
			}
			for _, params := range []*yaml.Node{mapValue(item, "parameters"), mapValue(op, "parameters")} {
				if params == nil {
					continue
				}
				for _, p := range params.Content {
					if in := mapValue(p, "in"); in != nil && in.Value == "query" {
						if name := mapValue(p, "name"); name != nil {
							so.Query = append(so.Query, name.Value)
						}
					}
				}
			}
			ops = append(ops, so)
		}
	}
	return ops, nil
}

func mapValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// Compare lists every disagreement between an operations table and a spec: operations the table lacks,
// table entries the spec does not have, and method or path mismatches. An empty result means the table
// covers the spec exactly.
func Compare(table []Operation, spec []SpecOperation) []string {
	var problems []string
	byID := map[string]Operation{}
	for _, op := range table {
		byID[op.ID] = op
	}
	inSpec := map[string]bool{}
	for _, so := range spec {
		inSpec[so.ID] = true
		op, ok := byID[so.ID]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("%s (%s %s) is in the spec but not in operations.yaml", so.ID, so.Method, so.Path))
		case op.Method != so.Method || op.Path != so.Path:
			problems = append(problems, fmt.Sprintf("%s is %s %s in the spec but %s %s in operations.yaml", so.ID, so.Method, so.Path, op.Method, op.Path))
		case !sameSet(op.Query, so.Query):
			problems = append(problems, fmt.Sprintf("%s declares query parameters %v in the spec but %v in operations.yaml", so.ID, so.Query, op.Query))
		}
	}
	for _, op := range table {
		if !inSpec[op.ID] {
			problems = append(problems, fmt.Sprintf("%s (%s %s) is in operations.yaml but not in the spec", op.ID, op.Method, op.Path))
		}
	}
	return problems
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(slices.Compact(a), slices.Compact(b))
}

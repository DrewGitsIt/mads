package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/services/data/datatest"
)

// TestCmdTemplateValidateAllCleanStore covers the zero-exit half of
// acceptance: against a freshly seeded store (just the valid default
// template), `mads template validate` succeeds and reports it.
func TestCmdTemplateValidateAllCleanStore(t *testing.T) {
	base := datatest.Start(t)

	var out bytes.Buffer
	if err := cmdTemplate([]string{"validate", "-data", base}, &out); err != nil {
		t.Fatalf("clean store: want nil error, got %v\noutput:\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "default-implementation  ok") {
		t.Errorf("want the seeded default reported ok, got:\n%s", out.String())
	}
}

// TestCmdTemplateValidateAllReportsInvalid covers the non-zero-exit half:
// once the Data Service's validate-all report includes an invalid
// template, the CLI must exit non-zero and print that template's name plus
// each field error. A real Data Service can never itself hold an invalid
// template reachable this way (handleCreate/handleUpdate already refuse
// one) — that's the whole point of this feature: the only way to see one is
// a store that predates a grammar tightening. So this test stands in for
// that Data Service response with a canned HTTP server returning the exact
// validate-all shape services/data/internal/api/templates.go documents,
// which is exactly what cmd/mads talks to over the wire regardless of how
// the row got invalid.
func TestCmdTemplateValidateAllReportsInvalid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/templates/validate-all" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{
			{"id": "tpl_x", "name": "default-implementation", "valid": true, "errors": []any{}},
			{"id": "tpl_y", "name": "old-one", "valid": false, "errors": []map[string]string{
				{"field": "source", "message": `line 7: unknown field "retires" — did you mean "retry"?`},
			}},
		}})
	}))
	defer srv.Close()

	var out bytes.Buffer
	err := cmdTemplate([]string{"validate", "-data", srv.URL}, &out)
	if err == nil {
		t.Fatal("want a non-zero-exit error once a stored template is invalid")
	}
	if !strings.Contains(err.Error(), "1 of 2") {
		t.Errorf("error should report the invalid count: %v", err)
	}
	report := out.String()
	for _, want := range []string{"old-one", "INVALID", "source:", `line 7: unknown field "retires"`} {
		if !strings.Contains(report, want) {
			t.Errorf("report missing %q, got:\n%s", want, report)
		}
	}
	if strings.Contains(report, "default-implementation  INVALID") {
		t.Errorf("valid template misreported as invalid:\n%s", report)
	}
}

// TestCmdTemplateValidateNamed covers `mads template validate <name>`: it
// checks only the named template(s) against a real Data Service, and
// errors clearly on an unknown name.
func TestCmdTemplateValidateNamed(t *testing.T) {
	base := datatest.Start(t)

	var out bytes.Buffer
	if err := cmdTemplate([]string{"validate", "-data", base, "default-implementation"}, &out); err != nil {
		t.Fatalf("named, valid template: want nil error, got %v", err)
	}
	if !strings.Contains(out.String(), "default-implementation  ok") {
		t.Errorf("want the named template reported ok, got:\n%s", out.String())
	}

	out.Reset()
	err := cmdTemplate([]string{"validate", "-data", base, "does-not-exist"}, &out)
	if err == nil {
		t.Fatal("unknown template name: want an error")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("error should name the unknown template: %v", err)
	}
}

// TestCmdTemplateUnknownSubcommand covers dispatch: an unrecognized
// subcommand (or none at all) is a clear error, not a panic or a silent
// no-op.
func TestCmdTemplateUnknownSubcommand(t *testing.T) {
	var out bytes.Buffer
	if err := cmdTemplate(nil, &out); err == nil {
		t.Fatal("no subcommand: want an error")
	}
	if err := cmdTemplate([]string{"bogus"}, &out); err == nil {
		t.Fatal("unknown subcommand: want an error")
	}
}

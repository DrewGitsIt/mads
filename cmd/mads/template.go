package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
)

// cmdTemplate dispatches `mads template <subcommand>`. It is its own
// sub-dispatcher (mirroring cmdUpdate's "check"/"apply" split) rather than
// another flat case in main.go's switch, so template can grow more
// subcommands later without main.go's usage text and switch needing to
// know about each one.
func cmdTemplate(args []string, out io.Writer) error {
	if len(args) < 1 {
		return fmt.Errorf("template: subcommand required (validate)")
	}
	switch args[0] {
	case "validate":
		return cmdTemplateValidate(args[1:], out)
	default:
		return fmt.Errorf("template: unknown subcommand %q (want: validate)", args[0])
	}
}

// cmdTemplateValidate is the CLI form of the Data Service's validate-all
// discovery report (services/data/internal/api/templates.go): the thing to
// run after upgrading mads to find out which stored templates the
// now-strict pkg/template grammar broke. pkg/template started rejecting
// unknown YAML keys it used to silently drop, so a template saved under the
// old, lax grammar can be invalid today even though handleCreate/
// handleUpdate already block any *new* invalid save — this is how you find
// the pre-existing damage. A non-zero exit on any invalid template (rather
// than just printing a report) is what makes this usable from CI and from
// an upgrade script, not only interactively.
func cmdTemplateValidate(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("template validate", flag.ExitOnError)
	data := fs.String("data", "http://localhost:17374", "Data Service URL")
	_ = fs.Parse(args)
	names := fs.Args()

	client := dataclient.New(*data, "user")
	ctx := context.Background()

	var results []dataclient.TemplateValidation
	if len(names) == 0 {
		var err error
		results, err = client.ValidateAllTemplates(ctx)
		if err != nil {
			return fmt.Errorf("validate all templates: %w", err)
		}
	} else {
		// There's no validate-a-subset endpoint — validate-all already
		// re-derives everything from stored source in one pass — so for a
		// named subset, fetch each by name and run the same launch-check
		// the server runs via POST /templates/validate.
		for _, name := range names {
			t, err := client.Template(ctx, name)
			if err != nil {
				return fmt.Errorf("template %q: %w", name, err)
			}
			row := dataclient.TemplateValidation{ID: t.ID, Name: t.Name, Valid: true}
			if verr := client.ValidateTemplate(ctx, t.Source); verr != nil {
				var ve *contracts.ValidationError
				if !errors.As(verr, &ve) {
					return fmt.Errorf("template %q: %w", name, verr)
				}
				row.Errors = ve.Errors
			}
			results = append(results, row)
		}
	}
	return reportTemplateValidation(out, results)
}

// reportTemplateValidation prints one line per template plus every field
// error (each message already carries its source line, e.g. `line 7:
// unknown field "retires" — did you mean "retry"?`) and returns a non-nil
// error — and therefore a non-zero process exit via main()'s error path —
// when any template is invalid.
func reportTemplateValidation(out io.Writer, results []dataclient.TemplateValidation) error {
	invalid := 0
	for _, res := range results {
		if res.Valid {
			fmt.Fprintf(out, "%s  ok\n", res.Name)
			continue
		}
		invalid++
		fmt.Fprintf(out, "%s  INVALID\n", res.Name)
		for _, fe := range res.Errors {
			fmt.Fprintf(out, "  %s: %s\n", fe.Field, fe.Message)
		}
	}
	if invalid > 0 {
		return fmt.Errorf("%d of %d template(s) invalid", invalid, len(results))
	}
	fmt.Fprintf(out, "%d template(s) valid\n", len(results))
	return nil
}

// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/jaab-tech/fluxrig/pkg/gears/native/iso8583/codec/sdl"
	"github.com/jaab-tech/fluxrig/pkg/utils/path"
	"github.com/jaab-tech/fluxrig/pkg/viz/protodoc"
)

var specDocCmd = &cobra.Command{
	Use:   "doc <spec-file>",
	Short: "Render the protocol reference from a spec",
	Long: `Renders a protocol reference from a spec: the messages, what each one
carries, and what every data element means.

The spec stores its rules on the fields, because a field's rules are what change
together — a scheme bulletin names one data element across several messages. A
reader wants the opposite, so the per-message view here is that matrix inverted.
It is derived on every render rather than maintained, which is the only way the
two stay in agreement.

Two formats. Markdown for a repository or a docs site; HTML for a page that is
read, printed, or saved as a PDF. The HTML is self-contained — no scripts,
stylesheets or fonts are fetched — because a protocol reference gets mailed and
opened offline. Everything it needs travels inside the file, the fluxrig mark
included; the protocol it documents is yours, and nothing on the page claims
otherwise.

Two variants:

  public     omits fields marked "scope: private", and says how many it withheld.
             Only this one is eligible for publication.
  complete   everything, for internal reference.

The spec is loaded before it is rendered, so a spec that does not resolve fails
here rather than producing a reference that describes nothing real.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		scope, _ := cmd.Flags().GetString("scope")
		format, _ := cmd.Flags().GetString("format")
		out, _ := cmd.Flags().GetString("out")
		title, _ := cmd.Flags().GetString("title")

		switch protodoc.Scope(scope) {
		case protodoc.ScopePublic, protodoc.ScopeComplete:
		default:
			return fmt.Errorf("unknown scope %q; use public or complete", scope)
		}
		switch format {
		case "markdown", "html":
		default:
			return fmt.Errorf("unknown format %q; use markdown or html", format)
		}

		safePath, err := path.Sanitize(args[0])
		if err != nil {
			return fmt.Errorf("invalid file path: %w", err)
		}

		// Rendering a spec nobody could load would produce a reference to a
		// protocol that does not exist. The load is the check.
		if _, _, errLoad := sdl.LoadSpec(safePath); errLoad != nil {
			return fmt.Errorf("the spec does not load, so there is nothing to document: %w", errLoad)
		}

		raw, err := os.ReadFile(filepath.Clean(safePath))
		if err != nil {
			return err
		}
		spec, err := sdl.ParseSemantic(raw)
		if err != nil {
			return err
		}
		if title == "" {
			title = spec.Name
		}

		// Both formats render one document, built once. Deciding what to say
		// twice is how two formats come to disagree about the same spec.
		// The source travels with the document: every section carries the text it
		// was derived from, so a reader who doubts a table can see the file.
		doc := protodoc.Build(spec, protodoc.Options{
			Scope:   protodoc.Scope(scope),
			Title:   title,
			Source:  raw,
			BaseDir: filepath.Dir(safePath),
		})
		rendered := protodoc.MarkdownDoc(doc)
		if format == "html" {
			rendered = protodoc.HTMLDoc(doc)
		}

		if out == "" || out == "-" {
			_, err = fmt.Fprint(cmd.OutOrStdout(), rendered)
			return err
		}
		safeOut, err := path.Sanitize(out)
		if err != nil {
			return fmt.Errorf("invalid output path: %w", err)
		}
		if errWrite := os.WriteFile(safeOut, []byte(rendered), 0o600); errWrite != nil {
			return errWrite
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s (%s)\n", safeOut, scope)
		return err
	},
}

func init() {
	specDocCmd.Flags().String("scope", "public", "public | complete")
	specDocCmd.Flags().String("format", "markdown", "markdown | html")
	specDocCmd.Flags().String("out", "", "Output file (default: stdout)")
	specDocCmd.Flags().String("title", "", "Heading (default: the spec's own name)")
	specCmd.AddCommand(specDocCmd)
}

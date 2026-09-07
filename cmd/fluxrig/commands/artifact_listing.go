// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/manager"
)

// printArtifacts writes a listing of one kind.
//
// One kind: the store holds specs and scenarios in a single index, and both
// listings used to print all of it, so `spec list` showed scenarios as specs.
// Filtering here rather than at each call site is what keeps that from coming
// back the next time a listing is added.
func printArtifacts(w io.Writer, list []manager.ArtifactInfo, kind manager.Kind, asJSON bool) error {
	var mine []manager.ArtifactInfo
	for _, a := range list {
		if a.Kind == kind {
			mine = append(mine, a)
		}
	}

	if asJSON {
		if mine == nil {
			mine = []manager.ArtifactInfo{}
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(mine)
	}

	if len(mine) == 0 {
		_, _ = fmt.Fprintf(w, "No %ss in the store.\n", kind)
		return nil
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	// No column for the protocol: no spec in the wild declares one, they all
	// rely on the loader's default, and a column that is always empty tells a
	// reader these specs have none. The field is still in the JSON, where a
	// machine can read "undeclared" for what it is.
	header := "NAME\tVERSION\tIMPORTED\tSIZE\tHASH"
	if kind == manager.KindSpec {
		// The title is what a person recognises and is not the reference: a
		// scenario writes iso8583-v87-ascii, a person reads ISO 8583:1987.
		header += "\tTITLE"
	}
	_, _ = fmt.Fprintln(tw, header)

	for _, a := range mine {
		version := a.Tag
		if a.Latest {
			// The alias resolves here, and a reader scanning six versions wants
			// to know which one a reference without a tag will reach.
			version += " (latest)"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s",
			a.Name, version, importedAt(a.ImportedAt), byteSize(a.Size), shortHash(a.Hash))
		if kind == manager.KindSpec {
			_, _ = fmt.Fprintf(tw, "\t%s", orUnknown(a.Title))
		}
		_, _ = fmt.Fprintln(tw)
	}
	return tw.Flush()
}

// importedAt renders a recorded date, and says so when there is none.
//
// An artefact filed before the store kept dates has no date. Showing the blob
// file's timestamp instead would look like an answer and be a different fact:
// copying a store rewrites every one of them.
func importedAt(t time.Time) string {
	if t.IsZero() {
		return "unrecorded"
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

func byteSize(n int64) string {
	switch {
	case n <= 0:
		return "-"
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

// shortHash is the prefix the CAS itself uses in a reference.
func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

func orUnknown(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

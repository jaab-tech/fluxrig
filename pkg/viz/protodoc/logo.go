// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package protodoc

import (
	_ "embed"
	"fmt"
	"regexp"
	"strings"
)

// The mark, as the brand draws it.
//
// It is the file rather than a copy transcribed into a string: a mark rebuilt by
// hand drifts from the one the brand actually uses, and nobody notices until it
// is beside the real one. Updating it is replacing the file.
//
//go:embed assets/fluxrig_logo.svg
var fluxrigLogoSVG string

var (
	xmlDecl       = regexp.MustCompile(`(?s)<\?xml.*?\?>\s*`)
	svgMetadata   = regexp.MustCompile(`(?s)<metadata\b.*?</metadata>`)
	svgNamedView  = regexp.MustCompile(`(?s)<sodipodi:namedview\b.*?(/>|</sodipodi:namedview>)`)
	editorAttrs   = regexp.MustCompile(`\s(inkscape|sodipodi):[\w-]+="[^"]*"`)
	editorNS      = regexp.MustCompile(`\sxmlns:(inkscape|sodipodi)="[^"]*"`)
	svgComment    = regexp.MustCompile(`(?s)<!--.*?-->`)
	svgIDAttr     = regexp.MustCompile(`id="([^"]+)"`)
	svgIDRef      = regexp.MustCompile(`url\(#([^)]+)\)`)
	svgSizeAttrs  = regexp.MustCompile(`\s(width|height)="\d+"`)
	svgOpenTag    = regexp.MustCompile(`^<svg\b`)
	svgWhitespace = regexp.MustCompile(`\s+`)
)

// logoMark renders the mark. suffix makes every id inside it unique, so the page
// can carry more than one without two copies claiming the same gradient.
//
// Three things are changed and no more: the editor's own metadata is dropped,
// the intrinsic 1250×450 is removed so a lost stylesheet cannot draw it a metre
// wide, and the wordmark's hardcoded fills become custom properties — its dark
// blue vanishes on a dark ground, and nothing else can reach a fill attribute.
func logoMark(suffix string) string {
	s := fluxrigLogoSVG
	s = xmlDecl.ReplaceAllString(s, "")
	s = svgMetadata.ReplaceAllString(s, "")
	s = svgNamedView.ReplaceAllString(s, "")
	s = svgComment.ReplaceAllString(s, "")
	s = editorAttrs.ReplaceAllString(s, "")
	s = editorNS.ReplaceAllString(s, "")
	s = svgWhitespace.ReplaceAllString(s, " ")
	s = strings.ReplaceAll(s, "> <", "><")
	s = svgSizeAttrs.ReplaceAllString(s, "")

	s = svgIDAttr.ReplaceAllString(s, `id="$1`+suffix+`"`)
	s = svgIDRef.ReplaceAllString(s, `url(#$1`+suffix+`)`)

	for placeholder, hardcoded := range map[string]string{
		"--logo-word": "#2f627d", // "flux"
		"--logo-mark": "#2cb2a7", // "rig"
		"--logo-dot":  "#d5eb74", // the dot on the i
	} {
		s = strings.ReplaceAll(s, `fill="`+hardcoded+`"`,
			fmt.Sprintf(`fill="var(%s,%s)"`, placeholder, hardcoded))
	}

	s = svgOpenTag.ReplaceAllString(s, `<svg class="logo" role="img" aria-label="fluxrig"`)
	return strings.TrimSpace(s)
}

package extraction

import (
	"path"
	"strings"

	"pvfine/internal/pvf"
)

// assetExtensions lists extensions that live in the client image packs rather
// than inside the archive. A reference to them is recorded, never followed.
var assetExtensions = map[string]bool{".img": true}

// looksLikeFileRef reports whether a token value plausibly addresses a file,
// whether inside the archive or in the client image packs. Registry rows and
// backticked section values are relative paths with mixed letter case, so only
// shape can be judged here.
//
// A directory separator is NOT required: owner-relative references are commonly
// bare file names, e.g. an aicharacter's [ai pattern] holds `Event.ai` and a
// monster's [attack info] may hold `Attack1.atk`.
func looksLikeFileRef(value string) bool {
	if value == "" || len(value) > 300 {
		return false
	}
	if strings.ContainsAny(value, "\r\n\t") {
		return false
	}
	if strings.HasPrefix(strings.TrimSpace(value), "[") {
		return false
	}
	if isDynamicPath(value) {
		return false
	}
	return plausibleExtension(path.Ext(value))
}

// looksLikeArchiveEntry reports whether a file reference can address something
// inside the archive. Image references resolve only against the client's
// ImagePacks2 directory, so they must never be looked up as archive entries.
func looksLikeArchiveEntry(value string) bool {
	if !looksLikeFileRef(value) {
		return false
	}
	return !assetExtensions[strings.ToLower(path.Ext(value))]
}

// looksLikeAssetPath reports whether a value addresses a client image.
func looksLikeAssetPath(value string) bool {
	if value == "" || strings.ContainsAny(value, "\r\n\t") {
		return false
	}
	return assetExtensions[strings.ToLower(path.Ext(value))]
}

// isDynamicPath reports whether a value contains a runtime substitution that
// static analysis cannot resolve, such as `%d%d.img` or `Monster/*.img`.
// Per the compatibility-matrix convention these must be reported as unknown,
// never as missing.
func isDynamicPath(value string) bool {
	return strings.ContainsAny(value, "*?%")
}

// plausibleExtension accepts one to five letters after the dot. PVF extensions
// are not uniformly three characters: sprite definitions use `.ai` and include
// headers use `.h`, so a fixed length would silently drop every such reference.
func plausibleExtension(ext string) bool {
	ext = strings.ToLower(ext)
	if len(ext) < 2 || len(ext) > 6 || ext[0] != '.' {
		return false
	}
	for i := 1; i < len(ext); i++ {
		if c := ext[i]; c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}

// candidateShape classifies an undeclared token as a probable reference so an
// incomplete catalog surfaces as data instead of silent loss.
//
// Path-shaped candidates are high confidence. Numeric candidates are deliberately
// tagged separately: measured on a real dungeon, integer coordinates inside
// animation and event-position sections are indistinguishable from identifiers,
// so consumers must treat Shape=="numeric" as a low-priority queue.
func candidateShape(el *pvf.ScriptElement) string {
	switch el.TokenType {
	case 6:
		if looksLikeFileRef(el.Value) || looksLikeAssetPath(el.Value) ||
			(isDynamicPath(el.Value) && plausibleExtension(path.Ext(el.Value))) {
			return "path"
		}
	case 0:
		if len(el.Value) >= 4 && allDigits(el.Value) {
			return "numeric"
		}
	}
	return ""
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

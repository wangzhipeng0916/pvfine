package extraction

import _ "embed"

// EmbeddedEdges is the built-in reference-edge catalog. Callers may pass
// alternative bytes to LoadCatalog to let users override or extend it without
// rebuilding the application.
//
//go:embed edges/extract-edges.json
var EmbeddedEdges []byte

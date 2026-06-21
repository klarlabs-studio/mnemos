// Package memory registers mnemos's memory storage provider with the store factory.
//
// Blank-import it so mnemos.New can open memory:// DSNs:
//
//	import _ "go.klarlabs.de/mnemos/memory"
//
// This is the public, externally-importable entry point for the provider whose
// implementation lives under internal/store/memory (which external modules cannot
// import directly).
package memory

import _ "go.klarlabs.de/mnemos/internal/store/memory"

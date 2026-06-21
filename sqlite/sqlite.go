// Package sqlite registers mnemos's sqlite storage provider with the store factory.
//
// Blank-import it so mnemos.New can open sqlite:// DSNs:
//
//	import _ "go.klarlabs.de/mnemos/sqlite"
//
// This is the public, externally-importable entry point for the provider whose
// implementation lives under internal/store/sqlite (which external modules cannot
// import directly).
package sqlite

import _ "go.klarlabs.de/mnemos/internal/store/sqlite"

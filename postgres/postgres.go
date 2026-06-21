// Package postgres registers mnemos's postgres storage provider with the store factory.
//
// Blank-import it so mnemos.New can open postgres:// DSNs:
//
//	import _ "go.klarlabs.de/mnemos/postgres"
//
// This is the public, externally-importable entry point for the provider whose
// implementation lives under internal/store/postgres (which external modules cannot
// import directly).
package postgres

import _ "go.klarlabs.de/mnemos/internal/store/postgres"

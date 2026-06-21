// Package libsql registers mnemos's libsql storage provider with the store factory.
//
// Blank-import it so mnemos.New can open libsql:// DSNs:
//
//	import _ "go.klarlabs.de/mnemos/libsql"
//
// This is the public, externally-importable entry point for the provider whose
// implementation lives under internal/store/libsql (which external modules cannot
// import directly).
package libsql

import _ "go.klarlabs.de/mnemos/internal/store/libsql"

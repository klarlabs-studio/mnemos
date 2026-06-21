// Package mysql registers mnemos's mysql storage provider with the store factory.
//
// Blank-import it so mnemos.New can open mysql:// DSNs:
//
//	import _ "go.klarlabs.de/mnemos/mysql"
//
// This is the public, externally-importable entry point for the provider whose
// implementation lives under internal/store/mysql (which external modules cannot
// import directly).
package mysql

import _ "go.klarlabs.de/mnemos/internal/store/mysql"

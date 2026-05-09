package main

import (
	"net/http"
	"os"
)

// securityHeaders is the outermost wrapper around every HTTP response,
// sitting between panicRecover and the access log so even handler
// panics still produce a hardened response. Defaults follow OWASP
// Secure Headers Project recommendations as of 2026.
//
// CSP notes: the embedded landing page (cmd/mnemos/web/landing.html)
// currently inlines a <script> block for the lead-form submit handler,
// so script-src needs 'unsafe-inline' until that is moved to a
// static asset (tracked separately). Everything else is locked down
// to same-origin: no third-party scripts, no framing, no referrer leak.
//
// Strict-Transport-Security is only emitted when the server is serving
// over TLS — otherwise browsers would refuse subsequent plaintext
// connections that legitimate operators still rely on for local
// development and Docker bring-up.
func securityHeaders(next http.Handler) http.Handler {
	tlsEnabled := os.Getenv("MNEMOS_TLS_CERT_FILE") != "" &&
		os.Getenv("MNEMOS_TLS_KEY_FILE") != ""

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		// Defense in depth — these are cheap and cover the bulk of
		// drive-by browser attacks even on endpoints that don't
		// render HTML, in case a future change starts sending some.
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		// Block the legacy XSS auditor, which can be weaponised on
		// some browsers; modern browsers ignore it.
		h.Set("X-XSS-Protection", "0")
		// Permissions-Policy: deny powerful APIs by default. Mnemos
		// has no use for camera/microphone/geolocation — say so.
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")

		// CSP — same-origin scripts/styles, deny framing, deny mixed
		// content, restrict form-action to same origin so a phished
		// page cannot exfiltrate the lead form to a third party.
		// 'unsafe-inline' is a transitional concession for the
		// landing page's inline submit handler; tighten when the
		// inline script is removed.
		h.Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self' 'unsafe-inline'; "+
				"style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; "+
				"font-src 'self' data:; "+
				"connect-src 'self'; "+
				"form-action 'self'; "+
				"frame-ancestors 'none'; "+
				"base-uri 'self'; "+
				"object-src 'none'")

		if tlsEnabled {
			// Two-year max-age + includeSubDomains is the OWASP
			// HSTS preload-list threshold. Operators who don't want
			// preload can shorten max-age via a fronting proxy.
			h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}

		next.ServeHTTP(w, r)
	})
}

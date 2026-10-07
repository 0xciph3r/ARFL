//go:build server

package main

// Wails server mode serves every bound Bridge method over HTTP with no
// authentication. ARFL's bridge returns the device key and spends tokens, so
// a server-mode build must never exist; this stops one from compiling.
var _ = arflMustNotBuildInWailsServerMode

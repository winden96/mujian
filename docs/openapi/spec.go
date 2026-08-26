// Package openapi exposes the public Mujian relay contract embedded in the
// application binary. Keeping the contract next to its source JSON makes the
// public endpoint independent of the frontend deployment layout.
package openapi

import _ "embed"

// relaySpec is the public, client-facing model gateway contract.
//
//go:embed mujian-relay.json
var relaySpec []byte

// RelaySpec returns the embedded public relay OpenAPI document.
func RelaySpec() []byte {
	return relaySpec
}

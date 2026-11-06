// Package mintconsts holds fixed protocol constants shared by mint
// client and server code. It has no imports from mintcore, mintclient,
// or platform packages — only constants.
package mintconsts

// OIDCAudience is the canonical OIDC audience value for the fullsend
// token mint. All mint entrypoints (GCF, standalone, CF Worker WASM)
// and the mint client use this constant when requesting or verifying
// OIDC tokens.
const OIDCAudience = "fullsend-mint"

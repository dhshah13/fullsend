//go:build !js

package mintcore

import "os"

// mintEnv returns the value of the environment variable named by key.
// On native platforms (GCF, standalone), this delegates to os.Getenv.
// On WASM (CF Worker), RegisterEnv must be called first to supply a
// JS callback that reads Worker bindings.
func mintEnv(key string) string {
	return os.Getenv(key)
}

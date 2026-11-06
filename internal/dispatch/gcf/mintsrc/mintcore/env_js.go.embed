//go:build js

package mintcore

import (
	"fmt"
	"syscall/js"
)

var registeredEnvFn js.Value

// RegisterEnv stores a JS callback for environment lookups. The
// callback signature is (key: string) => string. It must be called
// once from mintcoreInitMint before NewHandler reads configuration.
func RegisterEnv(fn js.Value) error {
	if fn.IsUndefined() || fn.IsNull() {
		return fmt.Errorf("env callback must not be null or undefined")
	}
	if fn.Type() != js.TypeFunction {
		return fmt.Errorf("env callback must be a function, got %s", fn.Type())
	}
	registeredEnvFn = fn
	return nil
}

// mintEnv returns the value of the environment variable named by key.
// On WASM it calls the JS callback registered via RegisterEnv.
func mintEnv(key string) string {
	if registeredEnvFn.IsUndefined() {
		return ""
	}
	result := registeredEnvFn.Invoke(key)
	if result.Type() == js.TypeString {
		return result.String()
	}
	return ""
}

# Creating secure gate binaries

This guide covers building gate binaries — small, statically linked programs that enforce application-level constraints on endpoint access from inside the sandbox. Gate binaries sit between the agent and an external service, validating requests before forwarding them.

## When you need a gate binary

OpenShell's L7 network policy enforces method and path restrictions at the proxy level. These controls are kernel-backed, per-binary, and not bypassable from inside the sandbox. For many access patterns, L7 rules are sufficient — use them when your constraints can be expressed as method + path combinations.

Gate binaries are needed when your access constraints go **beyond** what L7 rules can express:

- **Branch restrictions** — allow `git push` only to specific branches
- **Body filtering** — strip or validate fields in request bodies before forwarding
- **Query parameter enforcement** — restrict which query parameters are allowed
- **Commit signing** — sign commits or tags before pushing
- **Payload validation** — enforce schemas or size limits on outbound requests
- **Multi-step authorization** — check a condition (e.g., CI status) before allowing an operation

If L7 method/path rules cover your case, prefer those — they are proxy-enforced and immune to code injection.

## The LD_PRELOAD threat

The sandbox workspace must be writable and executable for code agents to function — they need to write code, compile, and run tests. This creates a specific threat: a dynamically linked binary running inside the sandbox is vulnerable to `LD_PRELOAD` injection.

An agent (or malicious code in a dependency it installs) can:

1. Write a shared object (`.so`) to the writable workspace
2. Set `LD_PRELOAD` to point to it
3. Override functions in the gate binary's validation logic

The dynamic linker processes `LD_PRELOAD` before the binary's own code runs, so injected code executes with the binary's identity. OpenShell's binary identity check (`/proc/pid/exe`) still sees the legitimate gate binary, meaning the proxy allows the connection — but the binary's validation logic has been bypassed.

**Static linking eliminates this threat.** The dynamic linker does not process `LD_PRELOAD` for statically linked executables. There is no dynamic symbol resolution to intercept.

## How to build a gate binary

### 1. Write the binary in Go with static linking

Go produces statically linked binaries by default when CGO is disabled. Build with:

```bash
CGO_ENABLED=0 go build -o gate-push ./cmd/gate-push/
```

Verify the binary is statically linked:

```bash
ldd gate-push
# Expected output: "not a dynamic executable"
```

If `ldd` reports any shared libraries, the binary is dynamically linked and vulnerable.

### 2. Structure the binary

A gate binary typically:

1. Parses the agent's request (command-line arguments, stdin, or an HTTP request)
2. Validates the request against its rules
3. Executes the real operation if validation passes
4. Returns the result to the agent

Keep the binary focused — it enforces one set of constraints for one operation. Complex multi-operation gate binaries are harder to audit.

### Example: branch-restricted git push

This example gate binary wraps `git push`, allowing pushes only to branches matching an allowed pattern:

```go
// cmd/gate-push/main.go
package main

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "usage: gate-push <remote> <refspec>\n")
		os.Exit(1)
	}

	remote := os.Args[1]
	refspec := os.Args[2]

	// Allow pushes only to agent/* branches.
	allowed := regexp.MustCompile(`^[^:]*:refs/heads/agent/`)
	if !allowed.MatchString(refspec) {
		fmt.Fprintf(os.Stderr, "gate-push: rejected — refspec %q does not target an agent/* branch\n", refspec)
		os.Exit(1)
	}

	cmd := exec.Command("git", "push", remote, refspec)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		os.Exit(1)
	}
}
```

Build it:

```bash
CGO_ENABLED=0 go build -o gate-push ./cmd/gate-push/
```

## Configuring the sandbox policy

The gate binary must be the **only** binary allowed to reach the endpoint it guards. If another binary (or a wildcard pattern) can also reach the same endpoint, the agent can bypass the gate by using that binary directly.

### Provider profile for the gate binary

Create a provider profile that grants the gate binary access to the guarded endpoint. The profile's `binary` field restricts which executable can use the endpoint:

```yaml
# profiles/gate-push.yaml
endpoints:
  - host: "github.com"
    port: 443
    methods: ["CONNECT"]
    binary: "/sandbox/workspace/gate-push"
```

### Provider definition

Create a provider definition that references the profile:

```yaml
# providers/gate-push.yaml
name: gate-push
type: custom
profiles:
  gate-push-access:
    file: profiles/gate-push.yaml
```

### Harness configuration

Reference the provider in your harness file and upload the gate binary to the sandbox:

```yaml
# harness/code.yaml
providers:
  - providers/gate-push.yaml

host_files:
  - source: gate-push           # pre-built static binary
    dest: /sandbox/workspace/gate-push
    mode: "0755"
```

### Key policy rules

1. **No overlapping access.** Ensure no other provider profile or inline network policy grants access to the guarded endpoint with a different binary or a wildcard. If `git` can also reach `github.com:443`, the gate binary is bypassed.

2. **Binary path must be exact.** The `binary` field in the profile uses the full path as resolved via `/proc/pid/exe`. Use the absolute path where the binary is placed in the sandbox.

3. **Separate the guarded endpoint.** If the agent needs to reach `github.com` for other operations (e.g., API calls via `gh`), use separate profiles with different `binary` restrictions for each use case.

## What the proxy enforces vs. what the binary enforces

Understanding the enforcement boundary is critical for correct policy design:

| Layer | Enforced by | Bypassable from sandbox? | Examples |
|-------|-------------|--------------------------|----------|
| Endpoint allowlist | OpenShell L7 proxy | No — kernel-backed | Which hosts and ports are reachable |
| Method/path rules | OpenShell L7 proxy | No — kernel-backed | `GET /api/v3/repos/*` only |
| Binary identity | OpenShell proxy (`/proc/pid/exe`) | No — kernel-backed | Only `gate-push` can reach `github.com:443` |
| Application logic | Gate binary code | Only if dynamically linked (`LD_PRELOAD`) | Branch validation, body filtering, signing |

The first three rows are proxy-enforced and hold regardless of what runs inside the sandbox. The fourth row — the gate binary's own validation logic — is the binary's responsibility. Static linking protects it from `LD_PRELOAD` injection, making the application logic tamper-resistant.

## Checklist

Before deploying a gate binary:

- [ ] Binary is built with `CGO_ENABLED=0` (Go) or equivalent static linking
- [ ] `ldd <binary>` reports "not a dynamic executable"
- [ ] Provider profile restricts the guarded endpoint to the gate binary only
- [ ] No other profile or network policy grants overlapping access to the same endpoint
- [ ] Gate binary validates all relevant request fields (not just the obvious ones)
- [ ] Gate binary is uploaded to the sandbox via `host_files` with mode `0755`
- [ ] Binary path in the profile matches the `dest` in `host_files`

## See also

- [Runtime implementation](../../contributing/runtime-implementation.md) — Binary identity enforcement details, sandbox hook contract, and workspace layout
- [Bring Your Own Agent](../user/bring-your-own-agent.md) — Harness configuration, providers, and profiles
- [ADR 0065](../../ADRs/0065-provider-backed-policy-composition.md) — Provider-backed policy composition

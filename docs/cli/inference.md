---
sidebar_label: fullsend inference
---

# fullsend inference

Manage the inference credentials agent runs use. `provision`, `deprovision` and `status` create, inspect, and remove the GCP Workload Identity Federation (WIF) pool, OIDC provider, and IAM bindings that let GitHub Actions workflows authenticate with GCP for Agent Platform (Vertex) access. [`openai`](#inference-openai) enrols repositories with OpenAI WIF for GPT models on the pi runtime.

## Commands

| Command | Description |
|---------|-------------|
| `fullsend inference provision <org\|owner/repo>` | Create WIF pool/provider and grant Agent Platform access |
| `fullsend inference deprovision <org\|owner/repo>` | Remove org or repo from WIF |
| `fullsend inference status <org\|owner/repo>` | Check WIF health and print config values |
| `fullsend inference openai request <owner/repo>[,…]` | Generate WIF provider/mapping request for OpenAI admin |
| `fullsend inference openai import [reply.json]` | Import OpenAI WIF identifiers into config |
| `fullsend inference openai status <owner/repo>` | Check OpenAI WIF configuration and exchange status |

## `inference provision`

Creates a WIF pool (`fullsend-inference`), an OIDC provider (`github-oidc`), and grants `roles/aiplatform.user` to the WIF principal. Idempotent and safe to re-run.

```bash
fullsend inference provision <org> \
  --project "<GCP_PROJECT>"
```

Per-repo mode scopes the WIF provider to a single repository:

```bash
fullsend inference provision <owner/repo> \
  --project "<GCP_PROJECT>"
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--project` | | GCP project ID |
| `--region` | `global` | GCP region |

### Required IAM roles

| Role | Description |
|------|-------------|
| `roles/iam.workloadIdentityPoolAdmin` | Create WIF pool and provider |
| `roles/resourcemanager.projectIamAdmin` | Grant `roles/aiplatform.user` to WIF principals |

### Required GCP APIs

```bash
gcloud services enable \
  iam.googleapis.com \
  cloudresourcemanager.googleapis.com \
  aiplatform.googleapis.com \
  --project="$GCP_PROJECT"
```

## `inference deprovision`

Removes an org or repo from WIF by deleting the IAM binding and (optionally) the WIF provider.

```bash
fullsend inference deprovision <org|owner/repo> \
  --project "<GCP_PROJECT>"
```

### Required IAM roles

| Role | Description |
|------|-------------|
| `roles/iam.workloadIdentityPoolAdmin` | Modify WIF pool and provider |

## `inference status`

Checks WIF health and prints the configuration values needed for `github setup`.

```bash
fullsend inference status <org|owner/repo> \
  --project "<GCP_PROJECT>"
```

Read-only — makes no changes.

## `inference openai`

Commands for enrolling repositories with OpenAI Workload Identity Federation (see the [operator guide](../guides/infrastructure/openai-workload-identity.md)). They need neither GCP access nor an OpenAI key.

`request` and `import` never reach the network: they produce a document and update local configuration. `status` reads configuration too, and — only when run inside a GitHub Actions job with `id-token: write` — performs one token exchange with OpenAI to prove the mapping accepts that repository, reporting the granted scope and expiry without ever printing the token.

### `inference openai request`

Generates the request document an administrator needs to enable OpenAI WIF for one or more repositories. Every value is computed from the repository names. Nothing is sent anywhere; the command needs no credentials.

```bash
fullsend inference openai request <owner/repo>[,<owner/repo>…] \
  [--audience "<audience>"] \
  [--project "<openai-project>"] \
  [--service-account "<existing-sa-id>"] \
  [--format json|md] \
  [--out <file>]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--audience` | `fullsend://<owner>` | OpenAI Workload Identity audience |
| `--project` | *(empty)* | OpenAI project name or ID for the service accounts |
| `--service-account` | `fullsend-<repo>-ci` | Existing service account ID (default: one per repo) |
| `--format` | `json` | Output format: `json` (versioned schema) or `md` (copy-paste ticket) |
| `--out` | *(stdout)* | Write output to a file |

### `inference openai import`

Takes the administrator's reply and writes `inference.openai` into `.fullsend/config.yaml` through the same setters as `fullsend github setup --openai-*`. All three identifiers must be present — a partial trio is refused.

```bash
# From a reply JSON file:
fullsend inference openai import reply.json

# From flags:
fullsend inference openai import \
  --audience "fullsend://<owner>" \
  --identity-provider-id "<idp-id>" \
  --service-account-id "<sa-id>"

# Set repository variables instead of config.yaml:
fullsend inference openai import \
  --variables --repo <owner/repo> \
  --audience "fullsend://<owner>" \
  --identity-provider-id "<idp-id>" \
  --service-account-id "<sa-id>"
```

| Flag | Default | Description |
|------|---------|-------------|
| `--audience` | | OpenAI Workload Identity audience |
| `--identity-provider-id` | | OpenAI identity provider ID |
| `--service-account-id` | | OpenAI service account ID |
| `--fullsend-dir` | `.fullsend` | Path to the .fullsend configuration directory |
| `--variables` | `false` | Set `FULLSEND_OPENAI_*` repository variables instead of config.yaml |
| `--repo` | | Target repository (`owner/repo`) for `--variables` |

### `inference openai status`

Prints the resolved OpenAI WIF identifiers and their source (config layer or environment variables), and flags a partial trio. When run inside a GitHub Actions job with `id-token: write`, performs one exchange and reports the returned scope and expiry without ever printing the token.

```bash
fullsend inference openai status <owner/repo> \
  [--fullsend-dir ".fullsend"]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--fullsend-dir` | `.fullsend` | Path to the .fullsend configuration directory |

## See also

- [Getting inference for fullsend](../guides/getting-started/getting-inference.md) — getting started guide
- [OpenAI Workload Identity](../guides/infrastructure/openai-workload-identity.md) — end-to-end OpenAI WIF setup guide
- [Advanced setup](../guides/infrastructure/advanced-setup.md) — non-standard installation paths and WIF configuration
- [CLI internals](../guides/dev/cli-internals.md) — command tree and implementation details

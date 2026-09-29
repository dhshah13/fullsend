# Recorded native Codex subagent usage

Reduced from successful local OpenAI API runs captured on 2026-09-28 with
exact stable Codex CLI versions **0.157.0** (`0157`) and **0.158.0** (`0158`),
model `gpt-5.6-luna`, low reasoning effort, native multi-agent V1, and three
fresh-context children (two named roles and one default role).

These are recorded fixtures, not fabricated event shapes. Reduction retained
all original `token_usage_record` envelopes and payloads, selected session
identity fields, and `turn_context` turn/model/root-turn fields, in original
order. Envelope timestamps, IDs, models and token counts are unchanged.
Prompts, instructions, cwd, local account/host details, tool calls, and all
other event types were omitted. No credentials were retained. Filenames were
renamed for clarity. Root/child association and per-response counters remain
verbatim. The tests supplement these captures with explicitly synthetic
mutation cases.

The first incomplete 0.158.0 attempt was **not** used: `0158` is the successful
`children-wait-loop` rerun. The private source corpus names are recorded below
with SHA-256 of the original, unreduced files so provenance can be checked
without committing their contents or local paths.

Official source archive provenance used to verify the wire types:

- `rust-v0.157.0`: archive SHA-256
  `10d6b3d485440b6b92d46e20398a940f756bb12a6a98a1daa1932df05937eab7`.
- `rust-v0.158.0`: archive SHA-256
  `e0c6f2492a1afad6ace9101b01a85deb58f81c52ee25af351d3b7478425a3558`.

| Fixture | Original rollout basename | Original SHA-256 |
| --- | --- | --- |
| `0157/root.jsonl` | `rollout-2026-09-28T10-22-59-01a0e865-c82b-7991-b075-3ad644e16b31.jsonl` | `784ee6fecef9ef5f7caf762615a6a27a790d27f652b8eef764f23168b79d85fe` |
| `0157/probe-alpha.jsonl` | `rollout-2026-09-28T10-23-04-01a0e865-da5f-7f23-a113-86d7a107c3f7.jsonl` | `c4b0f0694533e8716336723ef22173b7af7d91ea76f707287489512f755ece4b` |
| `0157/default.jsonl` | `rollout-2026-09-28T10-23-04-01a0e865-da84-71d3-95bd-ba0f3444eff2.jsonl` | `0bd0aef5d46e97e41aa4154869af6eb87ad89cf822ec190eaa93bbf4f6e0ae56` |
| `0157/probe-beta.jsonl` | `rollout-2026-09-28T10-23-04-01a0e865-daa3-7a71-b355-d76fc45c53f6.jsonl` | `43c6cb4b95f4bce3f4d69e04c44c6fefa1b798f36480fbdfb05be40d3731edc2` |
| `0158/root.jsonl` | `rollout-2026-09-28T10-25-25-01a0e868-0083-7571-b0e2-de21c3a6c910.jsonl` | `d8a5d6e760be4ce32420ea6c02017f345e64d68c6b190665a72eac7101daa186` |
| `0158/probe-alpha.jsonl` | `rollout-2026-09-28T10-25-29-01a0e868-0ff5-7b90-8852-e23f529ca223.jsonl` | `3c6269546850a566ece0dd1914d7338cab0e621a6f49c5263b5771ab208e617d` |
| `0158/probe-beta.jsonl` | `rollout-2026-09-28T10-25-29-01a0e868-1019-74d0-a11f-2ad529629759.jsonl` | `959ab32f90c4ddad81b5abe1703fda58042556d413a01687a8350782e322e359` |
| `0158/default.jsonl` | `rollout-2026-09-28T10-25-29-01a0e868-1037-7123-86da-fdb886715284.jsonl` | `8277c9906fe4e818f124763abd6150747787536773db92b735a7e135ba74ec96` |

Each version has six parent responses and one response from each child. The
parent stream total excludes children. Fullsend uses disjoint counters:
uncached input, nonreasoning output, reasoning, cache-read, cache-write.

| Version | Contribution | Uncached input | Nonreasoning output | Reasoning | Cache read | Cache write | Total |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 0.157.0 | Parent | 18 | 640 | 137 | 77975 | 17730 | 96500 |
| 0.157.0 | Children | 9 | 24 | 0 | 0 | 27626 | 27659 |
| 0.157.0 | Combined | 27 | 664 | 137 | 77975 | 45356 | 124159 |
| 0.158.0 | Parent | 18 | 624 | 170 | 78328 | 17845 | 96985 |
| 0.158.0 | Children | 9 | 26 | 11 | 0 | 27626 | 27672 |
| 0.158.0 | Combined | 27 | 650 | 181 | 78328 | 45471 | 124657 |

`Requests` means participating agent invocations: three children, not the
number of response records. Costs are unreported by Codex.

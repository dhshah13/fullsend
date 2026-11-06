#!/usr/bin/env bash
# Shared helper for resolving the trusted job ID from JOB_RESPONSE_FILE.
#
# Job identity comes from the runner-written JOB_RESPONSE_FILE, never from
# CUSTOM_ENV_CI_JOB_ID: CUSTOM_ENV_* values are job-controlled, so a job could
# name a concurrent job's container/state file and have this stage act on it.
# The runner sets JOB_RESPONSE_FILE for every stage and removes it only after
# cleanup has run.
#
# Usage (source from each executor stage):
#   source "$(dirname "${BASH_SOURCE[0]}")/job_id.sh"
#   JOB_ID=$(resolve_job_id) || { echo "ERROR: ..." >&2; exit 1; }
resolve_job_id() {
  [ -n "${JOB_RESPONSE_FILE:-}" ] && [ -r "${JOB_RESPONSE_FILE}" ] || return 1
  python3 -c '
import json, sys
v = json.load(open(sys.argv[1]))["id"]
if not isinstance(v, int) or v <= 0:
    sys.exit(1)
print(v)
' "${JOB_RESPONSE_FILE}"
}

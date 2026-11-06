#!/usr/bin/env bash
# Called by Renovate postUpgradeTasks after an OpenShell version bump.
#
# The github-releases datasource can't resolve a digest for OPENSHELL_SHA
# directly: Renovate's digest lookup compares the new value against raw
# GitHub tag names (e.g. "v0.0.103"), but extractVersionTemplate strips the
# "v" prefix from the value tracked in this file (e.g. "0.0.103"), so the
# comparison never matches. Instead, this script looks up the commit SHA
# for the release tag directly and patches it in.
set -euo pipefail

FILE=".github/scripts/openshell-version.sh"

OLD_VERSION=$(git show HEAD:"${FILE}" | grep -oP '^OPENSHELL_VERSION=\K\S+' || true)
NEW_VERSION=$(grep -oP '^OPENSHELL_VERSION=\K\S+' "${FILE}" || true)

if [[ -z "${NEW_VERSION}" ]]; then
  echo "error: could not extract OPENSHELL_VERSION from working-tree ${FILE}" >&2
  exit 1
fi
if [[ ! "${NEW_VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "error: NEW_VERSION is not a valid semver: ${NEW_VERSION}" >&2
  exit 1
fi

if [[ "${OLD_VERSION}" == "${NEW_VERSION}" ]]; then
  echo "OpenShell version unchanged (${NEW_VERSION}), nothing to do"
  exit 0
fi

echo "OpenShell: ${OLD_VERSION:-<none>} -> ${NEW_VERSION}"

WORKDIR="$(mktemp -d)"
trap 'rm -rf "${WORKDIR}"' EXIT

curl -fsSL "https://api.github.com/repos/NVIDIA/OpenShell/commits/v${NEW_VERSION}" \
  -o "${WORKDIR}/commit.json"
SHA=$(jq -r '.sha // empty' "${WORKDIR}/commit.json")

if [[ ! "${SHA}" =~ ^[0-9a-f]{40}$ ]]; then
  echo "error: resolved SHA is not a valid 40-char hex commit sha: ${SHA}" >&2
  exit 1
fi

if ! grep -q "^OPENSHELL_SHA=" "${FILE}"; then
  echo "error: no OPENSHELL_SHA line found in ${FILE}" >&2
  exit 1
fi

sed -i "s/^OPENSHELL_SHA=.*/OPENSHELL_SHA=${SHA}/" "${FILE}"

if ! grep -q "^OPENSHELL_SHA=${SHA}$" "${FILE}"; then
  echo "error: failed to update OPENSHELL_SHA in ${FILE}" >&2
  exit 1
fi

echo "updated OPENSHELL_SHA to ${SHA} for v${NEW_VERSION}"

#!/usr/bin/env bash
#
# lib.sh — Shared helpers for GitLab Runner VM provisioning scripts.
#
# Source this file at the top of any script that needs gl_curl().

# Wrap curl with GL_TOKEN passed via a temp config file to avoid
# exposing the token in /proc/<pid>/cmdline.
gl_curl() {
  local config old_umask rc
  old_umask=$(umask)
  umask 077
  config=$(mktemp)
  umask "${old_umask}"
  printf 'header = "PRIVATE-TOKEN: %s"\n' "${GL_TOKEN}" > "${config}"
  rc=0
  curl --max-time 30 --connect-timeout 10 -sf -K "${config}" "$@" || rc=$?
  rm -f "${config}"
  return "${rc}"
}

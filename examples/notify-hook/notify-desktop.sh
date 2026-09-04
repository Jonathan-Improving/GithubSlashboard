#!/bin/sh
# Example GSB_NOTIFY_HOOK target: turns a GithubSlashboard notification payload
# (SCHEMA.md § Notification hook) into a real desktop notification.
#
# Usage:
#   export GSB_NOTIFY_HOOK="/path/to/notify-desktop.sh"
#
# GithubSlashboard writes the JSON payload to this script's stdin after any
# run in which at least one open PR or issue needed a fresh judgment. Nothing
# is invoked otherwise — an unset GSB_NOTIFY_HOOK, or a run where nothing
# changed, means this script never runs.
#
# Requires exactly one of:
#   - macOS: terminal-notifier (brew install terminal-notifier)
#   - Linux: notify-send (usually preinstalled with a desktop environment)
# and a JSON extractor: python3 (stdlib json), used here for portability
# without adding a jq dependency. Swap in jq -r '.summary' if you prefer.
#
# The payload also carries the full `changed` array (repo, number, url,
# bucket, action, companion, priority per item) if you want a richer
# notification than the one-line summary — this script only reads `summary`
# to keep the example minimal.

set -eu

payload="$(cat)"

summary="$(printf '%s' "$payload" | python3 -c '
import json, sys
data = json.load(sys.stdin)
print(data.get("summary", "GithubSlashboard: something changed"))
')"

changed_count="$(printf '%s' "$payload" | python3 -c '
import json, sys
data = json.load(sys.stdin)
print(len(data.get("changed", [])))
')"

title="GithubSlashboard ($changed_count changed)"

# $summary is untrusted string data (model-generated text ultimately derived
# from GitHub content the operator does not control — see SCHEMA.md's
# Notification hook trust boundary). It is passed below as a single quoted
# argument to each notifier, never eval'd or interpolated into a re-parsed
# shell/AppleScript string — that distinction is what keeps a crafted PR
# comment from ever becoming a command.
if command -v terminal-notifier >/dev/null 2>&1; then
  # macOS
  terminal-notifier -title "$title" -message "$summary"
elif command -v notify-send >/dev/null 2>&1; then
  # Linux
  notify-send "$title" "$summary"
else
  # No notifier installed: fail loudly to stderr rather than silently doing
  # nothing, so a misconfigured hook is easy to spot in the run's logs
  # (GithubSlashboard logs a non-zero exit as a warning, never fatally).
  echo "notify-desktop.sh: neither terminal-notifier nor notify-send found" >&2
  echo "summary was: $summary" >&2
  exit 1
fi

#!/bin/bash
# Example wrapper for running GithubSlashboard unattended.
#
# Why a wrapper rather than invoking the binary directly from the scheduler:
#
#   1. A scheduled job does not inherit your interactive shell's environment. PATH
#      in particular is minimal, so anything the run shells out to must be findable.
#   2. The GitHub token should not be stored in a plist, a crontab, or a file on
#      disk. Fetching it at run time keeps it out of every artifact.
#   3. Keeping configuration here means the schedule itself never has to change.
#
# Adjust the paths and the token command for your setup, make it executable
# (chmod +x), and point your scheduler at this script.

set -euo pipefail

# Homebrew and user-local binaries are commonly outside a scheduled job's PATH.
export PATH="/opt/homebrew/bin:/usr/local/bin:$HOME/.local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

# Supply a read-only GitHub token. Any command that prints a token works; the
# GitHub CLI is convenient because it already manages credentials.
#   - gh auth token                       (GitHub CLI)
#   - security find-generic-password ...  (macOS Keychain)
#   - pass show github/token              (pass)
export GITHUB_TOKEN="$(gh auth token)"

# Optional: override the default artifact locations. Without these, the tool uses
# your platform's per-user application-data directory.
# export GSB_OUTPUT_PATH="$HOME/reports/github-status.md"
# export GSB_STORE_PATH="$HOME/.local/share/github-slashboard/prs.pr.yaml"

# Optional: skip the model for settled items to shorten an unattended run.
# export GSB_CLASSIFY_FLOOR_NOTES=false

exec githubslashboard "$@"

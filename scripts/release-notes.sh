#!/usr/bin/env bash
# release-notes.sh VERSION: prints the release notes for VERSION (1.2.3, or v1.2.3), its section of CHANGELOG.md, and
# fails when there is none, or it is empty. The release workflow checks this before it publishes anything, and uses
# it as the GitHub release's notes; run it to preview them. Links into the repository (docs/...) become links to the
# files at the release's tag: on the release page, a relative link would point under /releases/tag/.
set -euo pipefail
v=${1:?usage: scripts/release-notes.sh VERSION}
v=${v#v}
cd "$(dirname "$0")/.."
notes=$(awk -v head="## [$v]" '
  index($0, head) == 1 { on = 1; next }
  on && /^## \[/ { exit }
  on && /^\[[^]]+\]: / { exit }
  on { print }
' CHANGELOG.md | sed -e '/./,$!d' -E -e "s#\]\(((docs/|SECURITY\.md|CONTRIBUTING\.md|README\.md)[^)]*)\)#](https://github.com/greatmastix/MediaMTXUI/blob/v$v/\1)#g")
if [[ -z ${notes//[[:space:]]/} ]]; then
  echo "release-notes: CHANGELOG.md has no section \"## [$v]\" with notes: add it (move Unreleased there) first" >&2
  exit 1
fi
printf '%s\n' "$notes"

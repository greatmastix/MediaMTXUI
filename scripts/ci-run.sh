#!/usr/bin/env bash
# ci-run.sh COMMAND...: runs a ./dev command in GitHub Actions and, when it fails, publishes why as error annotations.
# Annotations are public on a public repository, unlike step logs, so anyone can see why a run failed without signing
# in. GitHub cuts an annotation at about 4 KB, so each failed part gets its own: every job ./dev ci names as failed
# (the lines that say what failed, then its last lines), the stack's warnings and errors after ./dev e2e, and the end
# of the command's own output.
set -uo pipefail
shopt -s nullglob
out=$(mktemp)
"$@" 2>&1 | tee "$out"
rc=${PIPESTATUS[0]}
((rc == 0)) && exit 0

# annotate TITLE: one error annotation with stdin, at most 3500 bytes (the end, if longer).
annotate() {
  local msg
  msg=$(sed -e 's/\x1b\[[0-9;]*m//g' | tail -c 3500 | sed -e 's/%/%25/g' -e 's/\r/%0D/g' | awk 'BEGIN{ORS="%0A"} {print}')
  echo "::error title=$1::$msg"
}

title="$1 ${2:-}"
if failed=$(grep -o 'failed: .*(logs in' "$out" | tail -1); then
  for f in .cache/parallel/*.log; do
    name=$(basename "$f" .log)
    [[ $failed == *"$name"* ]] || continue
    {
      grep -E -- '--- FAIL|_test\.go:[0-9]+|panic:|^FAIL|FAIL |✗|×|Error:|error:|issues:|\(revive\)|\(gofumpt\)|drifted|differs' "$f" | head -n 40
      echo "-----"
      tail -n 15 "$f"
    } | annotate "$title: $name"
  done
fi
for f in .cache/e2e-logs/*.log; do
  grep -E '"level":"(WARN|ERROR)"| ERR | WAR |level=(WARN|ERROR)' "$f" | tail -n 20 | annotate "$title: $(basename "$f")"
done
tail -n 40 "$out" | annotate "$title failed (exit $rc)"
exit "$rc"

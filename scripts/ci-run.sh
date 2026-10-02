#!/usr/bin/env bash
# ci-run.sh COMMAND...: runs a ./dev command in GitHub Actions and, when it fails, publishes the end of its output (and
# of the logs it names) as an error annotation. Annotations are public on a public repository, unlike step logs, so
# anyone can see why a run failed without signing in.
set -uo pipefail
out=$(mktemp)
"$@" 2>&1 | tee "$out"
rc=${PIPESTATUS[0]}
((rc == 0)) && exit 0
{
  tail -n 80 "$out"
  # ./dev ci names the parallel jobs that failed; ./dev e2e leaves the stack's logs in .cache/e2e-logs.
  if failed=$(grep -o 'failed: .*(logs in' "$out" | tail -1); then
    shopt -s nullglob
    for f in .cache/parallel/*.log; do
      name=$(basename "$f" .log)
      [[ $failed == *"$name"* ]] || continue
      echo "----- $name"
      tail -n 40 "$f"
    done
  fi
  for f in .cache/e2e-logs/*.log; do
    echo "----- $f"
    grep -E '"level":"(WARN|ERROR)"| ERR | WAR ' "$f" | tail -n 15
  done
} >"$out.tail" 2>/dev/null
msg=$(sed -e 's/%/%25/g' -e 's/\r/%0D/g' "$out.tail" | head -c 60000 | awk 'BEGIN{ORS="%0A"} {print}')
echo "::error title=$1 $2 failed (exit $rc)::$msg"
exit "$rc"

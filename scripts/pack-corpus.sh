#!/usr/bin/env bash
# Pack a repository into a single file for the Phase 0 "no RAG" baseline.
#
# The point of this script is to answer one question with a number:
#   does this corpus even need retrieval, or does it fit in a context window?
#
# Usage:
#   ./scripts/pack-corpus.sh ../cdc-pipeline eval/baseline/corpus.txt
set -euo pipefail

REPO="${1:-../cdc-pipeline}"
OUT="${2:-eval/baseline/corpus.txt}"

mkdir -p "$(dirname "$OUT")"
: > "$OUT"

# Same include/exclude rules the real indexer will use in Phase 1.
# Keep these two in sync or the baseline stops being comparable.
mapfile -t FILES < <(
  cd "$REPO" && find . -type f \
    \( -name '*.go' -o -name '*.md' -o -name '*.sql' -o -name '*.yml' \
       -o -name '*.yaml' -o -name '*.sh' -o -name 'Makefile' \
       -o -name 'Dockerfile' -o -name 'go.mod' \) \
    -not -path './.git/*' \
    -not -path './.claude/*' \
    -not -path './bin/*' \
    -not -name '*copy*' \
    | sed 's|^\./||' | sort
)

for f in "${FILES[@]}"; do
  {
    printf '===== FILE: %s =====\n' "$f"
    cat "$REPO/$f"
    printf '\n'
  } >> "$OUT"
done

BYTES=$(wc -c < "$OUT" | tr -d ' ')
LINES=$(wc -l < "$OUT" | tr -d ' ')

# ~4 chars per token is the usual rough ratio for source code. Treat this as an
# order of magnitude, not a budget - count exactly before you rely on it.
EST_TOKENS=$(( BYTES / 4 ))

printf '\n'
printf 'packed   %s\n' "$OUT"
printf 'files    %s\n' "${#FILES[@]}"
printf 'lines    %s\n' "$LINES"
printf 'bytes    %s\n' "$BYTES"
printf 'tokens   ~%s (estimated at 4 bytes/token)\n' "$EST_TOKENS"
printf '\n'

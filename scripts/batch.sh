#!/usr/bin/env bash
# batch.sh — convert every ARW with a paired JPEG under the given directories to Ultra HDR.
#
# usage: batch.sh [-j N] [-o OUTDIR] [-s] [--] SRC [SRC...]
#   -j N       parallel jobs (default 2 — each job needs ~1.5 GB RAM at 20 MP)
#   -o OUTDIR  write outputs here (default: next to the JPEG, as <base>_uhdr.jpg)
#   -s         skip pairs whose output already exists (cheap re-runs)
#   ARW2UHDR   env var to point at the binary (default: arw2uhdr on PATH)
#   EXTRA      env var with extra arw2uhdr flags, e.g. EXTRA="--gainmap rgb --strength 2"
#
# Pairing is by basename: DSC01234.ARW ↔ DSC01234.JPG (case-insensitive extension).
# Failures are logged, not fatal; a summary is printed at the end.
set -uo pipefail

JOBS=2
OUTDIR=""
SKIP=""
while getopts ":j:o:s" opt; do
  case "$opt" in
    j) JOBS="$OPTARG" ;;
    o) OUTDIR="$OPTARG" ;;
    s) SKIP="--skip-existing" ;;
    *) echo "usage: batch.sh [-j N] [-o OUTDIR] [-s] SRC [SRC...]" >&2; exit 2 ;;
  esac
done
shift $((OPTIND - 1))
[ $# -ge 1 ] || { echo "usage: batch.sh [-j N] [-o OUTDIR] [-s] SRC [SRC...]" >&2; exit 2; }

BIN="${ARW2UHDR:-arw2uhdr}"
command -v "$BIN" >/dev/null || { echo "batch.sh: $BIN not found (set ARW2UHDR=...)" >&2; exit 2; }
[ -z "$OUTDIR" ] || mkdir -p "$OUTDIR"

pairfile="$(mktemp)"
trap 'rm -f "$pairfile"' EXIT

# 1) discover pairs (-L follows symlinks)
find -L "$@" -type f \( -iname '*.arw' \) -print0 | sort -z |
while IFS= read -r -d '' arw; do
  base="${arw%.*}"
  for ext in JPG jpg JPEG jpeg; do
    if [ -f "$base.$ext" ]; then
      printf '%s\t%s\0' "$arw" "$base.$ext"
      break
    fi
  done
done > "$pairfile"

n=$(tr -cd '\0' < "$pairfile" | wc -c)
echo "batch.sh: $n pair(s) found, running $JOBS at a time"
[ "$n" -gt 0 ] || exit 0

# 2) convert (xargs fan-out; per-pair status lines)
export BIN OUTDIR SKIP
xargs -0 -P "$JOBS" -I{} bash -c '
  IFS=$'"'"'\t'"'"' read -r arw jpg <<< "{}"
  name="$(basename "${jpg%.*}")"
  out="${OUTDIR:+$OUTDIR/}${name}_uhdr.jpg"
  if "$BIN" $SKIP ${EXTRA:-} -o "$out" "$arw" "$jpg" >/dev/null 2>/tmp/batch_err_$$; then
    echo "OK    $name -> $out"
  else
    code=$?
    echo "FAIL  $name (exit $code): $(tail -1 /tmp/batch_err_$$)" >&2
  fi
  rm -f /tmp/batch_err_$$
' < "$pairfile"

echo "batch.sh: done"

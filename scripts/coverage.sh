#!/bin/sh
set -eu

root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
out="$root/coverage"
mkdir -p "$out"
work=$(mktemp -d "$out/run.XXXXXX")
trap 'rm -rf "$work"' 0
trap 'exit 1' HUP INT TERM
mkdir "$work/unit" "$work/e2e" "$work/fixture-data" "$work/merged"
packages=$(go list . ./internal/... | paste -sd, -)
cat > "$work/procguard-coverage-exec" << 'RUN'
#!/bin/sh
set -eu
GOCOVERDIR=$(CDPATH='' cd -- "$(dirname -- "$0")/unit" && pwd)
export GOCOVERDIR
exec "$@"
RUN
chmod +x "$work/procguard-coverage-exec"
PATH="$work:$PATH" go test -exec procguard-coverage-exec \
  -count=1 -covermode=atomic -coverpkg="$packages" \
  -coverprofile="$out/unit.out" . ./internal/... -args -test.gocoverdir="$work/unit"
go build -tags e2e -cover -covermode=atomic -coverpkg="$packages,./e2e/fixture" \
  -o "$work/fixture" ./e2e/fixture
go test -c -tags e2e -covermode=atomic -coverpkg="$packages" \
  -o "$work/e2e.test" ./e2e
GOCOVERDIR="$work/fixture-data" PROCGUARD_E2E_FIXTURE="$work/fixture" \
  PROCGUARD_E2E_ROOT="$root" "$work/e2e.test" -test.gocoverdir="$work/e2e"
set -- "$work/fixture-data"/covcounters.*
[ -f "$1" ] || {
  echo "Fixture produced no coverage counters." >&2
  exit 1
}
go tool covdata textfmt -pkg="$packages" -i="$work/e2e,$work/fixture-data" \
  -o="$out/e2e.out"
go tool covdata merge -pkg="$packages" \
  -i="$work/unit,$work/e2e,$work/fixture-data" -o="$work/merged"
go tool covdata textfmt -i="$work/merged" -o="$out/merged.out"
go tool cover -func="$out/merged.out" > "$out/functions.txt"
awk '
  NR == 1 { next }
  {
    split($1, location, ":")
    statements[location[1]] += $2
    if ($3 > 0) covered[location[1]] += $2
    total += $2
    if ($3 > 0) hit += $2
  }
  END {
    for (file in statements)
      printf "%s %.2f%% (%d/%d)\n", file, 100*covered[file]/statements[file], covered[file], statements[file]
    printf "TOTAL %.2f%% (%d/%d)\n", 100*hit/total, hit, total
  }
' "$out/merged.out" | sort | tee "$out/files.txt"
awk '
  NR == FNR { threshold = $1; next }
  FNR > 1 { total += $2; if ($3 > 0) hit += $2 }
  END {
    if (threshold <= 0 || threshold > 100 || total == 0) exit 2
    actual = 100*hit/total
    printf "Coverage gate: %.2f%% >= %.2f%%\n", actual, threshold
    if (actual < threshold) exit 1
  }
' scripts/coverage-threshold.txt "$out/merged.out"

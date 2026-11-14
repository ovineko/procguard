#!/bin/sh
set -eu
export GOMAXPROCS="${GOMAXPROCS:-2}"
fuzztime=${1:-60s}
for target in FuzzLimitsValidate FuzzSpecValidate FuzzOptionsValidate FuzzBackoff FuzzWatchParentValidate; do
  echo "Fuzzing $target ($fuzztime)"
  go test -run '^$' -fuzz="^$target$" -fuzztime="$fuzztime" .
done
if [ "$(go env GOOS)" = linux ]; then
  echo "Fuzzing FuzzParseProcStat ($fuzztime)"
  go test -run '^$' -fuzz='^FuzzParseProcStat$' -fuzztime="$fuzztime" .
fi
echo "Fuzzing FuzzDecode ($fuzztime)"
go test -run '^$' -fuzz='^FuzzDecode$' -fuzztime="$fuzztime" ./internal/e2eprotocol

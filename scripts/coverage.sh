#!/bin/sh
# Calcula a cobertura total (sem o ponto de entrada em cmd/) e falha abaixo
# do mínimo.
set -e
MIN=${MIN_COVERAGE:-80}
grep -v '/cmd/' coverage.out > coverage.filtered.out
TOTAL=$(go tool cover -func=coverage.filtered.out | awk '/^total:/ {gsub("%", "", $3); print $3}')
echo "Cobertura total: ${TOTAL}% (mínimo: ${MIN}%)"
awk -v t="$TOTAL" -v m="$MIN" 'BEGIN { exit (t + 0 >= m + 0) ? 0 : 1 }' || {
  echo "Cobertura abaixo do mínimo exigido" >&2
  exit 1
}

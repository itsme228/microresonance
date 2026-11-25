#!/usr/bin/env bash
# Циклический запуск диагностического теста.
# Вывод: peaks total/rejected/classified, tokens, H_initial/H_final, % разбора, reuse_rate.
# По выводу править A_cv/A_slope в ShapePipelineParams или дефолты в shape_pipeline.go.
# Использование: ./run_diagnostic_loop.sh [число_итераций]
set -e
cd "$(dirname "$0")"
cd ..
n=${1:-5}
for i in $(seq 1 "$n"); do
  echo "========== run $i/$n =========="
  GOCACHE=/tmp/go-build go test -v -run TestShapePipelineDiagnostic ./internal/core 2>&1 | grep -E "^(=== |runs=|peaks_|tokens_|H_|decompose_|reuse_|merge_|novelty|PASS|FAIL|---)"
done
echo "========== done =========="

# M9 Stage 3 v3 — LoCoMo Benchmark Summary

**Stack**: Phase D + M7 + M8 + M9 (date-aware extract, dual-speaker harness)
**Date**: $(date -u +%Y-%m-%d)

## Retrieval-Only (full, 1986 QAs)

| Metric | All cats (n=1986) | Excl cat-5 |
|--------|-------------------|------------|
| F1     | 0.0289        | 0.0307 |
| **LLM Judge** (headline) | **0.2674** | **0.3052** |
| hit@k  | 0.5196      | 0.5175 |

## Chat-50 (stratified, 5 cats × 10 convs)

| Metric | All cats (n=50) | Excl cat-5 |
|--------|-------------------|------------|
| F1     | 0.1468        | 0.1563 |
| **LLM Judge** (headline) | **0.6200** | **0.7000** |

## Files

- Retrieval predictions: `evaluation/locomo/results/m9-stage3-v3-retrieval.json`
- Retrieval score:       `evaluation/locomo/results/m9-stage3-v3-retrieval-score.json`
- Chat-50 predictions:   `evaluation/locomo/results/m9-stage3-v3-chat50-preds.json`
- Chat-50 score:         `evaluation/locomo/results/m9-stage3-v3-chat50-score.json`

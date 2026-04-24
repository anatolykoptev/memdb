## What's Changed

- M4: expose Phase D hyperparams as runtime env vars (#55) @anatolykoptev

## Features

- feat(add): configurable window\_chars per request (M7 Stream C, Option A) (#65) @anatolykoptev
- feat(chat): server-side answer\_style=factual (M7 Stream A) (#64) @anatolykoptev
- feat(d3): wire relation detector + surface silent edge-write errors (M5 follow-ups) (#58) @anatolykoptev
- feat(telemetry): M1 per-D-feature Prometheus counters (#53) @anatolykoptev

## Bug Fixes

- fix(d3): unblock memory\_edges on small cubes — wire admin reorg + include WorkingMemory (#57) @anatolykoptev

## Performance

- perf(handlers): batch embed calls in fast-add to remove window=512 latency cliff (#71) @anatolykoptev

## Tests

- test(d3): livepg integration test for runRelationPhase (#59) @anatolykoptev

## Documentation

- docs(process): compound-sprint orchestration pattern (M7 retro) (#70) @anatolykoptev
- docs(handlers): document window\_chars latency cliff in WindowChars godoc (#69) @anatolykoptev
- docs(plan): M7 compound lift sprint — multi-agent execution plan (#62) @anatolykoptev
- docs(roadmap): M7 next-session plan — compound lift (prompt + ingest) (#61) @anatolykoptev
- docs(locomo): M6 prompt ablation — +51% F1 via QA-specific system\_prompt (#60) @anatolykoptev
- docs(locomo): M4 combo tuning +8× F1 (#56) @anatolykoptev
- docs(locomo): M1+M2 closure + 5-category breakdown (#54) @anatolykoptev
- docs(locomo): M3 chat-mode F1 lift (+14×) (#52) @anatolykoptev
- docs(roadmap): Phase D closed + M3 chat-mode F1 jump documented (#51) @anatolykoptev
- docs: actualize roadmaps after v2.0.0 Phase D shipping (#50) @anatolykoptev

## Internal

- chore(locomo): m7 compound run stages 1+2 — F1 0.238 (+349% vs baseline, MemOS-tier) (#67) @anatolykoptev
- perf(handlers): m7 latency + pprof report (answer\_style=factual −52% p95) (#68) @anatolykoptev
- chore(testing): m7 regression report — no regressions across A/B/C (#66) @anatolykoptev
- chore(locomo): switch ingest to mode=raw for per-message granularity (M7 Stream B) (#63) @anatolykoptev

**Full Changelog:** https://github.com/anatolykoptev/memdb/compare/v2.0.0...v2.1.0


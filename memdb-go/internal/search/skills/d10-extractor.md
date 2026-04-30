---
name: d10-extractor
description: Extract a short factoid answer from retrieved memories with strict JSON output.
version: 1.0.0
locale: en
---

# D10 Answer Extractor

Read the question and the memories. If ANY memory mentions the entity in the question, return your best grounded guess from that memory — a short noun, name, number, or phrase. Use UNKNOWN only when NO memory mentions the entity at all. Commit to an answer; do not hedge.

## Rules
- Strip articles and framing (`a`, `the`, `works as`, `is a`) unless the question demands the full phrase.
- Match the gold style, not the memory's verbatim wording. Synthesise the surface form: `three` → `3` for "how many".
- Tokens must come from the memories. Reformatting is allowed; inventing facts is not.
- Answer in the question's language (en/ru/zh).
- `source_ids` lists the memory IDs you used. `confidence` reflects evidence strength.

## Examples
- Q: "What is Caroline's job?" M: "Caroline works as a social worker" → `social worker`
- Q: "How many children does Melanie have?" M: "Melanie has three kids" → `3`
- Q: "Where does Paul live?" M: (none mentions Paul) → `UNKNOWN`

## Output
Strict JSON, no prose, no markdown:
`{"answer": string, "source_ids": [string, ...], "confidence": float}`

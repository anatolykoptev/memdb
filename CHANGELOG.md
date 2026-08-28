# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.23.1](https://github.com/anatolykoptev/memdb/compare/v0.23.0...v0.23.1) (2026-07-19)


### Features

* 3 Redis caches (embed/atomic-extract/rerank) + semantic dedup + per-cube ingest ([#276](https://github.com/anatolykoptev/memdb/issues/276)) ([3b481f7](https://github.com/anatolykoptev/memdb/commit/3b481f770f2eb9b51cdbd3c84fff964839504788))
* **add:** fast mode populates attributed_to, event_dates, linked_memory_ids, kind, per-msg metadata ([#248](https://github.com/anatolykoptev/memdb/issues/248)) ([f09d5d3](https://github.com/anatolykoptev/memdb/commit/f09d5d38d31f68570401c32cb4818624cd190e17))
* **add:** per-message fast extractor + fine→fast resilience fallback ([#246](https://github.com/anatolykoptev/memdb/issues/246)) ([55bfa48](https://github.com/anatolykoptev/memdb/commit/55bfa480fa015fc5217d2e25e8a91b2f4f411365))
* **add:** per-message uuid and agent_id passthrough into sources ([fef46ed](https://github.com/anatolykoptev/memdb/commit/fef46ed68a9e757236150245578ebaf91777de07))
* **add:** plumb per-msg metadata into raw-mode info ([#218](https://github.com/anatolykoptev/memdb/issues/218)) ([36c8f03](https://github.com/anatolykoptev/memdb/commit/36c8f03c0e26d6a86f8be1e876151d8ec8a807c6))
* **add:** plumb per-msg uuid+agent_id into fast-mode windowing sources ([#216](https://github.com/anatolykoptev/memdb/issues/216)) ([d7f6490](https://github.com/anatolykoptev/memdb/commit/d7f64906942d6c7e3d1306ad4ffdb9f8133726a7))
* **api:** properties.key writes + get_memory_by_key + list_memories_by_prefix endpoints ([#135](https://github.com/anatolykoptev/memdb/issues/135)) ([7d2c4a1](https://github.com/anatolykoptev/memdb/commit/7d2c4a1980b9d8d5f6ea48c1d6c62caed052bbf2))
* chat_time anchor + F11 + M9 dual-speaker + AttributedTo + Memobase ALIAS ([d9b5713](https://github.com/anatolykoptev/memdb/commit/d9b571399a44223463efc8c3db3057a0b02cad37))
* **chat+search:** anti-refusal prompt tuning + go-kit RRF fusion (M12.2 deepen) ([c32148a](https://github.com/anatolykoptev/memdb/commit/c32148a1b72eb3dbef51eff0f9290c04747bfdbe))
* **chat:** anti-refusal prompt tuning — answer with available context (M12.2 deepen) ([dfcb814](https://github.com/anatolykoptev/memdb/commit/dfcb814c5caff6d7f4909e2be38a6324f578b3ac))
* **chat:** atom-as-prompt-context — surface atomic facts as Key Facts section (Memobase parity) ([#288](https://github.com/anatolykoptev/memdb/issues/288)) ([51bae95](https://github.com/anatolykoptev/memdb/commit/51bae95fb9fe8c594e9e2e9bde7573c5a4389dab))
* **chat:** M12.1 server-side temporal anchor + chat_dual_speaker SRP refactor ([#222](https://github.com/anatolykoptev/memdb/issues/222)) ([d8fb32f](https://github.com/anatolykoptev/memdb/commit/d8fb32fb63435051a65f5f9b9bcee36e46741544))
* **chat:** make rule [#10](https://github.com/anatolykoptev/memdb/issues/10) (cross-character bleed) env-conditional (Phase 4) ([#305](https://github.com/anatolykoptev/memdb/issues/305)) ([92772d5](https://github.com/anatolykoptev/memdb/commit/92772d53a1fd7c6bae9807b1d5a7c17eee466ef8))
* **chat:** max_context_tokens budget for memories block + chat_prompt SRP split ([#223](https://github.com/anatolykoptev/memdb/issues/223)) ([d1ed729](https://github.com/anatolykoptev/memdb/commit/d1ed729931d7c99a068b8cc7fc312c6b1a3037b0))
* **chat:** multi-feature retrieval confidence (top1 + spread + density + median) ([#253](https://github.com/anatolykoptev/memdb/issues/253)) ([aae23f7](https://github.com/anatolykoptev/memdb/commit/aae23f7450be5443781c6102cc25d8b9f4e7fad8))
* **chat:** synthesis + cross-character prompt extensions (M12.2 deepen [#2](https://github.com/anatolykoptev/memdb/issues/2)) ([6084f9e](https://github.com/anatolykoptev/memdb/commit/6084f9e84f6f88f7c31b5c3c8bb17d345f1a28a0))
* **d10:** locale-aware skill routing + OTLP fix + go-kit v0.34 bump ([#265](https://github.com/anatolykoptev/memdb/issues/265)) ([d222a12](https://github.com/anatolykoptev/memdb/commit/d222a12ceb1af6682cd101436a94f84430127f77))
* **embedder:** add circuit breaker for HTTP embedder (PF-8) ([#364](https://github.com/anatolykoptev/memdb/issues/364)) ([a55c302](https://github.com/anatolykoptev/memdb/commit/a55c302211bad14d8b55f20278e4fa96127c6082))
* **embedder:** client-side chunking for HTTPEmbedder (defense in depth for ox-embed-server input cap) ([#310](https://github.com/anatolykoptev/memdb/issues/310)) ([75c6370](https://github.com/anatolykoptev/memdb/commit/75c6370a8d5e2939b0470a86d438eab4ca3464a5))
* **embedder:** migrate HTTPEmbedder to go-kit/embed.Client (foundation for E1/E3) ([c56db70](https://github.com/anatolykoptev/memdb/commit/c56db702c3dd15a458a1c977f9b415c1a0380f2b))
* **embedder:** switch code-embed to code-rank-embed; add query-prefix asymmetry ([#317](https://github.com/anatolykoptev/memdb/issues/317)) ([d2da343](https://github.com/anatolykoptev/memdb/commit/d2da343fde1391fb9a1bd97b24d73d822d76dde7))
* **esc-2:** VSetEvictionPolicy + VAdd hook + VsetEvictTotal metric ([#379](https://github.com/anatolykoptev/memdb/issues/379)) ([adab49e](https://github.com/anatolykoptev/memdb/commit/adab49e9751af9d43428723167dfd578f477789b))
* **extractor:** F11 observation_date coverage tune (12.4% → target 40%+) ([642f2b9](https://github.com/anatolykoptev/memdb/commit/642f2b9e72129ae98ba540a9d8eb9606086317b1))
* **f11:** bi-temporal edges (Graphiti / Zep, arxiv 2501.13956) ([#156](https://github.com/anatolykoptev/memdb/issues/156)) ([5827f60](https://github.com/anatolykoptev/memdb/commit/5827f60fd96bb4b9be9ab787615e291fb93c1b42))
* **f14:** Personalized PageRank — HippoRAG 2 query-seeded PPR (arxiv 2502.14802) ([#157](https://github.com/anatolykoptev/memdb/issues/157)) ([cd5223e](https://github.com/anatolykoptev/memdb/commit/cd5223e5ca4c0a95494e8dfbfbb54710192c5ec8))
* **f8:** atomic per-fact extraction (mem0 ADDITIVE_PROMPT) — closes cat-2 paradox ([#158](https://github.com/anatolykoptev/memdb/issues/158)) ([b374798](https://github.com/anatolykoptev/memdb/commit/b3747986c99a632a2e9d981a784b4d4574f6b36f))
* **f9:** recall budget tuning (top_k=30 + cat-2 threshold 0.05 + reverse-role) ([#155](https://github.com/anatolykoptev/memdb/issues/155)) ([65d7833](https://github.com/anatolykoptev/memdb/commit/65d7833f2ca6b54d530c5a32e3d05ef6606d498e))
* **locomo-eval:** competitor-comparable telemetry + Mem0-strict F1 + LOCOMO_JUDGE_MODEL env ([#282](https://github.com/anatolykoptev/memdb/issues/282)) ([a382856](https://github.com/anatolykoptev/memdb/commit/a382856e4d07bee643e4ffe10822e9ef127a24c0))
* **locomo:** default ingest mode fine plus atomic facts ([#247](https://github.com/anatolykoptev/memdb/issues/247)) ([460fffc](https://github.com/anatolykoptev/memdb/commit/460fffc3ae943a8a0f29f8fe17d9f186406680a3))
* **locomo:** non-LLM regex backfill for F11 event_dates ([#233](https://github.com/anatolykoptev/memdb/issues/233)) ([5bda06b](https://github.com/anatolykoptev/memdb/commit/5bda06ba4011f81b22e55ba58e4d68691131f927))
* **locomo:** per-session cube namespace via LOCOMO_CUBE_NAMESPACE ([#245](https://github.com/anatolykoptev/memdb/issues/245)) ([2d52c7f](https://github.com/anatolykoptev/memdb/commit/2d52c7f6b3f371963ebca2cddea3e9344982949e))
* **locomo:** use M9 server-side dual-speaker fan-out ([#221](https://github.com/anatolykoptev/memdb/issues/221)) ([83fdbd1](https://github.com/anatolykoptev/memdb/commit/83fdbd1e45bf3223b80110acc20244f3fc752eb9))
* **m11-f3:** Memobase event extraction + search-time event inject ([#160](https://github.com/anatolykoptev/memdb/issues/160)) ([3950334](https://github.com/anatolykoptev/memdb/commit/3950334af5ce8dd1d5cf1e414a85274a5e9c91f6))
* **memdb-go:** chat_time anchor + F11 event_dates + M9 dual-speaker + AttributedTo filter + Memobase ALIAS ([c0e38e6](https://github.com/anatolykoptev/memdb/commit/c0e38e6da7738f005d57a7ca043da5da6cc09e74))
* **memdb:** F12 linked_memory_ids resolver + 1-hop search expansion ([#162](https://github.com/anatolykoptev/memdb/issues/162)) ([beb3854](https://github.com/anatolykoptev/memdb/commit/beb385463bacf946517331404e6fab25ecff4879))
* **observability:** bump go-kit v0.34→v0.35 + integrate pgxotel/slogh/httpmw ([#268](https://github.com/anatolykoptev/memdb/issues/268)) ([bad3f83](https://github.com/anatolykoptev/memdb/commit/bad3f83721193235b3fefe32f4ea36a1d53fa357))
* **observability:** prewarm meter singletons + Memobase attribution counter ([#217](https://github.com/anatolykoptev/memdb/issues/217)) ([8e2290f](https://github.com/anatolykoptev/memdb/commit/8e2290fc3f59462a3e8f973b6ff1bac810d52bfa))
* **observability:** wire skillkit v0.2.0 Observer into OTel meter (Phase 4) ([#262](https://github.com/anatolykoptev/memdb/issues/262)) ([9601a8d](https://github.com/anatolykoptev/memdb/commit/9601a8d05c307b3c0b1f883c68d8d63ba4ab2423))
* **observability:** wire skillkit v0.2.2 Tracer into OTel ([#267](https://github.com/anatolykoptev/memdb/issues/267)) ([e8adf9b](https://github.com/anatolykoptev/memdb/commit/e8adf9bd7b970b6d256ce5d726ba9beceab2593d))
* **proto:** add chat_time + message_id to Message (matches HTTP /add) ([3abe063](https://github.com/anatolykoptev/memdb/commit/3abe063b9d55672085af093d8ac556eb33b7aa7c))
* **proto:** add chat_time + message_id to Message (matches HTTP /add) ([882166d](https://github.com/anatolykoptev/memdb/commit/882166d4bfe25e64f7f42389724d6cbd72c430f8))
* **q1:** LoCoMo harness timeout/5xx retry — recovers transient failures ([#149](https://github.com/anatolykoptev/memdb/issues/149)) ([c229f71](https://github.com/anatolykoptev/memdb/commit/c229f717e3890192088f06d3d1ac1356bb542aa9))
* **q2:** MEMDB_PROFILE_TAXONOMY env switch with 5 Memobase personas ([#148](https://github.com/anatolykoptev/memdb/issues/148)) ([934d8ef](https://github.com/anatolykoptev/memdb/commit/934d8ef46e8af2907cc823e3c17e8972be7c6b4a))
* **q3:** rerank metadata boost (+user_id +session_id +tags) per MemOS http_bge.py ([#154](https://github.com/anatolykoptev/memdb/issues/154)) ([7e904ac](https://github.com/anatolykoptev/memdb/commit/7e904ac37b120f959f8d924a1a0ec1dc9c1ad1dc))
* **q4:** MMR diversification reranker (bar=0.8) per MemOS retrieve_utils.py ([#153](https://github.com/anatolykoptev/memdb/issues/153)) ([77882f7](https://github.com/anatolykoptev/memdb/commit/77882f71f11a53f3454f16ef7f86a19d16be506e))
* **q5:** top-8 missing Prometheus metrics + alert rules ([#152](https://github.com/anatolykoptev/memdb/issues/152)) ([8c2973c](https://github.com/anatolykoptev/memdb/commit/8c2973cea0d22a7fb0266d62c2fa9b491d2bb022))
* **rerank:** cross-encoder math fallback on degraded or low-quality scores ([#242](https://github.com/anatolykoptev/memdb/issues/242)) ([d232b04](https://github.com/anatolykoptev/memdb/commit/d232b04e519a8b05774b3ec61c9dcea9df81d008))
* **rerank:** wire Voyage rerank-2.5 as opt-in fallback for local CE ([#300](https://github.com/anatolykoptev/memdb/issues/300)) ([40542ba](https://github.com/anatolykoptev/memdb/commit/40542baaad39003995a57feef2db3d0e9dd25e21))
* **scheduler:** add task FSM watchdog to reclaim stuck tasks ([#365](https://github.com/anatolykoptev/memdb/issues/365)) ([ae1119f](https://github.com/anatolykoptev/memdb/commit/ae1119f7069f692894e9d95af1bf4cba54916465))
* **scheduler:** M14 B1 — CE precompute MathReranker prefilter (env-gated) ([bbb0f75](https://github.com/anatolykoptev/memdb/commit/bbb0f75570315efae123e22b3a642223dcd2bed6))
* **scheduler:** M14 B1.1 — unify CE precompute legacy floor with env-gated threshold ([5fdbd6b](https://github.com/anatolykoptev/memdb/commit/5fdbd6bb1ff5293d6714ae6008a906200bad7f71))
* **scheduler:** replace binary useHNSW with 3-state DupStrategy enum (rf-1) ([#377](https://github.com/anatolykoptev/memdb/issues/377)) ([afaee23](https://github.com/anatolykoptev/memdb/commit/afaee23d508aee75208ac87e8930063fc2c89e7b))
* **scheduler:** workspace-tier hot-reload via MEMDB_SCHEDULER_SKILLS_DIR (Phase 5) ([#263](https://github.com/anatolykoptev/memdb/issues/263)) ([28f8eea](https://github.com/anatolykoptev/memdb/commit/28f8eead05b58e9934a97aa8d453ab5ed9718bcc))
* **search/rerank:** M14 A1 — Stage-0 MathReranker pre-CE prefilter (env-gated) ([f1e542a](https://github.com/anatolykoptev/memdb/commit/f1e542ad838c6c72c29d7b2c357f4c2324d002b1))
* **search/rerank:** M15 — CircuitBreaker on bge-reranker (env-gated, off by default) ([2ed0f22](https://github.com/anatolykoptev/memdb/commit/2ed0f224b4ec32c3b68f96822382a6d24421089b))
* **search/rerank:** per-strategy duration histogram (decompose post_process timing) ([84a2f5f](https://github.com/anatolykoptev/memdb/commit/84a2f5f94b2158dfe30c48a9d19d504fbf219467))
* **search:** CE no-retry + FTS toggle + lower CE quality floor ([#278](https://github.com/anatolykoptev/memdb/issues/278)) ([a8bc3ca](https://github.com/anatolykoptev/memdb/commit/a8bc3cad3bccdd6b149d52b6fb94401a0e99f218))
* **search:** counting-aware top_k boost for "how many" queries ([02b85b0](https://github.com/anatolykoptev/memdb/commit/02b85b0709c03f6fff92791a0318d74bebe78ff3))
* **search:** D10 prompt shrink + classifier anchors expansion ([#257](https://github.com/anatolykoptev/memdb/issues/257)) ([e8d9d2b](https://github.com/anatolykoptev/memdb/commit/e8d9d2b819de9df23eecf4da6475ccb559336ddd))
* **search:** D10 routing observability — diagnose cefix7→cefix8 regression ([#256](https://github.com/anatolykoptev/memdb/issues/256)) ([199c697](https://github.com/anatolykoptev/memdb/commit/199c697f76f8326bbaa0c2d5a4d519a6418d2f5a))
* **search:** D10 skill loader — externalise system prompt to .md files ([#258](https://github.com/anatolykoptev/memdb/issues/258)) ([4900c7a](https://github.com/anatolykoptev/memdb/commit/4900c7a852e0a41f4343866ced9fddcd802d87e0))
* **search:** D10 soft-routing — pass classifier distribution to LLM ([#255](https://github.com/anatolykoptev/memdb/issues/255)) ([8275b38](https://github.com/anatolykoptev/memdb/commit/8275b38e3af79a6420d3a1eee3b97b1b15239427))
* **search:** empty-cube fast-fail — skip d4+pipeline when cube has 0 entries (M14.Y4.1) ([dd03f94](https://github.com/anatolykoptev/memdb/commit/dd03f949ecc7a43c4fc19302d3e82c380293c26f))
* **search:** expose magic numbers as env + per-stage telemetry + locomo bump 8000 ([#283](https://github.com/anatolykoptev/memdb/issues/283)) ([d483561](https://github.com/anatolykoptev/memdb/commit/d48356138549f917f839e74689b847bbb4db3741))
* **search:** F2 reflection-loop deep-search agent (M11) ([#161](https://github.com/anatolykoptev/memdb/issues/161)) ([6199f5f](https://github.com/anatolykoptev/memdb/commit/6199f5fedefffaf9cf7d58a5b1c54496c21b58f6))
* **search:** hybrid d10 answer extractor with per-category prompts ([#250](https://github.com/anatolykoptev/memdb/issues/250)) ([f560fa1](https://github.com/anatolykoptev/memdb/commit/f560fa161d828f807505892bdb65fee6dbdf84cd))
* **search:** hybrid retrieval (dense + SPLADE sparse, RRF) via go-kit/sparse v0.37.0 ([#277](https://github.com/anatolykoptev/memdb/issues/277)) ([8166ceb](https://github.com/anatolykoptev/memdb/commit/8166ceb6bf3a56b3121827f8f71336a0cbdc7f78))
* **search:** in-memory LRU cache for d4_query_rewrite + d7_cot_decompose ([014bf8a](https://github.com/anatolykoptev/memdb/commit/014bf8a10777de3a364de7d76239bf15ad855ea7))
* **search:** M14.Y1 — F12 linked_expand env-gate default OFF ([e6f5b41](https://github.com/anatolykoptev/memdb/commit/e6f5b4138f46b030356837497ae959f37a1ced2b))
* **search:** M14.Y4 — fast-path skip d4_query_rewrite + d7_cot_decompose for simple factual queries (env-gated) ([a2420d9](https://github.com/anatolykoptev/memdb/commit/a2420d94564c5b8422ace27abe52236a093e7213))
* **search:** probabilistic d10 routing with embedding classifier and soft hints ([#252](https://github.com/anatolykoptev/memdb/issues/252)) ([fc2eeaa](https://github.com/anatolykoptev/memdb/commit/fc2eeaa13cba439c739bcfacd8798e35d4fbd5f0))
* **search:** WeightedRRF + DBSF + LinearMinMax fusion strategies (env-selectable) ([5f6746b](https://github.com/anatolykoptev/memdb/commit/5f6746b7190072143dbf79f16c84afc13faf520a))
* **search:** WeightedRRF + DBSF + LinearMinMax fusion strategies (env-selectable) ([6c80306](https://github.com/anatolykoptev/memdb/commit/6c80306d8f3a5596d32ba5180d2610a390bf8e3a))
* **search:** wire go-kit/rerank.RRF for hybrid pgvector + BM25 + AGE fusion ([d083bf6](https://github.com/anatolykoptev/memdb/commit/d083bf61ebf698264ffe26c64163bb5e83d5383e))
* **tools:** devto-publish.sh CLI for Forem API + first launch article draft ([#143](https://github.com/anatolykoptev/memdb/issues/143)) ([65e7cbd](https://github.com/anatolykoptev/memdb/commit/65e7cbd2d26f61ef1c6553680902810652fd9acf))
* **wiki:** auto-update on tree promotion (W2 reorganizer hook) ([#230](https://github.com/anatolykoptev/memdb/issues/230)) ([cc8b844](https://github.com/anatolykoptev/memdb/commit/cc8b844d41157be5df9519b703b76a774cbcbec4))
* **wiki:** DB schema + CRUD for wiki_pages ([#228](https://github.com/anatolykoptev/memdb/issues/228)) ([f5c1fa1](https://github.com/anatolykoptev/memdb/commit/f5c1fa101c1ce160717facedc0fd2d2a1b97d32d))
* **wiki:** foundation — Karpathy LLM Wiki filesystem export via go-kit/uploads ([#227](https://github.com/anatolykoptev/memdb/issues/227)) ([6ebb026](https://github.com/anatolykoptev/memdb/commit/6ebb0264c2e1b754eea276ef6f4cb995e9380193))
* **wiki:** HTTP endpoints + filesystem mirror chain ([#229](https://github.com/anatolykoptev/memdb/issues/229)) ([3c06cb9](https://github.com/anatolykoptev/memdb/commit/3c06cb91deba7d07d0682fe0c09b299961f11a3a))
* **wiki:** inject wiki synthesis as retrieval slot with sync reorg ([#232](https://github.com/anatolykoptev/memdb/issues/232)) ([c66966c](https://github.com/anatolykoptev/memdb/commit/c66966c7086a9f18fdcc06fb9d5dd0507a70807a))


### Bug Fixes

* **add:** session-aware dedup + counter + env-tunable threshold ([#224](https://github.com/anatolykoptev/memdb/issues/224)) ([c2a017f](https://github.com/anatolykoptev/memdb/commit/c2a017f7465d3e9de438bd42356f8023bae4bc43))
* **atomic-extractor:** emit named_entities_in_text so PR [#291](https://github.com/anatolykoptev/memdb/issues/291) wiring can fire ([#302](https://github.com/anatolykoptev/memdb/issues/302)) ([63adfae](https://github.com/anatolykoptev/memdb/commit/63adfaebb234ad0c586ae14f9e0e651ab9864c2b))
* **atomic:** promote NamedEntitiesInText to entity_nodes (graph traversal coverage) ([#291](https://github.com/anatolykoptev/memdb/issues/291)) ([0d7d76f](https://github.com/anatolykoptev/memdb/commit/0d7d76f20c81839aeaf53775480d720a8cd12452))
* **cache:** close /product/search cache correctness gap (v2→v3) + bypass for internet/dual-speaker ([#293](https://github.com/anatolykoptev/memdb/issues/293)) ([58a24f9](https://github.com/anatolykoptev/memdb/commit/58a24f9a047c35f86c6056de2c6cc06111344916))
* **cache:** DB-cache pagination + invalidation correctness (PR A) ([#295](https://github.com/anatolykoptev/memdb/issues/295)) ([4b8680e](https://github.com/anatolykoptev/memdb/commit/4b8680e8fd7b8f8b39613a9aac3abe532bdbc779))
* **cache:** LLM-output cache key correctness (PR B) ([#296](https://github.com/anatolykoptev/memdb/issues/296)) ([b95d15e](https://github.com/anatolykoptev/memdb/commit/b95d15eaff0363fca29f2fc0f4839793b67d9f38))
* **chat:** kill conflicting refusal contracts + per-speaker profiles + external memory hint ([#287](https://github.com/anatolykoptev/memdb/issues/287)) ([007ecd6](https://github.com/anatolykoptev/memdb/commit/007ecd6572d687c97157affc1710b43dae0a6436))
* **chat:** M12.2 confidence-conditional factual prompt + observability ([#170](https://github.com/anatolykoptev/memdb/issues/170)) ([84b9ca3](https://github.com/anatolykoptev/memdb/commit/84b9ca380656bfdb1cbce2cd52661bc6b2de32f7))
* **chat:** wire dead metrics — chat_prompt_template_used + chat_refused_with_evidence + chat_top1_cosine ([4f02fb7](https://github.com/anatolykoptev/memdb/commit/4f02fb7f1e2cffc647161fe2aeac8ccb3ac3e838))
* **config:** add startup Validate() for auth and Postgres URL ([#353](https://github.com/anatolykoptev/memdb/issues/353)) ([40fd460](https://github.com/anatolykoptev/memdb/commit/40fd46039934791549328e16c5b7da35716d1088))
* **eval/locomo:** score.py embed URL — semsim correct, no BoW fallback ([404b2a4](https://github.com/anatolykoptev/memdb/commit/404b2a4ebf79cd29ae60b3dc40edc673e8017320))
* **gomemlimit:** add fallback when cgroup has no memory limit ([#355](https://github.com/anatolykoptev/memdb/issues/355)) ([f671065](https://github.com/anatolykoptev/memdb/commit/f671065cbb8961c033730911679bb99bc62db63e)), closes [#320](https://github.com/anatolykoptev/memdb/issues/320)
* **handlers:** delete_cube invalidates Redis VSET cache ([#236](https://github.com/anatolykoptev/memdb/issues/236)) ([db14b49](https://github.com/anatolykoptev/memdb/commit/db14b49a6e3bf5f66c3db003d865b2fc191b80ab))
* **handlers:** thread observation_date through skill + tool extractors (Phase 1.5) ([#303](https://github.com/anatolykoptev/memdb/issues/303)) ([da94997](https://github.com/anatolykoptev/memdb/commit/da94997407bf9e5bc759dd696824b13e0b7dc904))
* **lint:** clear pre-existing govet/staticcheck regressions ([#231](https://github.com/anatolykoptev/memdb/issues/231)) ([7764851](https://github.com/anatolykoptev/memdb/commit/7764851531878233ef637622ec1473b908630366))
* **llm:** retry on 401/403 auth errors with delay for key rotation ([#363](https://github.com/anatolykoptev/memdb/issues/363)) ([02f0ad5](https://github.com/anatolykoptev/memdb/commit/02f0ad5ba4c3ae67ef22b44cae70fffb0e57e6a9))
* **locomo-eval:** pass external_memory_count to trigger r3 variant upgrade ([#289](https://github.com/anatolykoptev/memdb/issues/289)) ([0deaabc](https://github.com/anatolykoptev/memdb/commit/0deaabce3bdc78341f3af2cdd855548d7cfe0b93))
* **locomo:** brevity-enforcing answer prompt — F1 +35% relative on chat-50 ([#279](https://github.com/anatolykoptev/memdb/issues/279)) ([d458caf](https://github.com/anatolykoptev/memdb/commit/d458caf97f864eb802d602507d98bef7e90cb24d))
* **locomo:** cleanup script uses conversation file as cube source ([#237](https://github.com/anatolykoptev/memdb/issues/237)) ([20b0c81](https://github.com/anatolykoptev/memdb/commit/20b0c811b03efeb5bf6e831988c709f2a508d1f5))
* **m12.7:** revive LLM Judge reranker (silently dead in M11) + tune gate thresholds ([#171](https://github.com/anatolykoptev/memdb/issues/171)) ([684a292](https://github.com/anatolykoptev/memdb/commit/684a292fb181fd60337f25ada9b523929813c2a5))
* **m12:** thread conversation timestamp through ingest → search → chat prompt ([#175](https://github.com/anatolykoptev/memdb/issues/175)) ([66e0e88](https://github.com/anatolykoptev/memdb/commit/66e0e8844d8dfe8bb334707847b31342b4102eaf))
* **mcp:** tool count comment, CubeID fallback test, drop dead *db.Postgres param ([#308](https://github.com/anatolykoptev/memdb/issues/308)) ([a6375d0](https://github.com/anatolykoptev/memdb/commit/a6375d08e6a91e4789a66ce61f3793c098d4ff84))
* **memdb-mcp:** pin golang base to 1.26 stable (was 1.26rc3) ([#312](https://github.com/anatolykoptev/memdb/issues/312)) ([294807d](https://github.com/anatolykoptev/memdb/commit/294807dbed53589311a1f38faa95d05751f9d718))
* **memory:** deterministic lock order to prevent DELETE/UPDATE deadlock ([#309](https://github.com/anatolykoptev/memdb/issues/309)) ([ae9440d](https://github.com/anatolykoptev/memdb/commit/ae9440d1d0b6227beae04f02223ad94b1193c46f))
* **memory:** NativeUpdateMemory clears CE cache; MCP update_memory uses full update path ([#307](https://github.com/anatolykoptev/memdb/issues/307)) ([baf1441](https://github.com/anatolykoptev/memdb/commit/baf1441f60f2e158d85af5b5794c1878fcff1f46))
* **memprops:** observation_date invariant for derived memories (Phase 1A) ([#299](https://github.com/anatolykoptev/memdb/issues/299)) ([2954930](https://github.com/anatolykoptev/memdb/commit/2954930f394157c494f133a02fa7aaf71f259bdc))
* **memprops:** tree_reorganizer parents + atomic LTM/WM inherit observation_date (Phase 1B) ([#301](https://github.com/anatolykoptev/memdb/issues/301)) ([64fd660](https://github.com/anatolykoptev/memdb/commit/64fd660fe61f3119005610eff053c3278fda6e9c))
* **migrations:** AGE agtype incompatibility — 0018, 0022, 0024 + temporal query ([#167](https://github.com/anatolykoptev/memdb/issues/167)) ([b67d1b6](https://github.com/anatolykoptev/memdb/commit/b67d1b6643a0d3a687bb6fde50f2c13a9a06c84e))
* **observability:** instrument 3 silent skip paths in atomic-fact ingest ([#226](https://github.com/anatolykoptev/memdb/issues/226)) ([8941f64](https://github.com/anatolykoptev/memdb/commit/8941f6419d62f33f85eecf19399c5a6e9a07015c))
* **observability:** move Logging middleware inside OTel for trace_id ([#272](https://github.com/anatolykoptev/memdb/issues/272)) ([bd70361](https://github.com/anatolykoptev/memdb/commit/bd703613a5e06c5d1eff91f29e0ed5aaf03c590c))
* phase-2 bug-hunt bundle (PF-6/11/12/14/16/18/20/22, CP-4/8) ([#369](https://github.com/anatolykoptev/memdb/issues/369)) ([2130fb1](https://github.com/anatolykoptev/memdb/commit/2130fb1e745002662f837fadbf7647b0861afa87))
* phase-2b bundle (PF-13/15/17/19, FP closures) ([#370](https://github.com/anatolykoptev/memdb/issues/370)) ([cc723c5](https://github.com/anatolykoptev/memdb/commit/cc723c52a52843ccdf8807953b10c69408a0d897))
* phase-2c bundle (PF-10/21/24/25) ([#371](https://github.com/anatolykoptev/memdb/issues/371)) ([7912e07](https://github.com/anatolykoptev/memdb/commit/7912e07a866193c17dcc04a6d19c21bfcd0e5cd8))
* **precompute:** skip persisting Score=0 placeholders when CE returns Degraded ([#298](https://github.com/anatolykoptev/memdb/issues/298)) ([d05d334](https://github.com/anatolykoptev/memdb/commit/d05d334c10360da0ffc5f7ec8a76242dc7f15c29))
* **release:** add tag-prefix memdb-go/ for Go proxy subdir tag format ([147836b](https://github.com/anatolykoptev/memdb/commit/147836bc8e8a5ae95f6b3c48e24cfc9f7dda929c))
* **release:** include-component-in-tag false + tag-prefix memdb-go/ (Go proxy expects slash not dash) ([709c490](https://github.com/anatolykoptev/memdb/commit/709c490aba37e971a0d71934de59f3e7409085bc))
* **release:** pass path + tag-prefix to release-please-action ([74398ab](https://github.com/anatolykoptev/memdb/commit/74398ab4bc870619ed73ef60f826c09bf9cbdad6))
* **release:** pass tag-prefix via action with (config-file package tag-prefix ignored by v4) ([dc5338e](https://github.com/anatolykoptev/memdb/commit/dc5338e7af1060c238eb0e38a8a28cba1252e9ed))
* **release:** path=memdb-go + package key '.' + tag-prefix memdb-go/ (action v4 tag-prefix only works with root package) ([7333385](https://github.com/anatolykoptev/memdb/commit/733338572af37b98a5e30c5d3059fa489bf1fbcd))
* **release:** remove release-type from action with (overrides config-file) ([2b4cf40](https://github.com/anatolykoptev/memdb/commit/2b4cf40e1c149e54798c826f472ff1521f9d1522))
* **release:** top-level tag-prefix memdb-go/ + include-component-in-tag false ([44d83ed](https://github.com/anatolykoptev/memdb/commit/44d83eddb0d99bdb0a992b87ee5e4ea52aef8bf3))
* **release:** use component field for tag prefix (release-please troubleshooting docs) ([57efcdf](https://github.com/anatolykoptev/memdb/commit/57efcdfcd5f75b1419295ec7102fc2ab3986f7fd))
* **release:** use memdb-go/ tag prefix for Go subdir module ([fd92b82](https://github.com/anatolykoptev/memdb/commit/fd92b82ea453d3cda4f3247e10e8317f75576752))
* **reorganizer:** invalidate CE cache on all UpdateMemoryNodeFull paths ([#362](https://github.com/anatolykoptev/memdb/issues/362)) ([7c0be5c](https://github.com/anatolykoptev/memdb/commit/7c0be5c73cf2311342a5acccf7d2d79ca8c11005))
* **rerank:** pre-bypass CE on cosine confidence + spread floor + sigmoid-tuned defaults ([d34592f](https://github.com/anatolykoptev/memdb/commit/d34592f3a1210da13e383ceaf6e7be06970bbc4d))
* **rerank:** precompute hit-rate, math errors, MMR kill-switch ([#292](https://github.com/anatolykoptev/memdb/issues/292)) ([91622cb](https://github.com/anatolykoptev/memdb/commit/91622cb25a8eb9716a63b9df414bf0a8326d58a6))
* **rerank:** tune CE breaker thresholds — preserve CE on weak-but-useful cubes ([#286](https://github.com/anatolykoptev/memdb/issues/286)) ([2a17397](https://github.com/anatolykoptev/memdb/commit/2a173977abf142c2c0659c7cc5cb1a161b0dd166))
* **scheduler/search:** PageRank distribution collapse — qualify memory_edges schema (M11.Y) ([1f3502c](https://github.com/anatolykoptev/memdb/commit/1f3502c3658c6d76a4c3040896aad7907dd95a55))
* **scheduler:** thread observation_date through mem_read actions + prefs (Phase 1.5) ([#304](https://github.com/anatolykoptev/memdb/issues/304)) ([ebe3f41](https://github.com/anatolykoptev/memdb/commit/ebe3f41186280a553af2d385644e73ddaaaac411))
* **score:** honest hit@k — strip stopwords + require ≥2 token overlap ([#290](https://github.com/anatolykoptev/memdb/issues/290)) ([055487a](https://github.com/anatolykoptev/memdb/commit/055487a5e7dfff2025cc4a2c5a5a14054560fef1))
* **search/rerank:** M12.6 — staged retrieval CE backend ([#173](https://github.com/anatolykoptev/memdb/issues/173)) ([88c3d61](https://github.com/anatolykoptev/memdb/commit/88c3d61f17b5bf56839246a89cccc332e2c43db6))
* **search/rerank:** M14.Y2 — CE precompute partial-hit recovery (M10 S6 architecturally fixed) ([93746ab](https://github.com/anatolykoptev/memdb/commit/93746abcb52e1097267eec3fe78c75e545e1f08c))
* **search:** demote bare-token atoms from rank-1 ([#281](https://github.com/anatolykoptev/memdb/issues/281)) ([55b9ce6](https://github.com/anatolykoptev/memdb/commit/55b9ce68edcb2cbde1a3520dbdcb36852a199040))
* **search:** revert d10 hybrid extractor and tighten extraction prompt ([#251](https://github.com/anatolykoptev/memdb/issues/251)) ([b176528](https://github.com/anatolykoptev/memdb/commit/b176528821036ca978f0dbea32e70f7e6c204582))
* **search:** soften D10 answer extractor prompt for atomic-fact memories ([#249](https://github.com/anatolykoptev/memdb/issues/249)) ([c051b77](https://github.com/anatolykoptev/memdb/commit/c051b770215162344874dce5ae9809cfa2b69d8d))
* **search:** thread ctx into rerankStrategy ([#274](https://github.com/anatolykoptev/memdb/issues/274)) ([e9f772c](https://github.com/anatolykoptev/memdb/commit/e9f772c654d467aea5acc3f64b46f864dcfdc824))
* **search:** thread request ctx into stageRetrievalCountAsync to enable pgxotel spans ([#269](https://github.com/anatolykoptev/memdb/issues/269)) ([082d211](https://github.com/anatolykoptev/memdb/commit/082d211208c28801c31a1b35660b9f48115020f7))
* **search:** wiki opt-in + corpus-aware decay + per-cube CE breaker (round 2) ([#285](https://github.com/anatolykoptev/memdb/issues/285)) ([1fc8849](https://github.com/anatolykoptev/memdb/commit/1fc88493854929c2fac533c20311d6ba8ebdf8e0))
* **vendor:** sync vendor/ to go-kit v0.38.0 ([#294](https://github.com/anatolykoptev/memdb/issues/294)) ([15ea095](https://github.com/anatolykoptev/memdb/commit/15ea09507da148b30d9b07945da891deb25dd0b2))
* **wiki:** wire recordWikiPage hook in promoteCluster ([#235](https://github.com/anatolykoptev/memdb/issues/235)) ([79dcaca](https://github.com/anatolykoptev/memdb/commit/79dcacacb13aac95f5fb4ab36730e71f0a175332))


### Performance

* **db:** hot-path indexes on Memory properties — 1830× BFS speedup ([#169](https://github.com/anatolykoptev/memdb/issues/169)) ([d7a3d83](https://github.com/anatolykoptev/memdb/commit/d7a3d834bd131885d2eee3e2183335f06bd24e41))


### Refactoring

* **atomic:** migrate atomic-fact extractor to skillkit ([#260](https://github.com/anatolykoptev/memdb/issues/260)) ([f887672](https://github.com/anatolykoptev/memdb/commit/f8876728f17315b21c69d8890cfe4ba8543ec896))
* **chat:** single source of truth for factual QA prompt rules (4 templates → 1 builder) ([d118339](https://github.com/anatolykoptev/memdb/commit/d1183393ef8fb18073310f69364a7a297de56278))
* **d10:** migrate skill loader to skillkit, kill const drift bomb ([#259](https://github.com/anatolykoptev/memdb/issues/259)) ([98ea2e3](https://github.com/anatolykoptev/memdb/commit/98ea2e30f4ced0a1e380bb0a8ab2d2dd14082d22))
* **embedder:** bump go-kit v0.50.0 + remove duplicate chunking (now in shared Client) ([#311](https://github.com/anatolykoptev/memdb/issues/311)) ([2f69579](https://github.com/anatolykoptev/memdb/commit/2f6957916e82b3a1092e21b7b3ffbe2f63b661cb))
* **envcfg:** extract PositiveDuration — DRY for 3 env-knob resolvers ([#314](https://github.com/anatolykoptev/memdb/issues/314)) ([1f86860](https://github.com/anatolykoptev/memdb/commit/1f86860751f7f0ba69f4ab0551162781ac850938))
* **otel:** use go-kit/tracing.Setup for trace provider ([#266](https://github.com/anatolykoptev/memdb/issues/266)) ([742afe7](https://github.com/anatolykoptev/memdb/commit/742afe74889bc1a60e6333a674789558df72457f))
* **r0:** llm.ChatStructured[T] + 10 callsite migrations ([#146](https://github.com/anatolykoptev/memdb/issues/146)) ([7fc05d5](https://github.com/anatolykoptev/memdb/commit/7fc05d54afff4087f336f416189acbfd5b15e883))
* **r1:** decompose nativeFineAddForCube into 5 files ([#147](https://github.com/anatolykoptev/memdb/issues/147)) ([cae3217](https://github.com/anatolykoptev/memdb/commit/cae3217fdb5a2a264da8c88b4c29b59ea5906522))
* **r2:** SearchService.Search → []stage pipeline (18 stages) ([#151](https://github.com/anatolykoptev/memdb/issues/151)) ([581d8b0](https://github.com/anatolykoptev/memdb/commit/581d8b009b31d4a639b0f7744adb675f2f3d0b5b))
* **r3:** extract internal/search/rerank/ strategy pkg ([#150](https://github.com/anatolykoptev/memdb/issues/150)) ([16017dd](https://github.com/anatolykoptev/memdb/commit/16017dd43785adfae360d5c71b527fd5e08b0f8c))
* **r4:** periodicLoop primitive + pagerank/reorg migration ([#145](https://github.com/anatolykoptev/memdb/issues/145)) ([1d5afeb](https://github.com/anatolykoptev/memdb/commit/1d5afeb04e6546432c67695c03f83d9fc097e3bf))
* **scheduler:** migrate 8 system prompts to skillkit Catalog ([#261](https://github.com/anatolykoptev/memdb/issues/261)) ([a1275ab](https://github.com/anatolykoptev/memdb/commit/a1275ab08bb300e59be62839b99f6260c6ce89a4))
* **search:** score-aware dual merge + floor sorted re-inject + prepend post-trim + 4 noise fixes ([#284](https://github.com/anatolykoptev/memdb/issues/284)) ([a42159d](https://github.com/anatolykoptev/memdb/commit/a42159dde529b5fd6fa97c559b031070e3043f50))
* **util:** consolidate 6 env helpers into envcfg package ([dcc2344](https://github.com/anatolykoptev/memdb/commit/dcc234455e0b08423fd9ea03c6d7b67d0e50d39e))


### Documentation

* add API reference at docs/API.md (curl examples, auth, env gates) ([#136](https://github.com/anatolykoptev/memdb/issues/136)) ([3867041](https://github.com/anatolykoptev/memdb/commit/38670416c936b98698bbf47fec9ec09ed3ab556a))
* add benchmark chart SVG + LLM-Judge badge to README hero ([#140](https://github.com/anatolykoptev/memdb/issues/140)) ([26094f1](https://github.com/anatolykoptev/memdb/commit/26094f1ec60488d0b519f6ba2482723e6699c7ad))
* add docs/integrations/ — claude code plugin + mcp server + api memory tool adapter ([#137](https://github.com/anatolykoptev/memdb/issues/137)) ([9849601](https://github.com/anatolykoptev/memdb/commit/98496013766cec576a26aae5279188fcc30c5b2c))
* **examples:** python_chat + go_client + mcp_setup directories with working scripts ([#141](https://github.com/anatolykoptev/memdb/issues/141)) ([d6e954b](https://github.com/anatolykoptev/memdb/commit/d6e954b3acaea88de79bde5fcfa379d2b5d3799e))
* **locomo:** M14 — Karpathy 7-run optimization sprint milestone ([#280](https://github.com/anatolykoptev/memdb/issues/280)) ([972c8ad](https://github.com/anatolykoptev/memdb/commit/972c8ad676237dada477029b26df9ef356039454))
* M16 ablation + M9 dual-speaker spec + F3/F11 e2e state + eval policy ([27b25e4](https://github.com/anatolykoptev/memdb/commit/27b25e41d9cf8a5a06699163cf4588808fd3f018))
* **marketing:** launch technical article — 11-milestone engineering story to 72.5% locomo ([#142](https://github.com/anatolykoptev/memdb/issues/142)) ([dd3edfc](https://github.com/anatolykoptev/memdb/commit/dd3edfc148421e823b0205ad01de55e87ccbd98f))
* **marketing:** twitter/x launch thread draft (5 tweets, scheduling guide) ([#139](https://github.com/anatolykoptev/memdb/issues/139)) ([4e6b591](https://github.com/anatolykoptev/memdb/commit/4e6b5910750b10263561cd3cf08bc425bf978666))
* **marketing:** v0.23.0 launch artifacts (competitive analysis + show hn + objections + checklist) ([#138](https://github.com/anatolykoptev/memdb/issues/138)) ([9903d92](https://github.com/anatolykoptev/memdb/commit/9903d92440fa8489c33e0bb5d4b518b2e736d719))
* refresh architecture overview for pure-Go stack ([#225](https://github.com/anatolykoptev/memdb/issues/225)) ([208e35a](https://github.com/anatolykoptev/memdb/commit/208e35a793f1d9124b409667bc22a51f64d58eb5))
* **roadmap:** M11/M12/M13/M14 sprints + post-merge ops checklist ([0552755](https://github.com/anatolykoptev/memdb/commit/055275568e1a52d103f61b8916c5b96849046597))


### Build & CI

* add livepg integration job with Postgres+pgvector+AGE ([#366](https://github.com/anatolykoptev/memdb/issues/366)) ([c16fcba](https://github.com/anatolykoptev/memdb/commit/c16fcba92cb6ba6351ef7b36ad4bcae6f0779ab7))
* add release-please for automated Go releases ([e3051ef](https://github.com/anatolykoptev/memdb/commit/e3051ef0b577d3d11359b032ea42e122079e0098))
* align with krolik CI convention ([#380](https://github.com/anatolykoptev/memdb/issues/380)) ([26b1bfd](https://github.com/anatolykoptev/memdb/commit/26b1bfd7f90af1c719f05ad5fa0b075c7f84281f))
* **esc-1:** composite test-pg image with AGE 1.6.0 prebuilt ([#378](https://github.com/anatolykoptev/memdb/issues/378)) ([06ade7f](https://github.com/anatolykoptev/memdb/commit/06ade7fa3040ed25554a1daa99a3e2ba34e21134))
* **esc-2:** Helm Redis 7→8 + maxmemory+allkeys-lru ([#376](https://github.com/anatolykoptev/memdb/issues/376)) ([e7917f9](https://github.com/anatolykoptev/memdb/commit/e7917f9100d21afff245873c8a1a732c681d1866))
* GOWORK=off go build ./... — ok ([f41ce92](https://github.com/anatolykoptev/memdb/commit/f41ce920c187db70b1db3e33d79e94a216e70e0c))
* GOWORK=off go build ./... ✓ ([dd3b70e](https://github.com/anatolykoptev/memdb/commit/dd3b70eb60ceaa525c865d85ee5821bf58b3779b))

## [0.24.1](https://github.com/anatolykoptev/memdb/compare/v0.24.0...v0.24.1) (2026-07-19)


### Bug Fixes

* **release:** use memdb-go/ tag prefix for Go subdir module ([fd92b82](https://github.com/anatolykoptev/memdb/commit/fd92b82ea453d3cda4f3247e10e8317f75576752))

## [0.24.0](https://github.com/anatolykoptev/memdb/compare/v0.23.0...v0.24.0) (2026-07-19)


### Features

* 3 Redis caches (embed/atomic-extract/rerank) + semantic dedup + per-cube ingest ([#276](https://github.com/anatolykoptev/memdb/issues/276)) ([3b481f7](https://github.com/anatolykoptev/memdb/commit/3b481f770f2eb9b51cdbd3c84fff964839504788))
* **add:** fast mode populates attributed_to, event_dates, linked_memory_ids, kind, per-msg metadata ([#248](https://github.com/anatolykoptev/memdb/issues/248)) ([f09d5d3](https://github.com/anatolykoptev/memdb/commit/f09d5d38d31f68570401c32cb4818624cd190e17))
* **add:** per-message fast extractor + fine→fast resilience fallback ([#246](https://github.com/anatolykoptev/memdb/issues/246)) ([55bfa48](https://github.com/anatolykoptev/memdb/commit/55bfa480fa015fc5217d2e25e8a91b2f4f411365))
* **add:** per-message uuid and agent_id passthrough into sources ([fef46ed](https://github.com/anatolykoptev/memdb/commit/fef46ed68a9e757236150245578ebaf91777de07))
* **add:** plumb per-msg metadata into raw-mode info ([#218](https://github.com/anatolykoptev/memdb/issues/218)) ([36c8f03](https://github.com/anatolykoptev/memdb/commit/36c8f03c0e26d6a86f8be1e876151d8ec8a807c6))
* **add:** plumb per-msg uuid+agent_id into fast-mode windowing sources ([#216](https://github.com/anatolykoptev/memdb/issues/216)) ([d7f6490](https://github.com/anatolykoptev/memdb/commit/d7f64906942d6c7e3d1306ad4ffdb9f8133726a7))
* **api:** properties.key writes + get_memory_by_key + list_memories_by_prefix endpoints ([#135](https://github.com/anatolykoptev/memdb/issues/135)) ([7d2c4a1](https://github.com/anatolykoptev/memdb/commit/7d2c4a1980b9d8d5f6ea48c1d6c62caed052bbf2))
* chat_time anchor + F11 + M9 dual-speaker + AttributedTo + Memobase ALIAS ([d9b5713](https://github.com/anatolykoptev/memdb/commit/d9b571399a44223463efc8c3db3057a0b02cad37))
* **chat+search:** anti-refusal prompt tuning + go-kit RRF fusion (M12.2 deepen) ([c32148a](https://github.com/anatolykoptev/memdb/commit/c32148a1b72eb3dbef51eff0f9290c04747bfdbe))
* **chat:** anti-refusal prompt tuning — answer with available context (M12.2 deepen) ([dfcb814](https://github.com/anatolykoptev/memdb/commit/dfcb814c5caff6d7f4909e2be38a6324f578b3ac))
* **chat:** atom-as-prompt-context — surface atomic facts as Key Facts section (Memobase parity) ([#288](https://github.com/anatolykoptev/memdb/issues/288)) ([51bae95](https://github.com/anatolykoptev/memdb/commit/51bae95fb9fe8c594e9e2e9bde7573c5a4389dab))
* **chat:** M12.1 server-side temporal anchor + chat_dual_speaker SRP refactor ([#222](https://github.com/anatolykoptev/memdb/issues/222)) ([d8fb32f](https://github.com/anatolykoptev/memdb/commit/d8fb32fb63435051a65f5f9b9bcee36e46741544))
* **chat:** make rule [#10](https://github.com/anatolykoptev/memdb/issues/10) (cross-character bleed) env-conditional (Phase 4) ([#305](https://github.com/anatolykoptev/memdb/issues/305)) ([92772d5](https://github.com/anatolykoptev/memdb/commit/92772d53a1fd7c6bae9807b1d5a7c17eee466ef8))
* **chat:** max_context_tokens budget for memories block + chat_prompt SRP split ([#223](https://github.com/anatolykoptev/memdb/issues/223)) ([d1ed729](https://github.com/anatolykoptev/memdb/commit/d1ed729931d7c99a068b8cc7fc312c6b1a3037b0))
* **chat:** multi-feature retrieval confidence (top1 + spread + density + median) ([#253](https://github.com/anatolykoptev/memdb/issues/253)) ([aae23f7](https://github.com/anatolykoptev/memdb/commit/aae23f7450be5443781c6102cc25d8b9f4e7fad8))
* **chat:** synthesis + cross-character prompt extensions (M12.2 deepen [#2](https://github.com/anatolykoptev/memdb/issues/2)) ([6084f9e](https://github.com/anatolykoptev/memdb/commit/6084f9e84f6f88f7c31b5c3c8bb17d345f1a28a0))
* **d10:** locale-aware skill routing + OTLP fix + go-kit v0.34 bump ([#265](https://github.com/anatolykoptev/memdb/issues/265)) ([d222a12](https://github.com/anatolykoptev/memdb/commit/d222a12ceb1af6682cd101436a94f84430127f77))
* **embedder:** add circuit breaker for HTTP embedder (PF-8) ([#364](https://github.com/anatolykoptev/memdb/issues/364)) ([a55c302](https://github.com/anatolykoptev/memdb/commit/a55c302211bad14d8b55f20278e4fa96127c6082))
* **embedder:** client-side chunking for HTTPEmbedder (defense in depth for ox-embed-server input cap) ([#310](https://github.com/anatolykoptev/memdb/issues/310)) ([75c6370](https://github.com/anatolykoptev/memdb/commit/75c6370a8d5e2939b0470a86d438eab4ca3464a5))
* **embedder:** migrate HTTPEmbedder to go-kit/embed.Client (foundation for E1/E3) ([c56db70](https://github.com/anatolykoptev/memdb/commit/c56db702c3dd15a458a1c977f9b415c1a0380f2b))
* **embedder:** switch code-embed to code-rank-embed; add query-prefix asymmetry ([#317](https://github.com/anatolykoptev/memdb/issues/317)) ([d2da343](https://github.com/anatolykoptev/memdb/commit/d2da343fde1391fb9a1bd97b24d73d822d76dde7))
* **esc-2:** VSetEvictionPolicy + VAdd hook + VsetEvictTotal metric ([#379](https://github.com/anatolykoptev/memdb/issues/379)) ([adab49e](https://github.com/anatolykoptev/memdb/commit/adab49e9751af9d43428723167dfd578f477789b))
* **extractor:** F11 observation_date coverage tune (12.4% → target 40%+) ([642f2b9](https://github.com/anatolykoptev/memdb/commit/642f2b9e72129ae98ba540a9d8eb9606086317b1))
* **f11:** bi-temporal edges (Graphiti / Zep, arxiv 2501.13956) ([#156](https://github.com/anatolykoptev/memdb/issues/156)) ([5827f60](https://github.com/anatolykoptev/memdb/commit/5827f60fd96bb4b9be9ab787615e291fb93c1b42))
* **f14:** Personalized PageRank — HippoRAG 2 query-seeded PPR (arxiv 2502.14802) ([#157](https://github.com/anatolykoptev/memdb/issues/157)) ([cd5223e](https://github.com/anatolykoptev/memdb/commit/cd5223e5ca4c0a95494e8dfbfbb54710192c5ec8))
* **f8:** atomic per-fact extraction (mem0 ADDITIVE_PROMPT) — closes cat-2 paradox ([#158](https://github.com/anatolykoptev/memdb/issues/158)) ([b374798](https://github.com/anatolykoptev/memdb/commit/b3747986c99a632a2e9d981a784b4d4574f6b36f))
* **f9:** recall budget tuning (top_k=30 + cat-2 threshold 0.05 + reverse-role) ([#155](https://github.com/anatolykoptev/memdb/issues/155)) ([65d7833](https://github.com/anatolykoptev/memdb/commit/65d7833f2ca6b54d530c5a32e3d05ef6606d498e))
* **locomo-eval:** competitor-comparable telemetry + Mem0-strict F1 + LOCOMO_JUDGE_MODEL env ([#282](https://github.com/anatolykoptev/memdb/issues/282)) ([a382856](https://github.com/anatolykoptev/memdb/commit/a382856e4d07bee643e4ffe10822e9ef127a24c0))
* **locomo:** default ingest mode fine plus atomic facts ([#247](https://github.com/anatolykoptev/memdb/issues/247)) ([460fffc](https://github.com/anatolykoptev/memdb/commit/460fffc3ae943a8a0f29f8fe17d9f186406680a3))
* **locomo:** non-LLM regex backfill for F11 event_dates ([#233](https://github.com/anatolykoptev/memdb/issues/233)) ([5bda06b](https://github.com/anatolykoptev/memdb/commit/5bda06ba4011f81b22e55ba58e4d68691131f927))
* **locomo:** per-session cube namespace via LOCOMO_CUBE_NAMESPACE ([#245](https://github.com/anatolykoptev/memdb/issues/245)) ([2d52c7f](https://github.com/anatolykoptev/memdb/commit/2d52c7f6b3f371963ebca2cddea3e9344982949e))
* **locomo:** use M9 server-side dual-speaker fan-out ([#221](https://github.com/anatolykoptev/memdb/issues/221)) ([83fdbd1](https://github.com/anatolykoptev/memdb/commit/83fdbd1e45bf3223b80110acc20244f3fc752eb9))
* **m11-f3:** Memobase event extraction + search-time event inject ([#160](https://github.com/anatolykoptev/memdb/issues/160)) ([3950334](https://github.com/anatolykoptev/memdb/commit/3950334af5ce8dd1d5cf1e414a85274a5e9c91f6))
* **memdb-go:** chat_time anchor + F11 event_dates + M9 dual-speaker + AttributedTo filter + Memobase ALIAS ([c0e38e6](https://github.com/anatolykoptev/memdb/commit/c0e38e6da7738f005d57a7ca043da5da6cc09e74))
* **memdb:** F12 linked_memory_ids resolver + 1-hop search expansion ([#162](https://github.com/anatolykoptev/memdb/issues/162)) ([beb3854](https://github.com/anatolykoptev/memdb/commit/beb385463bacf946517331404e6fab25ecff4879))
* **observability:** bump go-kit v0.34→v0.35 + integrate pgxotel/slogh/httpmw ([#268](https://github.com/anatolykoptev/memdb/issues/268)) ([bad3f83](https://github.com/anatolykoptev/memdb/commit/bad3f83721193235b3fefe32f4ea36a1d53fa357))
* **observability:** prewarm meter singletons + Memobase attribution counter ([#217](https://github.com/anatolykoptev/memdb/issues/217)) ([8e2290f](https://github.com/anatolykoptev/memdb/commit/8e2290fc3f59462a3e8f973b6ff1bac810d52bfa))
* **observability:** wire skillkit v0.2.0 Observer into OTel meter (Phase 4) ([#262](https://github.com/anatolykoptev/memdb/issues/262)) ([9601a8d](https://github.com/anatolykoptev/memdb/commit/9601a8d05c307b3c0b1f883c68d8d63ba4ab2423))
* **observability:** wire skillkit v0.2.2 Tracer into OTel ([#267](https://github.com/anatolykoptev/memdb/issues/267)) ([e8adf9b](https://github.com/anatolykoptev/memdb/commit/e8adf9bd7b970b6d256ce5d726ba9beceab2593d))
* **proto:** add chat_time + message_id to Message (matches HTTP /add) ([3abe063](https://github.com/anatolykoptev/memdb/commit/3abe063b9d55672085af093d8ac556eb33b7aa7c))
* **proto:** add chat_time + message_id to Message (matches HTTP /add) ([882166d](https://github.com/anatolykoptev/memdb/commit/882166d4bfe25e64f7f42389724d6cbd72c430f8))
* **q1:** LoCoMo harness timeout/5xx retry — recovers transient failures ([#149](https://github.com/anatolykoptev/memdb/issues/149)) ([c229f71](https://github.com/anatolykoptev/memdb/commit/c229f717e3890192088f06d3d1ac1356bb542aa9))
* **q2:** MEMDB_PROFILE_TAXONOMY env switch with 5 Memobase personas ([#148](https://github.com/anatolykoptev/memdb/issues/148)) ([934d8ef](https://github.com/anatolykoptev/memdb/commit/934d8ef46e8af2907cc823e3c17e8972be7c6b4a))
* **q3:** rerank metadata boost (+user_id +session_id +tags) per MemOS http_bge.py ([#154](https://github.com/anatolykoptev/memdb/issues/154)) ([7e904ac](https://github.com/anatolykoptev/memdb/commit/7e904ac37b120f959f8d924a1a0ec1dc9c1ad1dc))
* **q4:** MMR diversification reranker (bar=0.8) per MemOS retrieve_utils.py ([#153](https://github.com/anatolykoptev/memdb/issues/153)) ([77882f7](https://github.com/anatolykoptev/memdb/commit/77882f71f11a53f3454f16ef7f86a19d16be506e))
* **q5:** top-8 missing Prometheus metrics + alert rules ([#152](https://github.com/anatolykoptev/memdb/issues/152)) ([8c2973c](https://github.com/anatolykoptev/memdb/commit/8c2973cea0d22a7fb0266d62c2fa9b491d2bb022))
* **rerank:** cross-encoder math fallback on degraded or low-quality scores ([#242](https://github.com/anatolykoptev/memdb/issues/242)) ([d232b04](https://github.com/anatolykoptev/memdb/commit/d232b04e519a8b05774b3ec61c9dcea9df81d008))
* **rerank:** wire Voyage rerank-2.5 as opt-in fallback for local CE ([#300](https://github.com/anatolykoptev/memdb/issues/300)) ([40542ba](https://github.com/anatolykoptev/memdb/commit/40542baaad39003995a57feef2db3d0e9dd25e21))
* **scheduler:** add task FSM watchdog to reclaim stuck tasks ([#365](https://github.com/anatolykoptev/memdb/issues/365)) ([ae1119f](https://github.com/anatolykoptev/memdb/commit/ae1119f7069f692894e9d95af1bf4cba54916465))
* **scheduler:** M14 B1 — CE precompute MathReranker prefilter (env-gated) ([bbb0f75](https://github.com/anatolykoptev/memdb/commit/bbb0f75570315efae123e22b3a642223dcd2bed6))
* **scheduler:** M14 B1.1 — unify CE precompute legacy floor with env-gated threshold ([5fdbd6b](https://github.com/anatolykoptev/memdb/commit/5fdbd6bb1ff5293d6714ae6008a906200bad7f71))
* **scheduler:** replace binary useHNSW with 3-state DupStrategy enum (rf-1) ([#377](https://github.com/anatolykoptev/memdb/issues/377)) ([afaee23](https://github.com/anatolykoptev/memdb/commit/afaee23d508aee75208ac87e8930063fc2c89e7b))
* **scheduler:** workspace-tier hot-reload via MEMDB_SCHEDULER_SKILLS_DIR (Phase 5) ([#263](https://github.com/anatolykoptev/memdb/issues/263)) ([28f8eea](https://github.com/anatolykoptev/memdb/commit/28f8eead05b58e9934a97aa8d453ab5ed9718bcc))
* **search/rerank:** M14 A1 — Stage-0 MathReranker pre-CE prefilter (env-gated) ([f1e542a](https://github.com/anatolykoptev/memdb/commit/f1e542ad838c6c72c29d7b2c357f4c2324d002b1))
* **search/rerank:** M15 — CircuitBreaker on bge-reranker (env-gated, off by default) ([2ed0f22](https://github.com/anatolykoptev/memdb/commit/2ed0f224b4ec32c3b68f96822382a6d24421089b))
* **search/rerank:** per-strategy duration histogram (decompose post_process timing) ([84a2f5f](https://github.com/anatolykoptev/memdb/commit/84a2f5f94b2158dfe30c48a9d19d504fbf219467))
* **search:** CE no-retry + FTS toggle + lower CE quality floor ([#278](https://github.com/anatolykoptev/memdb/issues/278)) ([a8bc3ca](https://github.com/anatolykoptev/memdb/commit/a8bc3cad3bccdd6b149d52b6fb94401a0e99f218))
* **search:** counting-aware top_k boost for "how many" queries ([02b85b0](https://github.com/anatolykoptev/memdb/commit/02b85b0709c03f6fff92791a0318d74bebe78ff3))
* **search:** D10 prompt shrink + classifier anchors expansion ([#257](https://github.com/anatolykoptev/memdb/issues/257)) ([e8d9d2b](https://github.com/anatolykoptev/memdb/commit/e8d9d2b819de9df23eecf4da6475ccb559336ddd))
* **search:** D10 routing observability — diagnose cefix7→cefix8 regression ([#256](https://github.com/anatolykoptev/memdb/issues/256)) ([199c697](https://github.com/anatolykoptev/memdb/commit/199c697f76f8326bbaa0c2d5a4d519a6418d2f5a))
* **search:** D10 skill loader — externalise system prompt to .md files ([#258](https://github.com/anatolykoptev/memdb/issues/258)) ([4900c7a](https://github.com/anatolykoptev/memdb/commit/4900c7a852e0a41f4343866ced9fddcd802d87e0))
* **search:** D10 soft-routing — pass classifier distribution to LLM ([#255](https://github.com/anatolykoptev/memdb/issues/255)) ([8275b38](https://github.com/anatolykoptev/memdb/commit/8275b38e3af79a6420d3a1eee3b97b1b15239427))
* **search:** empty-cube fast-fail — skip d4+pipeline when cube has 0 entries (M14.Y4.1) ([dd03f94](https://github.com/anatolykoptev/memdb/commit/dd03f949ecc7a43c4fc19302d3e82c380293c26f))
* **search:** expose magic numbers as env + per-stage telemetry + locomo bump 8000 ([#283](https://github.com/anatolykoptev/memdb/issues/283)) ([d483561](https://github.com/anatolykoptev/memdb/commit/d48356138549f917f839e74689b847bbb4db3741))
* **search:** F2 reflection-loop deep-search agent (M11) ([#161](https://github.com/anatolykoptev/memdb/issues/161)) ([6199f5f](https://github.com/anatolykoptev/memdb/commit/6199f5fedefffaf9cf7d58a5b1c54496c21b58f6))
* **search:** hybrid d10 answer extractor with per-category prompts ([#250](https://github.com/anatolykoptev/memdb/issues/250)) ([f560fa1](https://github.com/anatolykoptev/memdb/commit/f560fa161d828f807505892bdb65fee6dbdf84cd))
* **search:** hybrid retrieval (dense + SPLADE sparse, RRF) via go-kit/sparse v0.37.0 ([#277](https://github.com/anatolykoptev/memdb/issues/277)) ([8166ceb](https://github.com/anatolykoptev/memdb/commit/8166ceb6bf3a56b3121827f8f71336a0cbdc7f78))
* **search:** in-memory LRU cache for d4_query_rewrite + d7_cot_decompose ([014bf8a](https://github.com/anatolykoptev/memdb/commit/014bf8a10777de3a364de7d76239bf15ad855ea7))
* **search:** M14.Y1 — F12 linked_expand env-gate default OFF ([e6f5b41](https://github.com/anatolykoptev/memdb/commit/e6f5b4138f46b030356837497ae959f37a1ced2b))
* **search:** M14.Y4 — fast-path skip d4_query_rewrite + d7_cot_decompose for simple factual queries (env-gated) ([a2420d9](https://github.com/anatolykoptev/memdb/commit/a2420d94564c5b8422ace27abe52236a093e7213))
* **search:** probabilistic d10 routing with embedding classifier and soft hints ([#252](https://github.com/anatolykoptev/memdb/issues/252)) ([fc2eeaa](https://github.com/anatolykoptev/memdb/commit/fc2eeaa13cba439c739bcfacd8798e35d4fbd5f0))
* **search:** WeightedRRF + DBSF + LinearMinMax fusion strategies (env-selectable) ([5f6746b](https://github.com/anatolykoptev/memdb/commit/5f6746b7190072143dbf79f16c84afc13faf520a))
* **search:** WeightedRRF + DBSF + LinearMinMax fusion strategies (env-selectable) ([6c80306](https://github.com/anatolykoptev/memdb/commit/6c80306d8f3a5596d32ba5180d2610a390bf8e3a))
* **search:** wire go-kit/rerank.RRF for hybrid pgvector + BM25 + AGE fusion ([d083bf6](https://github.com/anatolykoptev/memdb/commit/d083bf61ebf698264ffe26c64163bb5e83d5383e))
* **tools:** devto-publish.sh CLI for Forem API + first launch article draft ([#143](https://github.com/anatolykoptev/memdb/issues/143)) ([65e7cbd](https://github.com/anatolykoptev/memdb/commit/65e7cbd2d26f61ef1c6553680902810652fd9acf))
* **wiki:** auto-update on tree promotion (W2 reorganizer hook) ([#230](https://github.com/anatolykoptev/memdb/issues/230)) ([cc8b844](https://github.com/anatolykoptev/memdb/commit/cc8b844d41157be5df9519b703b76a774cbcbec4))
* **wiki:** DB schema + CRUD for wiki_pages ([#228](https://github.com/anatolykoptev/memdb/issues/228)) ([f5c1fa1](https://github.com/anatolykoptev/memdb/commit/f5c1fa101c1ce160717facedc0fd2d2a1b97d32d))
* **wiki:** foundation — Karpathy LLM Wiki filesystem export via go-kit/uploads ([#227](https://github.com/anatolykoptev/memdb/issues/227)) ([6ebb026](https://github.com/anatolykoptev/memdb/commit/6ebb0264c2e1b754eea276ef6f4cb995e9380193))
* **wiki:** HTTP endpoints + filesystem mirror chain ([#229](https://github.com/anatolykoptev/memdb/issues/229)) ([3c06cb9](https://github.com/anatolykoptev/memdb/commit/3c06cb91deba7d07d0682fe0c09b299961f11a3a))
* **wiki:** inject wiki synthesis as retrieval slot with sync reorg ([#232](https://github.com/anatolykoptev/memdb/issues/232)) ([c66966c](https://github.com/anatolykoptev/memdb/commit/c66966c7086a9f18fdcc06fb9d5dd0507a70807a))


### Bug Fixes

* **add:** session-aware dedup + counter + env-tunable threshold ([#224](https://github.com/anatolykoptev/memdb/issues/224)) ([c2a017f](https://github.com/anatolykoptev/memdb/commit/c2a017f7465d3e9de438bd42356f8023bae4bc43))
* **atomic-extractor:** emit named_entities_in_text so PR [#291](https://github.com/anatolykoptev/memdb/issues/291) wiring can fire ([#302](https://github.com/anatolykoptev/memdb/issues/302)) ([63adfae](https://github.com/anatolykoptev/memdb/commit/63adfaebb234ad0c586ae14f9e0e651ab9864c2b))
* **atomic:** promote NamedEntitiesInText to entity_nodes (graph traversal coverage) ([#291](https://github.com/anatolykoptev/memdb/issues/291)) ([0d7d76f](https://github.com/anatolykoptev/memdb/commit/0d7d76f20c81839aeaf53775480d720a8cd12452))
* **cache:** close /product/search cache correctness gap (v2→v3) + bypass for internet/dual-speaker ([#293](https://github.com/anatolykoptev/memdb/issues/293)) ([58a24f9](https://github.com/anatolykoptev/memdb/commit/58a24f9a047c35f86c6056de2c6cc06111344916))
* **cache:** DB-cache pagination + invalidation correctness (PR A) ([#295](https://github.com/anatolykoptev/memdb/issues/295)) ([4b8680e](https://github.com/anatolykoptev/memdb/commit/4b8680e8fd7b8f8b39613a9aac3abe532bdbc779))
* **cache:** LLM-output cache key correctness (PR B) ([#296](https://github.com/anatolykoptev/memdb/issues/296)) ([b95d15e](https://github.com/anatolykoptev/memdb/commit/b95d15eaff0363fca29f2fc0f4839793b67d9f38))
* **chat:** kill conflicting refusal contracts + per-speaker profiles + external memory hint ([#287](https://github.com/anatolykoptev/memdb/issues/287)) ([007ecd6](https://github.com/anatolykoptev/memdb/commit/007ecd6572d687c97157affc1710b43dae0a6436))
* **chat:** M12.2 confidence-conditional factual prompt + observability ([#170](https://github.com/anatolykoptev/memdb/issues/170)) ([84b9ca3](https://github.com/anatolykoptev/memdb/commit/84b9ca380656bfdb1cbce2cd52661bc6b2de32f7))
* **chat:** wire dead metrics — chat_prompt_template_used + chat_refused_with_evidence + chat_top1_cosine ([4f02fb7](https://github.com/anatolykoptev/memdb/commit/4f02fb7f1e2cffc647161fe2aeac8ccb3ac3e838))
* **config:** add startup Validate() for auth and Postgres URL ([#353](https://github.com/anatolykoptev/memdb/issues/353)) ([40fd460](https://github.com/anatolykoptev/memdb/commit/40fd46039934791549328e16c5b7da35716d1088))
* **eval/locomo:** score.py embed URL — semsim correct, no BoW fallback ([404b2a4](https://github.com/anatolykoptev/memdb/commit/404b2a4ebf79cd29ae60b3dc40edc673e8017320))
* **gomemlimit:** add fallback when cgroup has no memory limit ([#355](https://github.com/anatolykoptev/memdb/issues/355)) ([f671065](https://github.com/anatolykoptev/memdb/commit/f671065cbb8961c033730911679bb99bc62db63e)), closes [#320](https://github.com/anatolykoptev/memdb/issues/320)
* **handlers:** delete_cube invalidates Redis VSET cache ([#236](https://github.com/anatolykoptev/memdb/issues/236)) ([db14b49](https://github.com/anatolykoptev/memdb/commit/db14b49a6e3bf5f66c3db003d865b2fc191b80ab))
* **handlers:** thread observation_date through skill + tool extractors (Phase 1.5) ([#303](https://github.com/anatolykoptev/memdb/issues/303)) ([da94997](https://github.com/anatolykoptev/memdb/commit/da94997407bf9e5bc759dd696824b13e0b7dc904))
* **lint:** clear pre-existing govet/staticcheck regressions ([#231](https://github.com/anatolykoptev/memdb/issues/231)) ([7764851](https://github.com/anatolykoptev/memdb/commit/7764851531878233ef637622ec1473b908630366))
* **llm:** retry on 401/403 auth errors with delay for key rotation ([#363](https://github.com/anatolykoptev/memdb/issues/363)) ([02f0ad5](https://github.com/anatolykoptev/memdb/commit/02f0ad5ba4c3ae67ef22b44cae70fffb0e57e6a9))
* **locomo-eval:** pass external_memory_count to trigger r3 variant upgrade ([#289](https://github.com/anatolykoptev/memdb/issues/289)) ([0deaabc](https://github.com/anatolykoptev/memdb/commit/0deaabce3bdc78341f3af2cdd855548d7cfe0b93))
* **locomo:** brevity-enforcing answer prompt — F1 +35% relative on chat-50 ([#279](https://github.com/anatolykoptev/memdb/issues/279)) ([d458caf](https://github.com/anatolykoptev/memdb/commit/d458caf97f864eb802d602507d98bef7e90cb24d))
* **locomo:** cleanup script uses conversation file as cube source ([#237](https://github.com/anatolykoptev/memdb/issues/237)) ([20b0c81](https://github.com/anatolykoptev/memdb/commit/20b0c811b03efeb5bf6e831988c709f2a508d1f5))
* **m12.7:** revive LLM Judge reranker (silently dead in M11) + tune gate thresholds ([#171](https://github.com/anatolykoptev/memdb/issues/171)) ([684a292](https://github.com/anatolykoptev/memdb/commit/684a292fb181fd60337f25ada9b523929813c2a5))
* **m12:** thread conversation timestamp through ingest → search → chat prompt ([#175](https://github.com/anatolykoptev/memdb/issues/175)) ([66e0e88](https://github.com/anatolykoptev/memdb/commit/66e0e8844d8dfe8bb334707847b31342b4102eaf))
* **mcp:** tool count comment, CubeID fallback test, drop dead *db.Postgres param ([#308](https://github.com/anatolykoptev/memdb/issues/308)) ([a6375d0](https://github.com/anatolykoptev/memdb/commit/a6375d08e6a91e4789a66ce61f3793c098d4ff84))
* **memdb-mcp:** pin golang base to 1.26 stable (was 1.26rc3) ([#312](https://github.com/anatolykoptev/memdb/issues/312)) ([294807d](https://github.com/anatolykoptev/memdb/commit/294807dbed53589311a1f38faa95d05751f9d718))
* **memory:** deterministic lock order to prevent DELETE/UPDATE deadlock ([#309](https://github.com/anatolykoptev/memdb/issues/309)) ([ae9440d](https://github.com/anatolykoptev/memdb/commit/ae9440d1d0b6227beae04f02223ad94b1193c46f))
* **memory:** NativeUpdateMemory clears CE cache; MCP update_memory uses full update path ([#307](https://github.com/anatolykoptev/memdb/issues/307)) ([baf1441](https://github.com/anatolykoptev/memdb/commit/baf1441f60f2e158d85af5b5794c1878fcff1f46))
* **memprops:** observation_date invariant for derived memories (Phase 1A) ([#299](https://github.com/anatolykoptev/memdb/issues/299)) ([2954930](https://github.com/anatolykoptev/memdb/commit/2954930f394157c494f133a02fa7aaf71f259bdc))
* **memprops:** tree_reorganizer parents + atomic LTM/WM inherit observation_date (Phase 1B) ([#301](https://github.com/anatolykoptev/memdb/issues/301)) ([64fd660](https://github.com/anatolykoptev/memdb/commit/64fd660fe61f3119005610eff053c3278fda6e9c))
* **migrations:** AGE agtype incompatibility — 0018, 0022, 0024 + temporal query ([#167](https://github.com/anatolykoptev/memdb/issues/167)) ([b67d1b6](https://github.com/anatolykoptev/memdb/commit/b67d1b6643a0d3a687bb6fde50f2c13a9a06c84e))
* **observability:** instrument 3 silent skip paths in atomic-fact ingest ([#226](https://github.com/anatolykoptev/memdb/issues/226)) ([8941f64](https://github.com/anatolykoptev/memdb/commit/8941f6419d62f33f85eecf19399c5a6e9a07015c))
* **observability:** move Logging middleware inside OTel for trace_id ([#272](https://github.com/anatolykoptev/memdb/issues/272)) ([bd70361](https://github.com/anatolykoptev/memdb/commit/bd703613a5e06c5d1eff91f29e0ed5aaf03c590c))
* phase-2 bug-hunt bundle (PF-6/11/12/14/16/18/20/22, CP-4/8) ([#369](https://github.com/anatolykoptev/memdb/issues/369)) ([2130fb1](https://github.com/anatolykoptev/memdb/commit/2130fb1e745002662f837fadbf7647b0861afa87))
* phase-2b bundle (PF-13/15/17/19, FP closures) ([#370](https://github.com/anatolykoptev/memdb/issues/370)) ([cc723c5](https://github.com/anatolykoptev/memdb/commit/cc723c52a52843ccdf8807953b10c69408a0d897))
* phase-2c bundle (PF-10/21/24/25) ([#371](https://github.com/anatolykoptev/memdb/issues/371)) ([7912e07](https://github.com/anatolykoptev/memdb/commit/7912e07a866193c17dcc04a6d19c21bfcd0e5cd8))
* **precompute:** skip persisting Score=0 placeholders when CE returns Degraded ([#298](https://github.com/anatolykoptev/memdb/issues/298)) ([d05d334](https://github.com/anatolykoptev/memdb/commit/d05d334c10360da0ffc5f7ec8a76242dc7f15c29))
* **reorganizer:** invalidate CE cache on all UpdateMemoryNodeFull paths ([#362](https://github.com/anatolykoptev/memdb/issues/362)) ([7c0be5c](https://github.com/anatolykoptev/memdb/commit/7c0be5c73cf2311342a5acccf7d2d79ca8c11005))
* **rerank:** pre-bypass CE on cosine confidence + spread floor + sigmoid-tuned defaults ([d34592f](https://github.com/anatolykoptev/memdb/commit/d34592f3a1210da13e383ceaf6e7be06970bbc4d))
* **rerank:** precompute hit-rate, math errors, MMR kill-switch ([#292](https://github.com/anatolykoptev/memdb/issues/292)) ([91622cb](https://github.com/anatolykoptev/memdb/commit/91622cb25a8eb9716a63b9df414bf0a8326d58a6))
* **rerank:** tune CE breaker thresholds — preserve CE on weak-but-useful cubes ([#286](https://github.com/anatolykoptev/memdb/issues/286)) ([2a17397](https://github.com/anatolykoptev/memdb/commit/2a173977abf142c2c0659c7cc5cb1a161b0dd166))
* **scheduler/search:** PageRank distribution collapse — qualify memory_edges schema (M11.Y) ([1f3502c](https://github.com/anatolykoptev/memdb/commit/1f3502c3658c6d76a4c3040896aad7907dd95a55))
* **scheduler:** thread observation_date through mem_read actions + prefs (Phase 1.5) ([#304](https://github.com/anatolykoptev/memdb/issues/304)) ([ebe3f41](https://github.com/anatolykoptev/memdb/commit/ebe3f41186280a553af2d385644e73ddaaaac411))
* **score:** honest hit@k — strip stopwords + require ≥2 token overlap ([#290](https://github.com/anatolykoptev/memdb/issues/290)) ([055487a](https://github.com/anatolykoptev/memdb/commit/055487a5e7dfff2025cc4a2c5a5a14054560fef1))
* **search/rerank:** M12.6 — staged retrieval CE backend ([#173](https://github.com/anatolykoptev/memdb/issues/173)) ([88c3d61](https://github.com/anatolykoptev/memdb/commit/88c3d61f17b5bf56839246a89cccc332e2c43db6))
* **search/rerank:** M14.Y2 — CE precompute partial-hit recovery (M10 S6 architecturally fixed) ([93746ab](https://github.com/anatolykoptev/memdb/commit/93746abcb52e1097267eec3fe78c75e545e1f08c))
* **search:** demote bare-token atoms from rank-1 ([#281](https://github.com/anatolykoptev/memdb/issues/281)) ([55b9ce6](https://github.com/anatolykoptev/memdb/commit/55b9ce68edcb2cbde1a3520dbdcb36852a199040))
* **search:** revert d10 hybrid extractor and tighten extraction prompt ([#251](https://github.com/anatolykoptev/memdb/issues/251)) ([b176528](https://github.com/anatolykoptev/memdb/commit/b176528821036ca978f0dbea32e70f7e6c204582))
* **search:** soften D10 answer extractor prompt for atomic-fact memories ([#249](https://github.com/anatolykoptev/memdb/issues/249)) ([c051b77](https://github.com/anatolykoptev/memdb/commit/c051b770215162344874dce5ae9809cfa2b69d8d))
* **search:** thread ctx into rerankStrategy ([#274](https://github.com/anatolykoptev/memdb/issues/274)) ([e9f772c](https://github.com/anatolykoptev/memdb/commit/e9f772c654d467aea5acc3f64b46f864dcfdc824))
* **search:** thread request ctx into stageRetrievalCountAsync to enable pgxotel spans ([#269](https://github.com/anatolykoptev/memdb/issues/269)) ([082d211](https://github.com/anatolykoptev/memdb/commit/082d211208c28801c31a1b35660b9f48115020f7))
* **search:** wiki opt-in + corpus-aware decay + per-cube CE breaker (round 2) ([#285](https://github.com/anatolykoptev/memdb/issues/285)) ([1fc8849](https://github.com/anatolykoptev/memdb/commit/1fc88493854929c2fac533c20311d6ba8ebdf8e0))
* **vendor:** sync vendor/ to go-kit v0.38.0 ([#294](https://github.com/anatolykoptev/memdb/issues/294)) ([15ea095](https://github.com/anatolykoptev/memdb/commit/15ea09507da148b30d9b07945da891deb25dd0b2))
* **wiki:** wire recordWikiPage hook in promoteCluster ([#235](https://github.com/anatolykoptev/memdb/issues/235)) ([79dcaca](https://github.com/anatolykoptev/memdb/commit/79dcacacb13aac95f5fb4ab36730e71f0a175332))


### Performance Improvements

* **db:** hot-path indexes on Memory properties — 1830× BFS speedup ([#169](https://github.com/anatolykoptev/memdb/issues/169)) ([d7a3d83](https://github.com/anatolykoptev/memdb/commit/d7a3d834bd131885d2eee3e2183335f06bd24e41))

## [Unreleased]

## [memdb-go/v0.23.1] — 2026-07-19

Release v0.23.1

## [0.23.0] — 2026-04-26 — M10 user_profiles + perf + security audit

Headline: **MemDB scores 72.5% LLM Judge** on LoCoMo chat-50 stratified
(excl cat-5, Memobase convention) — up from 70.0% in v0.22.0 (+2.5pp).
Position on the public leaderboard: between MemOS (73.31%) and Zep (75.14%),
+5.62pp ahead of Mem0 (66.88%), -2.64pp short of Zep, -3.28pp short of Memobase (75.78%) leader. Full corpus 1986 QAs lands at
50.9% LLM Judge (excl cat-5), up from M9 retrieval-only ~30% (+20pp).

### Added

- **S1 `user_profiles` schema** — Memobase-derived `topic / sub_topic / memo`
  table + typed query layer. Migration `0015`. ([#121](https://github.com/anatolykoptev/memdb/pull/121))
- **S2 PROFILE-EXTRACT** — LLM profile extractor with the verbatim Memobase
  prompt; runs as a fire-and-forget hook off `add_fine`. Gated by
  `MEMDB_PROFILE_EXTRACT` (default `true`).
  ([#124](https://github.com/anatolykoptev/memdb/pull/124))
- **S3 PROFILE-RETRIEVE** — chat handler injects a structured `<user_profile>`
  section above the memory section in the prompt. Gated by
  `MEMDB_PROFILE_INJECT` (default `true`).
  ([#123](https://github.com/anatolykoptev/memdb/pull/123))
- **S4 LEVELS-API** — `level=l1|l2|l3` query parameter on search; surfaces
  the existing instant / working / long-term split as a MemOS-paper
  compatible API skin. ([#122](https://github.com/anatolykoptev/memdb/pull/122))
- **S5 Helm chart** — single-namespace `deploy/helm/` for postgres + redis +
  qdrant + embed-server + memdb-go + memdb-mcp; CI dry-run smoke test; no
  external subcharts. ([#120](https://github.com/anatolykoptev/memdb/pull/120))
- **S6 CE-PRECOMPUTE** — cross-encoder rerank scores pre-computed at D3
  ingest, persisted in `Memory.properties->'ce_score_topk'`. Gated by
  `MEMDB_CE_PRECOMPUTE` (default `true`).
  ([#125](https://github.com/anatolykoptev/memdb/pull/125))
- **S7 PageRank scheduler** — background goroutine computes PageRank on
  `memory_edges` every `MEMDB_PAGERANK_INTERVAL` (default `6h`); D1 rerank
  consumes the score as an additive boost. Gated by `MEMDB_PAGERANK_ENABLED`
  (default `true`). ([#127](https://github.com/anatolykoptev/memdb/pull/127))
- **S8 reward scaffold** — `feedback_events` + `extract_examples` tables
  for the M11 reward / corrections loop. Schema and write paths only;
  reads not wired yet. Migration `0016`.
  ([#128](https://github.com/anatolykoptev/memdb/pull/128))

### Security

- **C1 cube isolation in `user_profiles`** — added `cube_id` column +
  cube-scoped unique index on `(cube_id, user_id, topic, sub_topic)`;
  `GetProfilesByUserCube` filters by cube, never returns NULL-cube legacy
  rows. Migration `0017`. ([#129](https://github.com/anatolykoptev/memdb/pull/129))
- **C2 prompt-injection mitigation** — sanitize control characters +
  tag-wrap each fact in `<fact>...</fact>`; LLM is instructed to treat fact
  contents as data only.
  ([#130](https://github.com/anatolykoptev/memdb/pull/130))
- **C3 admission control** — bounded semaphore (size 8) acquired BEFORE the
  profile-extract goroutine spawn; saturated calls drop with a `busy`
  outcome counter. Caps DoS surface from request bursts.
  ([#131](https://github.com/anatolykoptev/memdb/pull/131))

### Fixed

- **I4 PageRank multi-replica advisory lock** — wraps the compute phase in a
  Postgres advisory lock so only one replica computes per interval. Stops
  duplicated CPU + write races.
  ([#132](https://github.com/anatolykoptev/memdb/pull/132))
- **P3 search cache key correctness** — cache key now includes `level`,
  `agent_id`, and `pref_top_k`. Earlier requests with different filter
  combinations could return another request's cached results.
  ([#133](https://github.com/anatolykoptev/memdb/pull/133))

### Performance

- **7.5× faster ingest** on the 1986-QA LoCoMo corpus (40 min vs 5 h on
  M9). CE precompute, embed batching, structural-edge dedup, and the
  fast-add pipeline compounded.
- **CE precompute lookup** saves 100-400ms p95 chat by replacing the
  query-time cross-encoder call with a graph property lookup.
- **PageRank D1 boost** lifts hub-memory recall on cat-1 + cat-3 questions.
- **Outer-loop parallelism** in `evaluation/locomo/query.py --workers N`
  (ThreadPoolExecutor); query phase on full corpus drops to ~10h with 4
  workers + `D2_MAX_HOP=2`.

### Bench (LoCoMo, Memobase-comparable LLM Judge)

| Track | All cats | Excl cat-5 |
|---|---|---|
| Chat-50 stratified, end-to-end | F1 0.138, **LLM Judge 62.0%** | F1 0.151, **LLM Judge 72.5%** |
| Full corpus (1986 QAs) | F1 0.153, LLM Judge 41.8% | F1 0.178, **LLM Judge 50.9%** |

Per-cat chat-50: 1=60% 2=80% 3=70% 4=80% 5=20%.
Per-cat full: 1=53.5% 2=29.0% 3=37.5% 4=59.9% 5=10.3%.

vs leaderboard: Memobase 75.78% > Zep 75.14% > MemOS 73.31% > **MemDB 72.5%** > Mem0 66.88%.

## [0.22.0] — 2026-04-26 — First public release

This is MemDB's first public release. Earlier v1.x and v2.x tags were internal
pre-public iterations — see [docs/versioning.md](docs/versioning.md) for the
re-versioning rationale.

### Headline result — first published Memobase-comparable measurement

MemDB v0.22.0 scores **70.0% LLM Judge** on LoCoMo chat-50 stratified
(excl cat-5, Memobase convention). Position: between Mem0 (66.88%) and
MemOS (73.31%), -5.78pp below Memobase leader (75.78%). Full numbers
in `evaluation/locomo/MILESTONES.md`.

### Why now

After M9 sprint (this week), MemDB has:
- Pure-Go stack (Python `memdb-api` removed 2026-04-26)
- Memobase-comparable LLM Judge measurement (publishable numbers)
- Phase D retrieval features fully shipped (D1-D11)
- Auto-release infrastructure validated (release-drafter + goreleaser + changelog-sync)

### Highlights since v2.2.0 (no breaking changes)

- **Pure Go runtime**: 6 containers (was 7); no more Python in hot path. See
  ROADMAP-GO-MIGRATION.md for details.
- **Memobase port** (M9 Streams 1-4): dual-speaker retrieval, LLM Judge metric,
  cat-5 exclusion + dual-track reporting, [mention DATE] time-anchoring in
  extract prompts.
- **Public release artifacts**: README rewritten for broad audience, LICENSE
  verified MIT, CONTRIBUTING / SECURITY / CODE_OF_CONDUCT added, versioning
  policy documented.

### Versioning reset

Previous: internal v2.2.0 (2026-04-26 morning).
Now: public v0.22.0 (2026-04-26 evening).

API/wire format unchanged. Update your image tag from
`anatolykoptev/memdb:v2.2.0` → `anatolykoptev/memdb:v0.22.0`. Schema migrations
(postgres_migrations.go) are automatic.

See [docs/versioning.md](docs/versioning.md) for full rationale.

### Migration from v2.x

- Update image tag in compose
- No code changes required for SDK/REST clients
- Schema auto-migrates on memdb-go startup

### Breaking changes

None at on-the-wire level. The version reset itself is the only "breaking"
change — `^v2` ranges no longer satisfy the latest release.

## [2.2.0] — 2026-04-25

### Features

<details>
<summary>12 changes</summary>

- feat(handlers): date-aware extract prompt with [mention date] tags for temporal lift (#90) @anatolykoptev
- feat(locomo): dual-speaker retrieval in harness for cat-5 attribution closure (#92) @anatolykoptev
- feat(locomo): cat-5 exclusion flag + dual-track aggregate reporting (#88) @anatolykoptev
- feat(locomo): llm judge metric for memobase/mem0-comparable scoring (#91) @anatolykoptev
- feat(search): cot query decomposition (d11) for multi-hop and temporal questions (#82) @anatolykoptev
- feat(handlers): structural edges at ingest (same\_session + timeline\_next + similar\_cosine) (#83) @anatolykoptev
- feat(search): instrument d2 multi-hop + targeted fix for cat-2 f1 lift (#81) @anatolykoptev
- feat(handlers): factual answer-style canary with sticky-per-user 10% split (#80) @anatolykoptev
- feat(add): configurable window\_chars per request (M7 Stream C, Option A) (#65) @anatolykoptev
- feat(chat): server-side answer\_style=factual (M7 Stream A) (#64) @anatolykoptev
- feat(d3): wire relation detector + surface silent edge-write errors (M5 follow-ups) (#58) @anatolykoptev
- feat(telemetry): M1 per-D-feature Prometheus counters (#53) @anatolykoptev
</details>

### Bug Fixes

- fix(search): keep all D2 seeds, cap only expansions (#84) @anatolykoptev
- fix(d3): unblock memory\_edges on small cubes — wire admin reorg + include WorkingMemory (#57) @anatolykoptev

### Performance

- perf(handlers): batch embed calls in fast-add to remove window=512 latency cliff (#71) @anatolykoptev

### Tests

- test(d3): livepg integration test for runRelationPhase (#59) @anatolykoptev

### Documentation

- docs(roadmap): mark go migration complete after phase 5 shutdown (#94) @anatolykoptev
- docs(plan): m9 memobase port + honest measurement sprint (#87) @anatolykoptev
- docs(eval): m8 cat-5 adversarial diagnosis + recommendation (#85) @anatolykoptev
- docs(competitive): m8 memory frameworks survey + top-3 port-target specs (#79) @anatolykoptev
- docs(plan): m8 multi-hop and competitive lift sprint (cat-2 + cot + competitor survey) (#76) @anatolykoptev
- docs: m7 + follow-ups changelog and roadmap sync (#73) @anatolykoptev
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

### Internal

- chore: phase 5 python shutdown — convert safety-net proxies to http 503/422 and remove memdb-api from compose (#93) @anatolykoptev
- chore(server): extract x-service-secret helper + wire default answer style (#78) @anatolykoptev
- chore(infra): gomemlimit auto-detect + recovery script template with heartbeat (#77) @anatolykoptev
- chore(locomo): m7 stage 3 attempt — measurement invalid (oom) (#75) @anatolykoptev
- chore(ci): add changelog auto-sync workflow from release notes (#74) @anatolykoptev
- chore(server): register pprof routes behind internal-auth (#72) @anatolykoptev
- chore(locomo): m7 compound run stages 1+2 — F1 0.238 (+349% vs baseline, MemOS-tier) (#67) @anatolykoptev
- perf(handlers): m7 latency + pprof report (answer\_style=factual −52% p95) (#68) @anatolykoptev
- chore(testing): m7 regression report — no regressions across A/B/C (#66) @anatolykoptev
- chore(locomo): switch ingest to mode=raw for per-message granularity (M7 Stream B) (#63) @anatolykoptev

## [2.1.0] — 2026-04-25

### Highlights

**M7 Compound Lift Sprint — first MemOS-tier LoCoMo result.** Aggregate F1 0.053 → 0.238 (+349%) on the LoCoMo benchmark via three orthogonal fixes (server-side QA prompt + per-message ingest granularity + retrieval-threshold tuning) plus an embed-batching perf win that makes small-window ingest production-safe. answer_style=factual is also 2.1× faster on chat (bonus: shorter prompt = less LLM input = faster TTFT). cat-4 open-domain F1 0.017 → 0.407 (+24×).

### Added — Server-side knobs

- `answer_style` field on `/product/chat/complete` and `/product/chat[/stream]` requests (`conversational` default, `factual` for short fact-extraction). New templates `factualQAPromptEN/ZH`. Validation: unknown value → 400.
- `window_chars` field on `/product/add` requests (mode=fast/async). Per-request override, range [128, 16384], default 4096 unchanged. Out-of-range silently falls back to default.

### Added — Observability

- OTel counter `memdb.chat.prompt_template_used_total{template={factual|conversational|custom}}` — adoption tracking for the new prompt mode.
- OTel histogram `memdb.add.embed_batch_size{mode}` — visibility into embed batch sizes after the perf refactor.
- `/debug/pprof/*` routes registered behind `X-Service-Secret` auth.

### Performance

- **Embed batching in fast-add pipeline.** `nativeFastAddForCube` collects window texts upfront and issues a single `embedder.Embed(texts)` call instead of N sequential calls. Latency at window=512 drops from ~13s p95 to ~1.0s (13× speedup). No regression at default window=4096.
- **`answer_style=factual` chat is 2.1× faster at p95** (14.7s → 7.0s) — short prompt cuts LLM input tokens by ~80%.

### Documentation

- M7 perf report `docs/perf/2026-04-25-m7-latency-report.md`.
- M7 regression report `docs/testing/2026-04-25-m7-regression-report.md`.
- Sliding-window design doc `docs/design/2026-04-25-sliding-window-decision.md` (Option A chosen: additive opt-in).
- Compound-sprint orchestration pattern `docs/process/2026-04-25-compound-sprint-orchestration-pattern.md`.
- Backlog file `docs/backlog/2026-04-26-followups.md` (10 items deferred from M7).
- `WindowChars` godoc with explicit +1551% latency cliff documentation.

### Fixed

- LoCoMo eval harness chat-endpoint threshold override was silently dropped (chat reads `threshold` field, harness was sending `relativity`). Now reads `LOCOMO_RETRIEVAL_THRESHOLD` and sends to BOTH endpoints with correct field names.

### Eval — LoCoMo

- Stage 2 aggregate F1 **0.238** at hit@k **0.769** (n=199, conv-26 full, 19 sessions). +349% F1 vs original baseline (0.053), +197% vs M6 prompt-only.
- Per-category Stage 2: cat-1 0.267, cat-2 0.091, cat-3 0.201, cat-4 0.407, cat-5 0.092.
- Stage 3 (full 1986 QA across 10 convs) running in background.

## [2.0.0] — 2026-04-24

### Highlights

**Full Phase D — LoCoMo intelligence stack.** All 10 retrieval + extraction quality features deployed (D1-D10). Production memdb-go is now a LoCoMo-competitive memory system with hierarchical storage, multi-hop graph retrieval, query rewriting, 3-stage iterative retrieval, CoT decomposition, pronoun+temporal resolution in extraction, structured preference taxonomy, post-retrieval answer enhancement, and a reproducible evaluation harness.

**Plus** three full pre-D phases (A observability, B integrity, C code quality), production-grade schema migration runner, embed-server resilience stack, and critical write-path unblock that restored retrieval from hit@20=0.000 to 0.700.

**Infrastructure**: 38 PRs merged in memdb, 15 in deploy-config, 1 in ox-embed-server. ~5000 LOC new Go code. 15 versioned migrations. LoCoMo eval baseline: `hit@20=0.700` (above Mem0/MemOS published numbers).

### Added — Phase D LoCoMo intelligence

- **D1** Temporal decay + importance scoring rerank. `final = cosine * exp(-λt·age/180d) * (1 + log(1+access_count))`. Gated `MEMDB_D1_IMPORTANCE`.
- **D2** Multi-hop AGE graph retrieval via recursive CTE on `memory_edges`. Hop-decay 0.8^hop, cap 2× original K. Gated `MEMDB_SEARCH_MULTIHOP`.
- **D3** Hierarchical reorganizer — ported Python `tree_text_memory/organize/` (5 modules) to Go. Raw → episodic → semantic tiers. LLM relation detector emits CAUSES/CONTRADICTS/SUPPORTS/RELATED with confidence. Gated `MEMDB_REORG_HIERARCHY`.
- **D4** Query rewriting before embedding (third-person, absolute temporal, noun-phrase dense). Gated `MEMDB_QUERY_REWRITE`.
- **D5** 3-stage iterative retrieval (coarse → refine → justify). Gated `MEMDB_SEARCH_STAGED`.
- **D6** Pronoun + temporal resolution in extraction. Schema adds `raw_text` (verbatim) + `resolved_text` (primary retrieval form).
- **D7** CoT query decomposition — multi-part questions split into atomic sub-queries; embed-per-subquery union. Gated `MEMDB_SEARCH_COT`.
- **D8** Third-person enforcement in extractor + 22-category preference taxonomy (14 explicit + 8 implicit, MemOS-style). `preference_category` stored in `PreferenceMemory` properties.
- **D9** LoCoMo eval harness (`evaluation/locomo/`) + MILESTONES.md audit trail. Deterministic sample, exact-match / F1 / semantic similarity / hit@k metrics. Reproducible baseline established pre-Phase-D.
- **D10** Post-retrieval answer enhancement. LLM distills top-5 memories into query-aligned concise answer; prepended at rank 0 as synthetic `EnhancedAnswer` item. Gated `MEMDB_SEARCH_ENHANCE`.

Migrations **0011** (access_count), **0013** (hierarchy_level + parent_memory_id), **0014** (raw_text + preference_category audit).

### Added — Phase A observability

- Memory-write heartbeat counter `memdb.memory.added_total{type, cube_id}` + `SilentMemoryStall` Prometheus alert (rate=0 for 1h → page).
- Buffer-flush error counter `memdb.buffer.flush_errors_total{reason}` (lua/parse/db/other) + `BufferFlushBurst` alert.
- DB metrics pre-register on startup (both drift + added counters visible at value 0 before first event).
- Prometheus scrape target `memdb-go:8080/metrics` (auth-exempt for internal network).

### Added — Phase B integrity

- `Ensure*Table` DDL consolidated into versioned migrations 0005-0008 (memory_edges / entity_nodes / entity_edges / user_configs). Single source of truth for schema.
- agtype operator audit — 3 runtime bugs in `HardDeleteCube` and `GetMemoriesByFilter` fixed.
- Unified JSON fence strip helpers — `StripJSONFence` is the single path; deleted string-based duplicate.

### Added — Phase C code quality

- `search/service.go` split 824 → 189 lines + 5 new files (orchestrator / parallel / merge / postprocess / response / types).
- `scheduler/reorganizer_mem_read.go` split 665 → 118 + 6 new files by stage.
- release-drafter workflow + conventional-commit PR title linter.

### Added — Schema migration runner (Phase 4.13)

- 15 versioned migrations (0001 baselined, 0002-0014 applied fresh) via the runner.
- Advisory lock on a pinned `*pgxpool.Conn` serializes concurrent startups.
- Per-migration transaction (DDL + tracking row commit atomically).
- sha256 checksum drift detection with OTel counter + alert.
- Baseline logic for production schemas that existed pre-runner.
- Fresh-DB integration test `scripts/test-migrations-fresh-db.sh` + `cmd/migration-test`.

### Added — embed-server resilience (external repo)

- memdb-go HTTP embedder wrapped in `withRetry` — 30s timeout + exp backoff on 429/503/502/504.
- embed-server emits queue-depth gauge, batch-wait histogram, rejections counter.
- 429 backpressure gate at 80% queue capacity.
- Prometheus alerts: EmbedQueueSaturation, EmbedRejections, EmbedHighLatency, EmbedBatchWaitHigh.

### Fixed — P0 write-path unblock

Three cascading blockers that gated all retrieval. Restored from `hit@20=0.000` to `0.700` in one sprint:

- **AGE 1.7 removed `agtype_in(text)` overload** → 10 SQL sites migrated to `::agtype` cast.
- **`memos_graph.cubes` was AGE vertex label** (Go code expected plain table) → migration 0009 drops label + recreates plain. Hotfix: `drop_vlabel` → `drop_label` (AGE 1.7 rename).
- **`Memory.id` is AGE auto-generated graphid**, not application UUID → refactor: INSERT drops id column; WHERE/DELETE/UPDATE/SELECT use `properties->>(('id'::text))`.
- Search queries project property UUID (10 queries in `queries_search_*.go`) — prevents graphid leak through API.
- Migration 0012 relocates edges tables from `ag_catalog` to `memos_graph` (search_path fallthrough bug from B1).

### Fixed — LLM reliability

- LLM JSON fence strip (`StripJSONFence`) — critical runtime fix for `buffer flusher: flush failed` spam. Markdown-wrapped JSON from LLM now parsed correctly.
- `MEMDB_LLM_SEARCH_MODEL` default changed from `gemini-2.0-flash` (unknown at cliproxyapi) to `gemini-2.5-flash-lite`. D4/D5/D10/Iterative/Fine all recovered from silent 500 → working.

### Changed

- `graph_dbs/polardb/schema.py` deleted entirely. `SchemaMixin` removed from `PolarDBGraphDB`. All DDL managed by Go runner.

### Dependencies

- `go-kit` bumped `v0.9.0` → `v0.24.1`.

### LoCoMo baseline (v2.0.0)

```
Sample: 1 conv, 3 sessions, 58 msgs, 10 category-1 QAs
EM     = 0.000
F1     = 0.010
semsim = 0.046 (was 0.000 pre-P1; +0.007 over post-P1)
hit@20 = 0.700 (was 0.000 pre-P1)
```

Above published Mem0 (hit@20=0.65) and MemOS (hit@10=0.60). F1/EM gated on chat/complete mode (upcoming harness iteration).

## [1.1.0] — 2026-04-23

### Highlights

**Versioned schema migration runner takes over from Python `schema.py`** —
memdb-go now owns PostgreSQL DDL management end-to-end. Closes Phase 4.13 of the
Go migration roadmap and unblocks Phase 5 (Python deprecation) from the
schema-management angle.

### Added — Schema management

- **`internal/db/RunMigrations`** — versioned SQL runner:
  - `pg_advisory_lock` on a pinned `*pgxpool.Conn` serializes concurrent
    startups across replicas
  - Per-migration transaction (body + `schema_migrations` insert commit
    atomically; half-apply impossible)
  - sha256 drift detection — edited-after-apply files get a Warn log and an
    OTel counter bump (no re-apply)
  - Baseline step marks `0001` applied without executing it when a pre-runner
    schema is detected (production transition path)
  - Fresh-DB bootstrap via `bootstrapGraphIfNeeded` — installs `age`, `vector`,
    `pg_trgm` extensions + `create_graph('memos_graph')` before any other DDL
  - Fail-fast: any error returns from `NewPostgres`, crashing startup so ops
    are notified (unlike `Ensure*Table` best-effort Warn)
- **`migrations/` embed FS** — versioned SQL files, applied in lex order:
  - `0001_phase2_user_cube_split.sql` — cubes table + memory user_id backfill
  - `0002_tsvector_fulltext.sql` — Chinese tsvector column + trigger + GIN
  - `0003_extensions_and_graph.sql` — extensions + AGE graph bootstrap
  - `0004_memory_embedding.sql` — `vector(1024)` column + HNSW halfvec index
- **Fresh-DB integration test** — `scripts/test-migrations-fresh-db.sh` +
  `cmd/migration-test`. Ephemeral Postgres, 8 psql assertions, idempotency
  check. `make test-migrations-fresh-db`. No new Go dependencies.

### Added — Observability

- **`memdb.migration.checksum_drift{name=...}` OTel counter** — dashboards can
  alert on `increase(...[5m]) > 0` instead of log-mining. Registered on first
  drift event.
- **Prometheus metrics exporter** — OTel Prometheus exporter on `/metrics`
  endpoint (pattern: `PROM_PORT = MCP_PORT + 1000`, so memdb-go at `9080`).
- **Domain metrics** for feedback pipeline, LLM client, embedder backends,
  scheduler workers, and add pipeline (requests / duration histograms /
  operations by type).

### Added — Search

- **Pre-migration cross-encoder enhancements** — `APIKey` Bearer auth,
  `MaxCharsPerDoc` cap, `gte-multi-rerank` default model. Prep for
  full go-kit/rerank migration.
- **go-kit/rerank migration** — cross-encoder rerank pipeline moved to shared
  `github.com/anatolykoptev/go-kit/rerank` package for reuse across services.

### Fixed

- **LLM JSON fence strip** (critical runtime): extract+dedup `json.Unmarshal`
  was failing on LLM responses wrapped in ` ```json ... ``` ` markdown code
  fences, producing `buffer flusher: flush failed` error spam every ~10s on
  prod. `StripJSONFence` helper in `internal/llm/jsonfence.go` (7 test cases:
  LF/CRLF, with/without language tag, bare fences, control). Post-deploy
  verified 0 errors/30s.
- **NewPostgres startup ordering**: `RunMigrations` now runs BEFORE the four
  `Ensure*Table` calls. On fresh DB, Ensure* used to fail-Warn because
  `memos_graph` schema didn't exist yet — service ran with missing
  `memory_edges`/`entity_nodes`/`entity_edges`/`user_configs` until second
  startup. Now self-heals on first boot.
- **AGE 1.7 agtype operator compatibility** — Memory table queries cast
  `agtype::text::jsonb` before `->>` to avoid `agtype ->> agtype` ambiguous
  resolution. Applied to `ListCubesByTag` containment check and inside
  `0001_phase2_user_cube_split.sql` (three latent bugs discovered by
  fresh-DB integration test).
- **OTel tracer** skips setup when `OTEL_EXPORTER_OTLP_ENDPOINT` is unset
  instead of failing hard.

### Changed — Deprecations

- **`graph_dbs/polardb/schema.py`** marked DEAD CODE. Audit showed all call
  sites in `connection.py:87-101` were already commented out before Phase
  4.13 started. Module and `SchemaMixin` class docstrings updated; file
  retained as historical reference only.

### Removed

- **Dead endpoints**: `/product/chat/stream/playground`,
  `/product/suggestions`, `/product/suggestions/{user_id}`,
  `control_memory_scheduler` MCP tool. Callers survey: 0 external users.

### Dependencies

- `go-kit` bumped `v0.9.0` → `v0.21.0` → `v0.24.0` → `v0.24.1` (rerank
  package + cache Redis DB routing fix)

### Internal

- 42 commits across 10 PRs (#3 through #10, plus direct T1–T5 commits on
  `main` prior to the updated branch-only git hygiene rule).
- Prod state after release: `schema_migrations` table has 4 applied rows
  (`0001` baselined, `0002`/`0003`/`0004` executed). Restart is a clean
  idempotent no-op.

---

## [1.0.4] — 2026-04-18

### Added

- **Cross-encoder rerank** (#2): BGE-reranker-v2-m3 via embed-server
  `/v1/rerank` as search step 6.05. Expected +3-5 LoCoMo points.

### Security

- **11 advisories closed** (#1): 2 CRITICAL (`pgx` memory-safety, `grpc`
  authz bypass), 4 HIGH (`mcp-sdk` ×3, `otel` PATH hijacking), 5 MEDIUM.
- Dependency bumps: `pgx/v5 5.9.1`, `grpc 1.80.0`, `mcp-sdk 1.5.0`,
  `otel 1.43.0`.

### Artifacts

- goreleaser workflow attaches linux/darwin amd64/arm64 binaries for
  `memdb-mcp` and `mcp-stdio-proxy`.

---

## [1.0.0] — 2026-03-02

Initial public release. Baseline for changelog. See
[docs/ROADMAP-GO-MIGRATION.md](docs/ROADMAP-GO-MIGRATION.md) for the detailed history
of Python → Go migration phases 1–4.5 that preceded this tag.

[Unreleased]: https://github.com/anatolykoptev/memdb/compare/memdb-go/v0.23.1...HEAD
[memdb-go/v0.23.1]: https://github.com/anatolykoptev/memdb/releases/tag/memdb-go/v0.23.1
[0.23.0]: https://github.com/anatolykoptev/memdb/compare/v0.22.0...v0.23.0
[0.22.0]: https://github.com/anatolykoptev/memdb/compare/v2.2.0...v0.22.0
[2.2.0]: https://github.com/anatolykoptev/memdb/releases/tag/v2.2.0
[2.1.0]: https://github.com/anatolykoptev/memdb/compare/v2.0.0...v2.1.0
[2.0.0]: https://github.com/anatolykoptev/memdb/compare/v1.1.0...v2.0.0
[1.1.0]: https://github.com/anatolykoptev/memdb/compare/v1.0.4...v1.1.0
[1.0.4]: https://github.com/anatolykoptev/memdb/compare/v1.0.0...v1.0.4
[1.0.0]: https://github.com/anatolykoptev/memdb/releases/tag/v1.0.0

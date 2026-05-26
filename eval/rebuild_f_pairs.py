"""One-shot: rewrite F_long_document with section-discrimination shape.

Each pair: 6 sections from the substrate plan. One is correct, the other
5-pick-4 are distractors. All candidates share substrate-plan vocabulary,
so the model has to discriminate the section that addresses the query.

This is the proper test for information-density: previous F failed because
distractors used wholly different topics, making it just topic discrimination.
"""
import json
from pathlib import Path

p = Path(__file__).parent / "pairs.json"
data = json.loads(p.read_text(encoding="utf-8"))

SECTIONS = {
    "predicate_ast":
        "rrc.Predicate is a typed AST for filtering chunk retrieval. Backends "
        "implement a Compile method that lowers Predicate to their native filter "
        "form. The interface is sealed via the unexported predicateMarker method, "
        "so backend type-switches can be exhaustive. New predicate types are "
        "added without touching the engine. Each backend adds a case to its "
        "compile function; backends that don't support a type return an error or "
        "fall through to a safe default. Concrete predicates: PredAll matches "
        "every chunk. PredThread matches chunks whose owning thread equals "
        "ThreadID. PredScope encodes the user-visible retrieval scope: with "
        "Scope = ScopeThread, matches chunks from CurrentThread; with Scope = "
        "ScopeAll, matches chunks from any thread. PredAnd matches chunks "
        "satisfying every child predicate. PredOr matches chunks satisfying at "
        "least one child predicate. PredNot matches chunks that do not satisfy "
        "Inner. PredHasMetadata matches chunks whose metadata Key equals Value. "
        "The metadata key/value space is open — backends decide which keys they "
        "index. Future filters such as attached-file presence, role, or "
        "time-window add new Pred types without touching the engine or the "
        "interface. The retrieval substrate gets a clean abstraction: the "
        "engine stops doing cosine math; it asks the oracle for nearest chunks "
        "under a filter, with sub-linear cost.",

    "storage_layer":
        "The vector index becomes a first-class storage primitive in the SQLite "
        "file alongside messages, chunks, scores, edges. Implementation via the "
        "sqlite-vec extension. New schema managed by service/storage/migrations: "
        "chunk_vectors is a vec0 virtual table keyed by chunk_rowid INTEGER "
        "PRIMARY KEY with payload embedding FLOAT[D] where D is the embedder's "
        "output dimension. chunk_vector_metadata is a companion regular table "
        "indexed alongside chunk_vectors with columns chunk_rowid INTEGER PK, "
        "message_id TEXT, chunk_index INTEGER, thread_id TEXT, model_id TEXT, "
        "role TEXT, has_attachments BOOL — start with the filters needed today, "
        "extend as predicates demand. The embeddings table is removed in favor "
        "of chunk_vectors as the source of truth for vector data. HNSW "
        "parameters: vec0 defaults are sane for our scale; tune M=16, "
        "ef_construction=200, ef_search=64 initially, leave knobs accessible "
        "via config for later. Filter integration: NearestChunks lowers the "
        "predicate to a SQL WHERE clause that joins chunk_vectors against "
        "chunk_vector_metadata and pushes the predicate into the vec0 KNN "
        "search. Pre-traversal filtering for high recall rather than "
        "post-filter. Concurrency: SQLite WAL handles single-writer/"
        "multi-reader. The existing engine mutex pattern continues; vector "
        "writes happen in the same transaction as the embedding insert.",

    "embedder_layer":
        "zembed-1 is asymmetric — distinct prompts for queries vs documents, "
        "applied automatically by sentence-transformers via model.encode_query "
        "and model.encode_document. The current core.Embedder interface is "
        "symmetric. Interface change: replace symmetric Embed and EmbedBatch "
        "with asymmetric EmbedQuery and EmbedDocument, both signatures taking "
        "a slice of strings and returning a slice of float32 slices. All "
        "adapters implementing the interface update accordingly. Engine call "
        "sites: the new message being processed embeds via EmbedQuery; chunks "
        "being indexed into the corpus embed via EmbedDocument. For symmetric "
        "models like bge-m3, the implementation routes both methods to the same "
        "code path. Asymmetric is the principled default; symmetric is a "
        "degenerate case. Serving stack: custom Python service. TEI's "
        "default-prompt-name is server-wide, set at boot, not per-request — "
        "running asymmetric models would require two TEI containers per "
        "embedder. zembed-1's trust_remote_code modeling defines the "
        "encode_query and encode_document semantics; vLLM ingests weights and "
        "applies its own inference, so it can't faithfully reproduce them. A "
        "custom Python service of about 50 LOC plus Dockerfile loads zembed-1 "
        "via sentence-transformers, exposes two endpoints /embed/query and "
        "/embed/document, routes to model.encode_query and model.encode_document "
        "respectively. Single service, single GPU instance, correct semantics "
        "by construction.",

    "reranker_layer":
        "zerank-2 served via vLLM, which provides causal LM serving with an "
        "OpenAI-compatible API. vLLM exposes per-token logprobs; the Go adapter "
        "extracts the Yes token logprob from the completion response and "
        "applies sigmoid of logit divided by 5.0. Prefix KV caching in vLLM is "
        "free: scoring N candidates against 1 query reuses the query-prefix's "
        "KV state across the N forward passes since the chat-template prefix "
        "through user content start is identical across them. vLLM is the "
        "principled fit because zerank-2 is a causal LM; vLLM is what causal "
        "LMs are served by; Spidey's core/adapter/openai already speaks the "
        "wire protocol; the new adapter is small with about 30-50 LOC of "
        "yes-logit extraction plus sigmoid. Production-grade serving with "
        "prefix caching, dynamic batching, paged attention — all properties "
        "zerank-2's per-call cost benefits from. Single inference stack pattern "
        "across LM-served work: future LM-as-judge, LM-as-rerank, and main "
        "completer all run via vLLM. The reranker swap drops BAAI bge-reranker-"
        "v2-m3 and the spidey-tei-rerank-1 Docker container from the production "
        "stack.",

    "subagent_semantics":
        "Engine.Fork shares the underlying ChunkOracle, so chunks and vector "
        "index are corpus-level, not per-fork. Forked engines diverge on the "
        "DAG which is the subagent's edge view, and on the score cache which "
        "is the subagent's scoring memory. Vector retrieval calls the same "
        "shared oracle. Confirmed: identity in fork lives in the reasoning "
        "thread; corpus is one. This is consistent with the directive that "
        "subagents share the corpus. Engine.Fork does not snapshot the chunk "
        "store or the vector index. Forks operate on the same chunks, same "
        "vectors, same index. Schema-level model identity records model_id per "
        "chunk vector, so embedder swap is index rebuild, not schema "
        "migration. zembed-1's native dim is 2560; vec0 schema is embedding "
        "FLOAT[2560]. Matryoshka truncation to 1280, 640 etc. is a future "
        "config knob if storage becomes load-bearing. The library exposes "
        "clean swap seams; library consumers can plug different models without "
        "forking. User direction grounding the scope: no backward compat, no "
        "migrations, SQLite stays, Go-idiomatic swappable defaults, filter "
        "extensibility is first-class.",

    "migration_steps":
        "Per directive: no migration. On deployment: Stop the running "
        "spidey-web binary; docker compose down for the old TEI containers. "
        "Wipe embeddings, scores, edges tables, or drop the SQLite file "
        "entirely in dev environment. Apply schema migration creating "
        "chunk_vectors vec0 dim 2560 plus chunk_vector_metadata. docker "
        "compose up -d brings up spidey-vllm-1 zerank-2 and spidey-zembed-1 "
        "zembed-1 via Python service. Healthchecks green before spidey-web "
        "boots. Boot spidey-web. Vector index empty; populates as messages "
        "arrive, or eagerly via existing background backfill goroutine in "
        "service/runtime/substrate. End-to-end smoke tests: task build clean "
        "across all modules rrc plus core plus service plus web. task test "
        "clean. New tests for predicate composition and lowering, "
        "sqlitevec_oracle returning top-K under various predicates with recall "
        "vs full-scan baseline above 0.95, zerank adapter HTTP shape sigmoid "
        "math logprob extraction, zembed adapter HTTP shape asymmetric routing "
        "query vs document endpoints L2-normalization confirmation. Settings "
        "JSON reflects new providers classifier=zerank embedder=zembed.",
}

PAIRS = [
    ("F1_predicate_design", "predicate_ast",
     "where in the substrate plan does it describe the Predicate AST and how backends compile it to native filter representations"),
    ("F2_storage_design", "storage_layer",
     "where in the substrate plan does it describe how sqlite-vec is integrated, the HNSW tuning, and how predicate filters get pushed into the KNN search"),
    ("F3_embedder_design", "embedder_layer",
     "where in the substrate plan does it describe the asymmetric query and document encoding and why a custom Python service is needed instead of TEI"),
    ("F4_reranker_design", "reranker_layer",
     "where in the substrate plan does it describe how the vLLM Yes-token logprob is extracted and why prefix caching matters for batched scoring"),
    ("F5_subagent_design", "subagent_semantics",
     "where in the substrate plan does it describe Engine.Fork sharing the chunk oracle and how identity diverges in a subagent"),
    ("F6_migration_steps", "migration_steps",
     "where in the substrate plan does it list the deployment steps for stopping the old TEI containers and bringing up the new vllm and zembed services"),
]

f_pairs = []
for pair_id, correct_key, query in PAIRS:
    distractor_keys = [k for k in SECTIONS if k != correct_key]
    f_pairs.append({
        "id": pair_id,
        "query": query,
        "correct": SECTIONS[correct_key],
        "distractors": [SECTIONS[k] for k in distractor_keys[:4]],
    })

data["categories"]["F_long_document"] = f_pairs
p.write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
total = sum(len(v) for v in data["categories"].values())
print(f"F_long_document rewritten: {len(f_pairs)} pairs (section-discrimination shape). Total pairs across all categories: {total}")

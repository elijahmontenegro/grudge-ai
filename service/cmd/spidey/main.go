package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/gorilla/websocket"

	"github.com/emontenegr/spidey/core"
	"github.com/emontenegr/spidey/core/adapter/tei"
	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"github.com/emontenegr/spidey/rrc"
	"github.com/emontenegr/spidey/service/agent"
	"github.com/emontenegr/spidey/service/api"
	"github.com/emontenegr/spidey/service/config"
	"github.com/emontenegr/spidey/service/graph"
	"github.com/emontenegr/spidey/service/hooks"
	"github.com/emontenegr/spidey/service/prompt"
	"github.com/emontenegr/spidey/service/proxy"
	"github.com/emontenegr/spidey/service/sandbox"
	"github.com/emontenegr/spidey/service/search"
	skillspkg "github.com/emontenegr/spidey/service/skills"
	"github.com/emontenegr/spidey/service/storage"
	"github.com/emontenegr/spidey/service/tray"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	// tiktoken is the committed token estimator for context-budget
	// sizing. If it can't load at boot — corrupt cache, network
	// unreachable for first-run fetch — we refuse to start rather
	// than silently degrade to a char-based heuristic that would
	// change the unit every downstream budget check operates in.
	if err := rrc.InitTokenEncoder(); err != nil {
		log.Fatalf("token encoder: %v", err)
	}

	db, err := storage.Open(cfg.DataDir)
	if err != nil {
		log.Fatalf("storage: %v", err)
	}
	defer db.Close()

	// Backfill unnamed threads from first message content
	if n := db.BackfillThreadNames(); n > 0 {
		log.Printf("Named %d unnamed threads from first message", n)
	}

	// Sandbox preflight — non-fatal. Threads with sandboxed=true will
	// fail at Bash-call time with the same error if the image is not
	// built; logging here makes the situation visible at boot instead
	// of surprising the user mid-turn.
	if err := sandbox.CheckReady(); err != nil {
		log.Printf("Sandbox not ready: %v (sandboxed=false threads unaffected)", err)
	} else {
		log.Printf("Sandbox ready: image %s", sandbox.Image)
	}

	// Build providers from config — nil when not yet configured (first run).
	// The small-fast completer is gone (it was QUD's extractor, also gone);
	// RRC's substrate is now classifier (NLI + embed composite) only.
	var (
		mainCompleter core.Completer
		classifier    core.Classifier
		embedder      core.Embedder
		providers     []core.Provider
	)

	if mainCfg, ok := cfg.Settings.Providers["main"]; ok {
		p, err := config.BuildProvider(mainCfg)
		if err != nil {
			log.Fatalf("main provider: %v", err)
		}
		providers = append(providers, p)
		mainCompleter, err = p.Completer(mainCfg.Model)
		if err != nil {
			log.Fatalf("main completer (%s/%s): %v", mainCfg.Adapter, mainCfg.Model, err)
		}
	}

	// Build composite classifier: NLI + embedding similarity, max signal wins.
	// classifier config points to NLI model, embedder config points to embedding model.
	var nliURL, embedURL string
	if clsCfg, ok := cfg.Settings.Providers["classifier"]; ok {
		nliURL = clsCfg.BaseURL
	}
	if embCfg, ok := cfg.Settings.Providers["embedder"]; ok {
		embedURL = embCfg.BaseURL
	}
	if nliURL != "" || embedURL != "" {
		classifier = tei.NewCompositeClassifier(nliURL, embedURL)
		log.Printf("Composite classifier: NLI=%q, embed=%q", nliURL, embedURL)
	}

	// Embedder for semantic search (separate from classifier)
	if embCfg, ok := cfg.Settings.Providers["embedder"]; ok {
		p, err := config.BuildProvider(embCfg)
		if err != nil {
			log.Fatalf("embedder provider: %v", err)
		}
		providers = append(providers, p)
		embedder, err = p.Embedder(embCfg.Model)
		if err != nil {
			log.Fatalf("embedder (%s/%s): %v", embCfg.Adapter, embCfg.Model, err)
		}
	}

	defer func() {
		for _, p := range providers {
			p.Close()
		}
	}()

	if classifier == nil || mainCompleter == nil {
		log.Printf("WARNING: providers not fully configured — configure at http://spidey.localhost:8420/settings")
	}

	// Resolve template directory early — needed by both engine config and prompt assembler
	templateDir := "templates"
	if exePath, err := os.Executable(); err == nil {
		for _, c := range []string{
			filepath.Join(filepath.Dir(exePath), "templates"),
			filepath.Join(filepath.Dir(exePath), "..", "templates"),
			filepath.Join(filepath.Dir(exePath), "..", "..", "..", "templates"),
			"templates",
		} {
			if info, err := os.Stat(c); err == nil && info.IsDir() {
				templateDir = c
				break
			}
		}
	}

	// Initialize RRC engine. Classifier is the only dependency now.
	// Config precedence: if Settings.Engine is the zero struct, nothing was
	// ever persisted (first run, or legacy config.json with no `engine`
	// block) — boot on DefaultConfig. Otherwise trust the persisted
	// snapshot verbatim: zero in an individual field is intentional
	// (WeightCE=0 → pure-temporal, ScoreFloor=0 → no cutoff, etc), the
	// same rule the UpdateSettings resolver enforces.
	rrcCfg := rrc.DefaultConfig()
	if se := cfg.Settings.Engine; se != (config.EngineConfig{}) {
		rrcCfg.EdgeThreshold = se.EdgeThreshold
		rrcCfg.ScoreFloor = se.ScoreFloor
		rrcCfg.WeightCE = se.WeightCE
		rrcCfg.WeightTemp = se.WeightTemp
		rrcCfg.ZScoreThreshold = se.ZScoreThreshold
		rrcCfg.MinBatchStdDev = se.MinBatchStdDev
		rrcCfg.RadiusSize = se.RadiusSize
		rrcCfg.RerankTopK = se.RerankTopK
		rrcCfg.ContextBudgetTokens = se.ContextBudgetTokens
	}
	engine := rrc.NewEngine(rrcCfg, classifier)

	// Wire chunking at the storage layer. Messages go in; paragraph-
	// sized chunks come out, atomic with the message row.
	chunkFn := func(text string) []storage.Chunk {
		rcs := rrc.ChunkText(text, rrcCfg.Chunk)
		out := make([]storage.Chunk, len(rcs))
		for i, c := range rcs {
			out[i] = storage.Chunk{
				ChunkIndex: c.Index,
				Text:       c.Text,
				ByteStart:  c.ByteStart,
				ByteEnd:    c.ByteEnd,
				TokenEst:   c.TokenEst,
			}
		}
		return out
	}
	db.SetChunker(chunkFn)

	// Chunk backfill for pre-existing messages. Messages inserted
	// before v2 migration have no chunks table rows — they'd be
	// invisible to RRC scoring. Iterate every chunkless message, run
	// its text through the chunker, persist. Synchronous on startup
	// because embeddings backfill below depends on chunks existing
	// first; the whole thing is a fast walk over blob-decode + local
	// chunking, no network calls.
	if err := backfillChunks(db, chunkFn); err != nil {
		log.Printf("Chunk backfill: %v", err)
	}

	// Resolve model IDs — used as cache keys for scores (reranker) and
	// embeddings (embedder). Switching a model in settings shifts the
	// keys; old rows stay tagged under their original model_id,
	// harmless.
	rerankerModelID := ""
	if clsCfg, ok := cfg.Settings.Providers["classifier"]; ok {
		rerankerModelID = clsCfg.Model
	}
	embedModelID := ""
	if embCfg, ok := cfg.Settings.Providers["embedder"]; ok {
		embedModelID = embCfg.Model
	}

	// Load persisted state
	if edges, err := db.AllEdges(); err == nil && len(edges) > 0 {
		engine.LoadDAG(edges)
		log.Printf("Loaded %d edges", len(edges))
	}
	if rerankerModelID != "" {
		if scores, err := db.ChunkScoresForModel(rerankerModelID); err == nil && len(scores) > 0 {
			// Convert storage.ChunkScoreKey → rrc.ScoreKey
			converted := make(map[rrc.ScoreKey]float64, len(scores))
			for k, v := range scores {
				converted[rrc.ScoreKey{
					FromMsgID:    k.FromID,
					FromChunkIdx: k.FromIdx,
					ToMsgID:      k.ToID,
					ToChunkIdx:   k.ToIdx,
				}] = v
			}
			engine.LoadScoreCache(converted)
			log.Printf("Loaded %d chunk-pair scores (model=%s)", len(scores), rerankerModelID)
		}
	}

	// Wire score persistence — every reranker-produced chunk-pair
	// score writes through to the scores table so restarts and forks
	// inherit the cache per spec.
	if rerankerModelID != "" {
		engine.Scores().SetPersister(func(fromID string, fromIdx int, toID string, toIdx int, score float64) {
			if err := db.InsertChunkScore(fromID, fromIdx, toID, toIdx, rerankerModelID, score); err != nil {
				log.Printf("InsertChunkScore(%s[%d], %s[%d], %s): %v", fromID, fromIdx, toID, toIdx, rerankerModelID, err)
			}
		})
	}

	// Searcher + ChunkOracle share the same embedder and model ID.
	// Searcher powers the /search query; ChunkOracle feeds RRC rerank.
	var searcher *search.Searcher
	var chunkOracle *search.ChunkOracle
	if embedder != nil && embedModelID != "" {
		searcher = search.NewSearcher(embedder, embedModelID, db)
		chunkOracle = search.NewChunkOracle(db, embedder, embedModelID)
		engine.SetChunkOracle(chunkOracle)

		// Backfill chunk embeddings in the background. Non-blocking —
		// service accepts requests immediately; OnMessage misses on
		// not-yet-backfilled chunks just embed live.
		go chunkOracle.BackfillEmbeddings(context.Background())
	}

	// Load MCP tools from config
	var mcpConfigs []agent.MCPServerConfig
	for _, srv := range cfg.Settings.MCPServers {
		mcpConfigs = append(mcpConfigs, agent.MCPServerConfig{
			Name: srv.Name, Endpoint: srv.Endpoint, Enabled: srv.Enabled,
		})
	}
	mcpToolsets := agent.LoadMCPTools(mcpConfigs)
	if len(mcpToolsets) > 0 {
		log.Printf("Loaded %d MCP toolsets", len(mcpToolsets))
	}
	mcpTools, err := agent.MCPToolsAsTools(mcpToolsets)
	if err != nil {
		log.Fatalf("mcp tools: %v", err)
	}

	// Prompt assembler — uses templateDir resolved above
	assembler := prompt.NewAssembler(templateDir)
	log.Printf("Prompt templates: %s", templateDir)

	// Hooks dispatcher
	hookDispatcher := hooks.NewDispatcher(cfg.Settings.Hooks)

	// Load skills from user + project directories
	loadedSkills := skillspkg.LoadAll(filepath.Join(cfg.DataDir, "skills"), nil)
	if len(loadedSkills) > 0 {
		log.Printf("Loaded %d skills", len(loadedSkills))
	}

	// GraphQL
	resolver := graph.NewResolver(db, engine, cfg, searcher, mainCompleter)
	resolver.Assembler = assembler
	resolver.Hooks = hookDispatcher
	resolver.Skills = loadedSkills
	resolver.MCPTools = mcpTools

	// Reconcile agent_state rows left non-Idle by the prior session.
	// Goroutines don't survive process exit — any Running/Paused row
	// in DB references a runner that no longer exists. Autonomous
	// runs with remaining budget auto-resume; everything else
	// normalizes to Idle so the UI stops lying. Runs synchronously
	// before the HTTP listener starts so the first GraphQL query
	// sees the reconciled state, not the stale DB row.
	resolver.ReconcileOnBoot(ctx)
	gqlSrv := handler.New(graph.NewExecutableSchema(graph.Config{Resolvers: resolver}))
	gqlSrv.SetErrorPresenter(graph.ErrorPresenter)
	gqlSrv.AddTransport(transport.POST{})
	gqlSrv.AddTransport(transport.Websocket{
		KeepAlivePingInterval: 10 * time.Second,
		Upgrader: websocket.Upgrader{
			CheckOrigin:  func(r *http.Request) bool { return true },
			Subprotocols: []string{"graphql-transport-ws", "graphql-ws"},
		},
	})

	// Proxy
	proxyHandler := proxy.NewHandler(classifier, mainCompleter, rrc.DefaultConfig())

	// Routes
	mux := http.NewServeMux()
	proxyHandler.RegisterRoutes(mux)
	mux.Handle("/graphql", gqlSrv)

	// Attachment upload/download. Files land in the thread's sandbox
	// workspace so they're immediately accessible to the agent via
	// FileRead — same path surface whether sandboxed=true or not.
	attachmentMgr := api.NewManager(cfg.DataDir)
	mux.HandleFunc("/api/attachments/", func(w http.ResponseWriter, r *http.Request) {
		// One prefix routes both upload (POST) and download (GET) so
		// clients don't need separate endpoints to construct.
		if r.Method == http.MethodGet {
			attachmentMgr.HandleDownload(w, r)
			return
		}
		attachmentMgr.HandleUpload(w, r)
	})

	// Static assets — check multiple paths for web/dist
	var webDist string
	candidates := []string{
		filepath.Join(filepath.Dir(os.Args[0]), "..", "..", "..", "web", "dist"),
		filepath.Join("web", "dist"),
		filepath.Join("..", "web", "dist"),
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && info.IsDir() {
			webDist = c
			break
		}
	}
	if webDist != "" {
		fs := http.Dir(webDist)
		fileServer := http.FileServer(fs)
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			if path != "/" {
				if _, err := fs.Open(path); err != nil {
					// Asset-like paths must 404 cleanly. Falling back to
					// index.html sends HTML for a <script src> and browsers
					// reject it with "Expected a JavaScript-or-Wasm module
					// script" errors — looks like the whole app is broken.
					isAsset := strings.HasPrefix(path, "/assets/") ||
						strings.HasSuffix(path, ".js") ||
						strings.HasSuffix(path, ".mjs") ||
						strings.HasSuffix(path, ".css") ||
						strings.HasSuffix(path, ".map") ||
						strings.HasSuffix(path, ".json") ||
						strings.HasSuffix(path, ".svg") ||
						strings.HasSuffix(path, ".png") ||
						strings.HasSuffix(path, ".ico") ||
						strings.HasSuffix(path, ".woff") ||
						strings.HasSuffix(path, ".woff2")
					if isAsset {
						http.NotFound(w, r)
						return
					}
					// SPA fallback for client-side routes (/thread/:id etc).
					r.URL.Path = "/"
				}
			}
			fileServer.ServeHTTP(w, r)
		})
		log.Printf("Serving web UI from %s", webDist)
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<!doctype html><html><body><h1>Spidey</h1><p>Web UI not built. Run <code>cd web &amp;&amp; npm run build</code></p></body></html>`))
		})
	}

	// Bind to 127.0.0.1:8420 — browsers resolve spidey.localhost per RFC 6761,
	// but the OS-level resolver on Windows doesn't handle *.localhost subdomains.
	bindAddr := "127.0.0.1:8420"
	publicURL := "http://spidey.localhost:8420"
	srv := &http.Server{Addr: bindAddr, Handler: mux}

	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		srv.Shutdown(shutdownCtx)
	}()

	// Start system tray in background — provides Open/Settings/Quit menu
	go tray.Run(cancel)

	log.Printf("Spidey listening on %s (%s)", publicURL, bindAddr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("server: %v", err)
	}
}

// backfillChunks populates the chunks table for every message that
// has none. Runs synchronously on startup because every other RRC
// substrate (embeddings, scores) keys off chunks — if they don't
// exist, the engine can't score anything. Cheap enough to block
// startup: no network calls, pure text-chunking in Go, hundreds of
// messages per second.
func backfillChunks(db *storage.DB, chunk func(string) []storage.Chunk) error {
	ids, err := db.MessagesWithoutChunks()
	if err != nil {
		return fmt.Errorf("MessagesWithoutChunks: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}
	log.Printf("Chunk backfill: processing %d pre-existing messages", len(ids))
	start := time.Now()
	var withChunks, withoutText int
	for _, id := range ids {
		msg, err := db.GetMessage(id)
		if err != nil || msg == nil {
			continue
		}
		text := textFromProto(msg.Content)
		if text == "" {
			withoutText++
			continue
		}
		chunks := chunk(text)
		if len(chunks) == 0 {
			withoutText++
			continue
		}
		if err := db.InsertChunks(id, chunks); err != nil {
			log.Printf("Chunk backfill: InsertChunks(%s): %v", id, err)
			continue
		}
		withChunks++
	}
	log.Printf("Chunk backfill: chunked %d messages (%d text-only, %d empty) in %v",
		withChunks, withChunks, withoutText, time.Since(start))
	return nil
}

// textFromProto mirrors storage.textFromBlocks at the main package
// boundary so the backfill doesn't need a storage package API just
// for text extraction. Kept in sync with storage/messages.go:textFromBlocks
// and rrc/engine.go:textFromMessage — all three concatenate the same
// scorable blocks so the chunking/scoring/embedding keys stay
// coherent with each other.
func textFromProto(blocks []*pb.ContentBlock) string {
	var sb strings.Builder
	for _, b := range blocks {
		if t := b.GetText(); t != nil {
			sb.WriteString(t.Text)
			sb.WriteByte('\n')
		} else if t := b.GetThinking(); t != nil {
			sb.WriteString(t.Text)
			sb.WriteByte('\n')
		} else if tc := b.GetToolCall(); tc != nil {
			sb.WriteString(tc.Name)
			sb.WriteString(": ")
			sb.WriteString(tc.Arguments)
			sb.WriteByte('\n')
		} else if tr := b.GetToolResult(); tr != nil {
			sb.WriteString(tr.Content)
			sb.WriteByte('\n')
		} else if a := b.GetAttachment(); a != nil {
			sb.WriteString("[attached: ")
			sb.WriteString(a.Filename)
			sb.WriteString(" at ")
			sb.WriteString(a.Path)
			sb.WriteString("]\n")
			if a.InlinedText != "" {
				sb.WriteString(a.InlinedText)
				sb.WriteByte('\n')
			}
		}
	}
	return sb.String()
}

package main

import (
	"context"
	"embed"
	"io/fs"
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

	// Blank-imported for side-effect registration with core.NewProvider.
	// Importing core alone gives an empty registry; each adapter
	// package wires itself in init() in core/adapter/X/register.go.
	_ "github.com/emontenegr/spidey/core/adapter/anthropic"
	_ "github.com/emontenegr/spidey/core/adapter/googleai"
	_ "github.com/emontenegr/spidey/core/adapter/ollama"
	_ "github.com/emontenegr/spidey/core/adapter/openai"
	_ "github.com/emontenegr/spidey/core/adapter/vllm"
	"github.com/emontenegr/spidey/rrc"
	"github.com/emontenegr/spidey/rrc/tiktoken"
	"github.com/emontenegr/spidey/service/agent"
	"github.com/emontenegr/spidey/service/api"
	"github.com/emontenegr/spidey/service/config"
	"github.com/emontenegr/spidey/service/graph"
	"github.com/emontenegr/spidey/service/hooks"
	"github.com/emontenegr/spidey/service/prompt"
	"github.com/emontenegr/spidey/service/proxy"
	"github.com/emontenegr/spidey/service/runtime/substrate"
	"github.com/emontenegr/spidey/service/sandbox"
	skillspkg "github.com/emontenegr/spidey/service/skills"
	"github.com/emontenegr/spidey/service/storage"
	"github.com/emontenegr/spidey/service/tray"
)

// Embedded web bundle. The Taskfile's embed-sync task copies the
// freshly-built web/dist into ./dist before `go build`. The
// directory is in .gitignore — it's a build artifact, not source.
//
//go:embed all:dist
var embeddedWeb embed.FS

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
	tokenEst, err := tiktoken.New()
	if err != nil {
		log.Fatalf("token estimator: %v", err)
	}
	rrc.SetDefaultEstimator(tokenEst)

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

	// Construct the runtime substrate (providers + engine + storage
	// triple, score/edge hydration, score persister wiring, chunk
	// oracle wiring). Every previous inline construction step is
	// now in service/runtime/substrate.Build.
	subs, err := substrate.Build(ctx, cfg, db)
	if err != nil {
		log.Fatalf("substrate: %v", err)
	}

	// Resolve template directory — needed by the prompt assembler.
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
	resolver := graph.NewResolver(db, subs.Engine, cfg, subs.Searcher, subs.MainCompleter)
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
	proxyHandler := proxy.NewHandler(subs.Classifier, subs.MainCompleter, rrc.DefaultConfig())

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

	// Static assets — embedded via //go:embed dist (Taskfile's
	// embed-sync task copies web/dist into ./dist before build).
	// Self-contained binary; no runtime path lookup, no ../../../web/dist
	// candidate list, no surprises when the binary ships standalone.
	webRoot, err := fs.Sub(embeddedWeb, "dist")
	if err != nil {
		log.Fatalf("embedded web: %v", err)
	}
	if _, err := fs.Stat(webRoot, "index.html"); err == nil {
		fileServer := http.FileServer(http.FS(webRoot))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			if path != "/" {
				if _, err := fs.Stat(webRoot, strings.TrimPrefix(path, "/")); err != nil {
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
		log.Printf("Serving web UI from embedded dist")
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<!doctype html><html><body><h1>Spidey</h1><p>Web UI not built into binary. Run <code>task embed-sync</code> then rebuild.</p></body></html>`))
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

// (backfillChunks moved to service/runtime/substrate.)

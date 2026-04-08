package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/gorilla/websocket"

	"github.com/emontenegr/spidey/core"
	"github.com/emontenegr/spidey/core/adapter/tei"
	"github.com/emontenegr/spidey/rrc"
	"github.com/emontenegr/spidey/service/agent"
	"github.com/emontenegr/spidey/service/config"
	"github.com/emontenegr/spidey/service/graph"
	"github.com/emontenegr/spidey/service/proxy"
	"github.com/emontenegr/spidey/service/search"
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

	db, err := storage.Open(cfg.DataDir)
	if err != nil {
		log.Fatalf("storage: %v", err)
	}
	defer db.Close()

	// Backfill unnamed threads from first message content
	if n := db.BackfillThreadNames(); n > 0 {
		log.Printf("Named %d unnamed threads from first message", n)
	}

	// Build providers from config — nil when not yet configured (first run)
	var (
		mainCompleter  core.Completer
		classifier     core.Classifier
		embedder       core.Embedder
		smallCompleter core.Completer
		providers      []core.Provider
	)

	if mainCfg, ok := cfg.Settings.Providers["main"]; ok {
		p, err := config.BuildProvider(mainCfg)
		if err != nil {
			log.Printf("main provider: %v", err)
		} else {
			providers = append(providers, p)
			mainCompleter, _ = p.Completer(mainCfg.Model)
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
			log.Printf("embedder provider: %v", err)
		} else {
			providers = append(providers, p)
			embedder, _ = p.Embedder(embCfg.Model)
		}
	}

	if sfCfg, ok := cfg.Settings.Providers["small_fast"]; ok {
		p, err := config.BuildProvider(sfCfg)
		if err != nil {
			log.Printf("small_fast provider: %v", err)
		} else {
			providers = append(providers, p)
			smallCompleter, _ = p.Completer(sfCfg.Model)
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

	// Initialize RRC engine — embedder for dependency scoring, classifier optional
	engine := rrc.NewEngine(rrc.DefaultConfig(), classifier, smallCompleter)

	// Load persisted state
	if edges, err := db.AllEdges(); err == nil && len(edges) > 0 {
		engine.LoadDAG(edges)
		log.Printf("Loaded %d edges", len(edges))
	}
	if scores, err := db.AllScores(); err == nil && len(scores) > 0 {
		engine.LoadScoreCache(scores)
		log.Printf("Loaded %d scores", len(scores))
	}
	if threadIDs, err := db.AllThreadsWithQUDs(); err == nil {
		for _, tid := range threadIDs {
			if g, err := db.QUDGraphForThread(tid); err == nil {
				engine.LoadQUDGraph(tid, g)
			}
		}
		if len(threadIDs) > 0 {
			log.Printf("Loaded QUD graphs for %d threads", len(threadIDs))
		}
	}

	// Searcher (nil if embedder unavailable)
	var searcher *search.Searcher
	if embedder != nil {
		model := ""
		if clsCfg, ok := cfg.Settings.Providers["classifier"]; ok {
			model = clsCfg.Model
		}
		searcher = search.NewSearcher(embedder, model, db)
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

	// GraphQL
	resolver := graph.NewResolver(db, engine, cfg, searcher, mainCompleter)
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
			// SPA fallback: try the file first, serve index.html for unknown paths
			path := r.URL.Path
			if path != "/" {
				if _, err := fs.Open(path); err != nil {
					// Not a static file — serve index.html for client-side routing
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
		srv.Shutdown(context.Background())
	}()

	go tray.OpenBrowser(publicURL)

	log.Printf("Spidey listening on %s (%s)", publicURL, bindAddr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("server: %v", err)
	}
}

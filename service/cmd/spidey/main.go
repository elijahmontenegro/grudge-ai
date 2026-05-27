package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"runtime"
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
	_ "github.com/emontenegr/spidey/core/adapter/tei"
	_ "github.com/emontenegr/spidey/core/adapter/vllm"
	_ "github.com/emontenegr/spidey/core/adapter/zerank"
	"github.com/emontenegr/spidey/service/config"
	"github.com/emontenegr/spidey/service/graph"
	"github.com/emontenegr/spidey/service/kernel"
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

	// Mutex and block profiles are empty unless sampling is enabled
	// (CPU and heap profiles work without these). The fractions below
	// are the ones used by Go core's own diagnostics — low enough that
	// the sampling itself isn't a load source, high enough to surface
	// real contention. CPU profile (/debug/pprof/profile) is always on.
	runtime.SetMutexProfileFraction(5)
	runtime.SetBlockProfileRate(1)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	// Bootstrap consolidates: token estimator, storage, sandbox preflight,
	// substrate (providers + engine), MCP toolset loading, prompt
	// assembler, hooks dispatcher, skill loading, runner registry, embed
	// queue, and the per-thread caches the runner factory closes over.
	k, err := kernel.Bootstrap(ctx, cfg)
	if err != nil {
		log.Fatalf("kernel: %v", err)
	}
	defer k.Shutdown()

	resolver := graph.NewResolver(k)

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

	mux := http.NewServeMux()
	mux.Handle("/graphql", gqlSrv)

	// pprof routes — same registration the net/http/pprof init() would
	// do against DefaultServeMux, replayed on our custom mux. Loopback
	// binding is the only access control today; if the service ever
	// binds publicly these should be gated behind a 127.0.0.1 check.
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	// Attachment upload/download. Files land in the thread's sandbox
	// workspace so they're immediately accessible to the agent via
	// FileRead — same path surface whether sandboxed=true or not.
	attachmentMgr := graph.NewAttachmentManager(cfg.DataDir)
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
	// Self-contained binary; no runtime path lookup, no surprises
	// when the binary ships standalone.
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

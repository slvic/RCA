package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/slvic/rca-agent/internal/agent"
	"github.com/slvic/rca-agent/internal/config"
	"github.com/slvic/rca-agent/internal/llm"
	"github.com/slvic/rca-agent/internal/mcp"
	"github.com/slvic/rca-agent/internal/report"
	"github.com/slvic/rca-agent/internal/webhook"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	alertFile := flag.String("alert-file", "", "run a single investigation from a JSON alert file (CLI mode)")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	llmClient := llm.NewClient(cfg.LLM.APIKey, cfg.LLM.Model)
	mcpClient := mcp.NewClient(cfg.MCPServers, logger)
	ag := agent.New(llmClient, mcpClient, cfg, logger)

	if *alertFile != "" {
		runCLI(ag, *alertFile, cfg, logger)
		return
	}

	runServer(ag, cfg, logger)
}

func runCLI(ag *agent.Agent, alertFile string, cfg *config.Config, logger *slog.Logger) {
	data, err := os.ReadFile(alertFile)
	if err != nil {
		logger.Error("read alert file", "err", err)
		os.Exit(1)
	}

	var a report.Alert
	if err := json.Unmarshal(data, &a); err != nil {
		logger.Error("parse alert file", "err", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Investigation.Timeout)
	defer cancel()

	logger.Info("CLI mode: investigating alert", "alert", a.Name)

	rep, err := ag.Investigate(ctx, a)
	if err != nil {
		logger.Error("investigation failed", "err", err)
		os.Exit(1)
	}

	if auditErr := agent.WriteAuditLog(a, rep); auditErr != nil {
		logger.Warn("audit log write failed", "err", auditErr)
	}

	out, _ := rep.ToJSON()
	fmt.Println(string(out))
	fmt.Println()
	fmt.Println(rep.ToMarkdown())
}

func runServer(ag *agent.Agent, cfg *config.Config, logger *slog.Logger) {
	dedup := webhook.NewDeduplicator(cfg.Webhook.DedupTTL)
	handler := webhook.NewHandler(ag, dedup, logger, cfg.Investigation.Timeout, cfg.Webhook.AllowedSeverities)

	mux := http.NewServeMux()
	mux.Handle("/webhook/alert", handler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"ok"}`)
	})

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Webhook.Port),
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	logger.Info("rca-agent starting", "port", cfg.Webhook.Port, "model", cfg.LLM.Model)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		logger.Info("shutting down")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		srv.Shutdown(ctx) //nolint:errcheck
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("server failed", "err", err)
		os.Exit(1)
	}
}

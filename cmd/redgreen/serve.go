package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/dsswift/orrery/sdk"
	"github.com/dsswift/redgreen/internal/config"
	"github.com/dsswift/redgreen/internal/describe"
	"github.com/dsswift/redgreen/internal/outcomes"
	"github.com/dsswift/redgreen/internal/provider"
	"github.com/dsswift/redgreen/internal/provider/azuredevops"
	"github.com/dsswift/redgreen/internal/provider/github"
	"github.com/dsswift/redgreen/internal/provider/gitlab"
	"github.com/dsswift/redgreen/internal/services"
	"github.com/dsswift/redgreen/internal/state"
	rgsync "github.com/dsswift/redgreen/internal/sync"
	"github.com/dsswift/redgreen/internal/telemetry"
)

// webhookLimit is the largest webhook body read.
const webhookLimit = 25 << 20

// stateFile is the state's file name in DATA_DIR.
const stateFile = "state.sqlite"

func serve() error {
	base := telemetry.NewLogger(os.Stdout)
	log := base.With("component", "redgreen")
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	tel, err := telemetry.Setup(ctx, cfg.OtelEndpoint, cfg.OtelService, version, log)
	if err != nil {
		return err
	}
	store, err := state.Open(filepath.Join(cfg.DataDir, stateFile))
	if err != nil {
		return err
	}
	defer store.Close()
	client := &http.Client{Timeout: 60 * time.Second, Transport: telemetry.Transport(nil)}

	gh, err := github.New(github.Config{AppID: cfg.GitHubAppID, PrivateKey: cfg.GitHubAppPrivateKey, WebhookSecret: cfg.GitHubWebhookSecret, Allowed: cfg.GitHubAllowed, Token: cfg.GitHubToken, TokenAccounts: cfg.GitHubTokenAccounts}, store, client, base.With("component", "github"))
	if err != nil {
		return err
	}
	gl, err := gitlab.New(gitlab.Config{URL: cfg.GitLabURL, Allowed: cfg.GitLabAllowed, ClientID: cfg.GitLabClientID, ClientSecret: cfg.GitLabClientSecret, RefreshToken: cfg.GitLabRefreshToken, WebhookSecret: cfg.GitLabWebhookSecret, Token: cfg.GitLabToken, TokenAccounts: cfg.GitLabTokenAccounts, SecretKey: cfg.SecretKey}, store, client, base.With("component", "gitlab"))
	if err != nil {
		return err
	}
	ado := azuredevops.New(azuredevops.Config{TenantID: cfg.AzureTenantID, ClientID: cfg.AzureClientID, ClientSecret: cfg.AzureClientSecret, Organizations: cfg.AzureOrganizations}, store, client, base.With("component", "azuredevops"))
	providers := []provider.Provider{gh, gl, ado}
	tel.WatchQuotas(func() []telemetry.Quota {
		var out []telemetry.Quota
		for _, p := range providers {
			out = append(out, p.Quotas()...)
		}
		return out
	})

	hub := func(token, component string) *sdk.Client {
		if cfg.OrreryURL == "" || token == "" {
			return nil
		}
		return sdk.New(cfg.OrreryURL, token, sdk.WithHTTPClient(client), sdk.WithLogger(base.With("component", component)))
	}
	push := hub(cfg.OrreryToken, "orrery")
	if push == nil {
		log.Warn("orrery push off: ORRERY_URL and ORRERY_TOKEN are not both set")
	}
	var reader *outcomes.Reader
	if c := hub(cfg.OrreryReadToken, "orrery-read"); c != nil {
		reader = &outcomes.Reader{Client: c}
	} else {
		log.Info("orrery read off: ORRERY_URL and ORRERY_READ_TOKEN are not both set; Argo CD syncs and incidents are not read")
	}
	if c := hub(cfg.OrreryPushToken, "orrery-describe"); c != nil {
		desc, err := describe.Load("orrery.yaml")
		if err != nil {
			return err
		}
		go describe.Describer{Client: c, Desc: desc, Tel: tel, Log: base.With("component", "orrery-describe")}.Run(ctx)
	} else {
		log.Info("orrery describe off: ORRERY_URL and ORRERY_PUSH_TOKEN are not both set")
	}

	gitlabHost := ""
	if u, err := url.Parse(cfg.GitLabURL); err == nil {
		gitlabHost = u.Hostname()
	}
	engine := rgsync.New(rgsync.Options{Providers: providers, Store: store, Push: push, Read: reader, Tel: tel, Log: base.With("component", "sync"),
		Bots: services.Bots{Extra: cfg.BotAuthors}, Interval: cfg.SyncInterval, Concurrency: cfg.Concurrency, GitLabHost: gitlabHost})
	go engine.Run(ctx)

	mux := http.NewServeMux()
	mux.Handle("GET /healthz", tel.Middleware("/healthz", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "version": version})
	})))
	for _, p := range []provider.Provider{gh, gl} {
		hooks, ok := p.(provider.Webhooks)
		if !ok {
			continue
		}
		kind := p.Kind()
		route := "/api/webhooks/" + string(kind)
		mux.Handle("POST "+route, tel.Middleware(route, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(io.LimitReader(r.Body, webhookLimit))
			if err != nil {
				http.Error(w, "unreadable", http.StatusBadRequest)
				return
			}
			d, err := hooks.Webhook(r.Context(), r.Header, body)
			if err != nil {
				tel.Webhook(r.Context(), string(kind), "rejected")
				log.WarnContext(r.Context(), "webhook rejected", "provider", kind, "error", err)
				http.Error(w, "rejected", http.StatusBadRequest)
				return
			}
			engine.Webhook(context.WithoutCancel(r.Context()), kind, d)
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte("OK"))
		})))
	}
	server := &http.Server{Addr: ":" + strconv.Itoa(cfg.Port), Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		log.Info("shutting down")
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shut)
		_ = tel.Shutdown(shut)
	}()
	log.Info("listening", "port", cfg.Port, "version", version, "dataDir", cfg.DataDir, "publicUrl", cfg.PublicURL)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

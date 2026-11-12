package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"cotd-results/internal/app"
	"cotd-results/internal/config"
)

func main() {
	var (
		configPath  = flag.String("config", "config.toml", "path to config.toml")
		once        = flag.Bool("once", false, "run a single poll cycle, then exit")
		dryRun      = flag.Bool("dry-run", false, "print Discord messages instead of sending them")
		testWebhook = flag.Bool("test-webhook", false, "post a fake cup to the webhook, simulate the final results after 5s, then exit")
		debug       = flag.Bool("debug", false, "enable debug logging")
	)
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	var (
		cfg *config.Config
		err error
	)
	if *testWebhook {
		cfg, err = config.LoadConfigForTest(*configPath)
	} else {
		cfg, err = config.LoadConfig(*configPath)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	tracker, err := app.NewApp(cfg, *dryRun)
	if err != nil {
		fmt.Fprintf(os.Stderr, "initialise: %v\n", err)
		os.Exit(1)
	}

	if *testWebhook {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		slog.Info("sending test webhook messages", "delay", app.TestWebhookDelay.String(), "dry_run", *dryRun)
		if err := tracker.RunTest(ctx); err != nil {
			slog.Error("test failed", "err", err)
			os.Exit(1)
		}
		return
	}

	slog.Info("starting cotd-results",
		"cups", cfg.Cups.Type,
		"partitions", cfg.Cups.Partitions,
		"editions", cfg.Cups.Editions,
		"clubs", cfg.Clubs.IDs,
		"poll", fmt.Sprintf("%ds", cfg.Settings.PollIntervalSeconds),
		"dry_run", *dryRun,
	)
	if !tracker.HasOAuth() {
		slog.Warn("oauth.client_id/client_secret not set; player names will fall back to account ID prefixes")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *once {
		if err := tracker.RunOnce(ctx); err != nil {
			slog.Error("run failed", "err", err)
			os.Exit(1)
		}
		return
	}

	if err := tracker.Run(ctx); err != nil {
		slog.Error("run failed", "err", err)
		os.Exit(1)
	}
}

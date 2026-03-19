package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	maxbot "github.com/max-messenger/max-bot-api-client-go"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		fmt.Fprintf(os.Stderr, "Environment variable %s is not set\n", key)
		os.Exit(1)
	}
	return v
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// parseAdminIDs разбирает строку вида "123456789,987654321" в []int64.
func parseAdminIDs(s string) []int64 {
	var ids []int64
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ADMIN_IDS: invalid ID %q, skipping\n", part)
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

// parseFileSizeMB парсит строку в int (МБ), fallback — значение по умолчанию.
func parseFileSizeMB(s string, defaultMB int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return defaultMB
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < 0 {
		fmt.Fprintf(os.Stderr, "Invalid file size value %q, using default %d MB\n", s, defaultMB)
		return defaultMB
	}
	return v
}

func genKey() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	cfg := Config{
		MaxToken:         mustEnv("MAX_TOKEN"),
		TgBotURL:         envOr("TG_BOT_URL", "https://t.me/MaxTelegramBridgeBot"),
		MaxBotURL:        envOr("MAX_BOT_URL", "https://max.ru/id710708943262_bot"),
		WebhookURL:       os.Getenv("WEBHOOK_URL"),
		WebhookPort:      envOr("WEBHOOK_PORT", "8443"),
		AdminIDs:         parseAdminIDs(os.Getenv("ADMIN_IDS")),
		TgMaxFileSizeMB:  parseFileSizeMB(os.Getenv("TG_MAX_FILE_SIZE_MB"), 0),
		MaxMaxFileSizeMB: parseFileSizeMB(os.Getenv("MAX_MAX_FILE_SIZE_MB"), 0),
	}

	if len(cfg.AdminIDs) > 0 {
		slog.Info("Access restricted", "admin_ids", cfg.AdminIDs)
	} else {
		slog.Warn("ADMIN_IDS is not set - bot is accessible to everyone")
	}

	tgToken := mustEnv("TG_TOKEN")
	dbPath := envOr("DB_PATH", "bridge.db")

	var repo Repository
	var err error
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		repo, err = NewPostgresRepo(dsn)
		if err != nil {
			slog.Error("PostgreSQL error", "err", err)
			os.Exit(1)
		}
		slog.Info("DB: PostgreSQL")
	} else {
		repo, err = NewSQLiteRepo(dbPath)
		if err != nil {
			slog.Error("SQLite error", "err", err)
			os.Exit(1)
		}
		slog.Info("DB: SQLite", "path", dbPath)
	}
	defer repo.Close()

	tgBot, err := tgbotapi.NewBotAPI(tgToken)
	if err != nil {
		slog.Error("TG bot error", "err", err)
		os.Exit(1)
	}
	slog.Info("Telegram bot started", "username", tgBot.Self.UserName)

	maxApi, err := maxbot.New(cfg.MaxToken)
	if err != nil {
		slog.Error("MAX bot error", "err", err)
		os.Exit(1)
	}
	maxInfo, err := maxApi.Bots.GetBot(context.Background())
	if err != nil {
		slog.Error("MAX bot info error", "err", err)
		os.Exit(1)
	}
	slog.Info("MAX bot started", "name", maxInfo.Name)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		slog.Info("Shutting down...")
		cancel()
	}()

	bridge := NewBridge(cfg, repo, tgBot, maxApi)
	bridge.Run(ctx)
	slog.Info("Bridge stopped")
}
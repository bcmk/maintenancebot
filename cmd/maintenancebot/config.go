package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

type endpoint struct {
	ListenPath          string `json:"listen_path"`          // the path excluding domain to listen to, the good choice is "/your-telegram-bot-token"
	WebhookDomain       string `json:"webhook_domain"`       // the domain listening to the webhook
	BotToken            string `json:"bot_token"`            // your Telegram bot token
	MaintenanceResponse string `json:"maintenance_response"` // the maintenance response
}

type config struct {
	AdminID                int64               `json:"admin_id"`                 // admin Telegram ID
	AdminEndpoint          string              `json:"admin_endpoint"`           // admin endpoint
	ListenAddress          string              `json:"listen_address"`           // the address to listen to
	Endpoints              map[string]endpoint `json:"endpoints"`                // the endpoints by simple name, used for the support of the bots in different languages accessing the same database
	TelegramTimeoutSeconds int                 `json:"telegram_timeout_seconds"` // the timeout for Telegram queries
}

func readConfig(path string) *config {
	file, err := os.Open(filepath.Clean(path))
	checkErr(err)
	defer func() { checkErr(file.Close()) }()
	return parseConfig(file)
}

func parseConfig(r io.Reader) *config {
	decoder := json.NewDecoder(r)
	cfg := &config{}
	err := decoder.Decode(cfg)
	checkErr(err)
	checkErr(checkConfig(cfg))
	return cfg
}

func checkConfig(cfg *config) error {
	for _, x := range cfg.Endpoints {
		if x.ListenPath == "" {
			return errors.New("configure listen_path")
		}
		if x.WebhookDomain == "" {
			return errors.New("configure webhook_domain")
		}
		if x.BotToken == "" {
			return errors.New("configure bot_token")
		}
		if x.MaintenanceResponse == "" {
			return errors.New("configure maintenance_response")
		}
	}
	if cfg.ListenAddress == "" {
		return errors.New("configure listen_address")
	}
	if _, found := cfg.Endpoints[cfg.AdminEndpoint]; !found {
		return errors.New("configure admin_endpoint")
	}
	if cfg.TelegramTimeoutSeconds == 0 {
		return errors.New("configure telegram_timeout_seconds")
	}

	return nil
}

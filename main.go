// Command pdp-emulator serves a Permit.io PDP stand-in whose decisions tests control. See README.md.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/kharabasz/pdp-emulator/emulator"
)

func main() {
	port := env("PDP_EMULATOR_PORT", env("PORT", "7000"))

	// "healthcheck" lets a container image with no shell or curl check itself (Docker HEALTHCHECK).
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		c := http.Client{Timeout: 2 * time.Second}
		resp, err := c.Get("http://127.0.0.1:" + port + "/healthy")
		if err != nil || resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		return
	}

	logger := log.New(os.Stdout, "pdp-emulator ", log.LstdFlags|log.LUTC)
	cfg := emulator.Config{
		DefaultAllow: boolEnv("PDP_EMULATOR_DEFAULT_ALLOW", true),
		APIKey:       os.Getenv("PDP_API_KEY"),
		RequireKey:   boolEnv("PDP_EMULATOR_REQUIRE_API_KEY", false),
		Logger:       logger,
	}
	if f := os.Getenv("PDP_EMULATOR_RULES_FILE"); f != "" {
		rules, err := loadRules(f)
		if err != nil {
			logger.Fatalf("rules file: %v", err)
		}
		cfg.Rules = rules
	}
	if cfg.RequireKey && cfg.APIKey == "" {
		logger.Fatal("PDP_EMULATOR_REQUIRE_API_KEY is set but PDP_API_KEY is empty")
	}

	srv := &http.Server{Addr: ":" + port, Handler: emulator.New(cfg), ReadHeaderTimeout: 10 * time.Second}
	logger.Printf("listening on :%s (default allow=%t, %d start-up rules)", port, cfg.DefaultAllow, len(cfg.Rules))
	logger.Fatal(srv.ListenAndServe())
}

func loadRules(path string) ([]emulator.Rule, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rules []emulator.Rule
	if err := json.Unmarshal(b, &rules); err != nil {
		return nil, fmt.Errorf("%s must hold a JSON list of rules: %w", path, err)
	}
	return rules, nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func boolEnv(k string, def bool) bool {
	v, err := strconv.ParseBool(os.Getenv(k))
	if err != nil {
		return def
	}
	return v
}

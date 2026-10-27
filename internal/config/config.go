// Package config loads and validates runtime settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// Config holds every runtime setting.
type Config struct {
	Port             int
	Host             string
	DatabasePath     string
	LogLevel         slog.Level
	SeedDemo         bool
	OperatorUser     string
	OperatorPassword string // empty disables operator authentication
	TrustProxy       bool
	SSEHeartbeat     time.Duration
	SSEMaxClients    int
	TrackRatePerMin  int
	ShutdownTimeout  time.Duration
}

// Addr is the listen address.
func (c Config) Addr() string { return c.Host + ":" + strconv.Itoa(c.Port) }

// Load reads settings through getenv (os.Getenv in production) and validates them.
func Load(getenv func(string) string) (Config, error) {
	c := Config{}
	var errs []error
	get := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}
	intVar := func(key string, def, lo, hi int) int {
		raw := get(key, strconv.Itoa(def))
		v, err := strconv.Atoi(raw)
		if err != nil || v < lo || v > hi {
			errs = append(errs, fmt.Errorf("%s: want integer in [%d, %d], got %q", key, lo, hi, raw))
		}
		return v
	}
	boolVar := func(key string, def bool) bool {
		raw := get(key, strconv.FormatBool(def))
		v, err := strconv.ParseBool(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: want boolean, got %q", key, raw))
		}
		return v
	}
	durVar := func(key string, def, lo, hi time.Duration) time.Duration {
		raw := get(key, def.String())
		v, err := time.ParseDuration(raw)
		if err != nil || v < lo || v > hi {
			errs = append(errs, fmt.Errorf("%s: want duration in [%s, %s], got %q", key, lo, hi, raw))
		}
		return v
	}

	c.Port = intVar("PORT", 3003, 1, 65535)
	c.Host = get("HOST", "")
	c.DatabasePath = get("DATABASE_PATH", "data/orders.sqlite")
	if err := c.LogLevel.UnmarshalText([]byte(get("LOG_LEVEL", "info"))); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL: %w", err))
	}
	c.SeedDemo = boolVar("SEED_DEMO", true)
	c.OperatorUser = get("OPERATOR_USER", "operacao")
	c.OperatorPassword = getenv("OPERATOR_PASSWORD")
	if c.OperatorPassword != "" && len(c.OperatorPassword) < 12 {
		errs = append(errs, errors.New("OPERATOR_PASSWORD: use at least 12 characters"))
	}
	c.TrustProxy = boolVar("TRUST_PROXY", false)
	c.SSEHeartbeat = durVar("SSE_HEARTBEAT", 20*time.Second, time.Second, 5*time.Minute)
	c.SSEMaxClients = intVar("SSE_MAX_CLIENTS", 500, 1, 100_000)
	c.TrackRatePerMin = intVar("TRACK_RATE_LIMIT", 60, 0, 100_000)
	c.ShutdownTimeout = durVar("SHUTDOWN_TIMEOUT", 10*time.Second, time.Second, 5*time.Minute)

	if err := errors.Join(errs...); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return c, nil
}

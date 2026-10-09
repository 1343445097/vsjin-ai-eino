// Package appconfig owns standalone application configuration, without business-system dependencies.
package appconfig

import (
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	RuntimeOnly                                           bool
	Host, Port, APIToken, DataDir, SkillsDir, MessageKind string
	SkillGroups                                           []string
	MaxIterations                                         int
	TurnTimeout                                           time.Duration
}

func Load(env func(string) string) (Config, error) {
	value := func(key, fallback string) string {
		if v := strings.TrimSpace(env(key)); v != "" {
			return v
		}
		return fallback
	}
	c := Config{Host: value("VSJIN_AI_HOST", "127.0.0.1"), Port: value("VSJIN_AI_PORT", "18900"), APIToken: strings.TrimSpace(env("VSJIN_AI_API_TOKEN")), DataDir: value("VSJIN_AI_DATA_DIR", "./data"), SkillsDir: strings.TrimSpace(env("VSJIN_SKILLS_DIR")), MessageKind: value("VSJIN_MESSAGE_KIND", "message")}
	c.RuntimeOnly = env("VSJIN_AI_RUNTIME_ONLY") == "true"
	if c.RuntimeOnly && c.APIToken == "" {
		return c, fmt.Errorf("VSJIN_AI_API_TOKEN is required for runtime-only mode")
	}
	p, err := strconv.Atoi(c.Port)
	if err != nil || p < 1 || p > 65535 {
		return c, fmt.Errorf("VSJIN_AI_PORT must be between 1 and 65535")
	}
	ip := net.ParseIP(c.Host)
	if c.Host != "localhost" && (ip == nil || !ip.IsLoopback()) && c.APIToken == "" {
		return c, fmt.Errorf("VSJIN_AI_API_TOKEN is required for a non-loopback listener")
	}
	if c.MessageKind != "message" && c.MessageKind != "agentic" {
		return c, fmt.Errorf("VSJIN_MESSAGE_KIND must be message or agentic")
	}
	n, err := strconv.Atoi(value("VSJIN_AI_MAX_ITERATIONS", "12"))
	if err != nil || n < 1 || n > 50 {
		return c, fmt.Errorf("VSJIN_AI_MAX_ITERATIONS must be between 1 and 50")
	}
	c.MaxIterations = n
	seconds, err := strconv.Atoi(value("VSJIN_AI_TURN_TIMEOUT_SECONDS", "180"))
	if err != nil || seconds < 1 || seconds > 1800 {
		return c, fmt.Errorf("VSJIN_AI_TURN_TIMEOUT_SECONDS must be between 1 and 1800")
	}
	c.TurnTimeout = time.Duration(seconds) * time.Second
	seen := map[string]bool{}
	for _, g := range strings.Split(value("VSJIN_SKILL_GROUPS", "knowledge.base,knowledge.cad,scai.reporting"), ",") {
		g = strings.TrimSpace(g)
		if g != "" && !seen[g] {
			c.SkillGroups = append(c.SkillGroups, g)
			seen[g] = true
		}
	}
	return c, nil
}

func (c Config) SessionDir() string { return filepath.Join(c.DataDir, "sessions_"+c.MessageKind) }

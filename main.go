// Copyright 2026 CloudWeGo Authors. Licensed under Apache-2.0; see LICENSE.
// VSJin AI standalone lifecycle and configuration: Copyright 2026 VSJin.
package main

import (
	"context"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"github.com/vsjin/vsjin-ai/internal/appconfig"
	"github.com/vsjin/vsjin-ai/internal/checkpoint"
	"github.com/vsjin/vsjin-ai/mem"
	"github.com/vsjin/vsjin-ai/policy"
	"github.com/vsjin/vsjin-ai/server"
	"github.com/vsjin/vsjin-ai/skills"
	"log"
	"os"
)

func main() {
	cfg, err := appconfig.Load(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	if cfg.MessageKind == "agentic" {
		runTyped[*schema.AgenticMessage](cfg)
	} else {
		runTyped[*schema.Message](cfg)
	}
}

func runTyped[M adk.MessageType](cfg appconfig.Config) {
	ctx := context.Background()
	root := cfg.SkillsDir
	if root == "" {
		dir, cleanup, err := policy.Materialize()
		if err != nil {
			log.Fatal("内置技能不可用")
		}
		defer cleanup()
		root = dir
	}
	catalog, err := skills.Load(root, cfg.SkillGroups)
	if err != nil {
		log.Fatalf("技能目录加载失败: %v", err)
	}
	var agent adk.TypedResumableAgent[M]
	if !cfg.RuntimeOnly {
		agent, err = buildAgentTyped[M](ctx, cfg, catalog)
		if err != nil {
			log.Fatalf("模型配置不可用: %v", err)
		}
	}
	store, err := mem.NewStore[M](cfg.SessionDir())
	if err != nil {
		log.Fatal("会话存储不可用")
	}
	summaries := make([]server.SkillSummary, 0, len(catalog.Skills))
	for _, s := range catalog.Skills {
		summaries = append(summaries, server.SkillSummary{Name: s.Name, Description: s.Description})
	}
	srv := server.New(server.Config[M]{Agent: agent, CheckPointStore: checkpoint.NewInMemoryStore(), Store: store, Host: cfg.Host, Port: cfg.Port, APIToken: cfg.APIToken, TurnTimeout: cfg.TurnTimeout, Skills: summaries, Configured: !cfg.RuntimeOnly})
	log.Printf("VSJin AI · EinoLoop listening at http://%s:%s (%d authorized skills)", cfg.Host, cfg.Port, len(summaries))
	srv.Spin()
}

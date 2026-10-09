/*
 * Copyright 2026 CloudWeGo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package chatmodel

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/cloudwego/eino-ext/components/model/agenticark"
	"github.com/cloudwego/eino-ext/components/model/agenticopenai"
	"github.com/cloudwego/eino-ext/components/model/ark"
	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	openairesponses "github.com/openai/openai-go/v3/responses"
	arkModel "github.com/volcengine/volcengine-go-sdk/service/arkruntime/model"
)

// NewModel creates a model matching M. The caller chooses the concrete message
// type at the boundary; MODEL_TYPE still selects the provider (OpenAI by
// default, Ark when MODEL_TYPE=ark).
func NewModel[M adk.MessageType](ctx context.Context) (einomodel.BaseModel[M], error) {
	if err := validateConfig(); err != nil {
		return nil, err
	}
	var zero M
	switch any(zero).(type) {
	case *schema.AgenticMessage:
		cm, err := newAgenticModel(ctx)
		if err != nil {
			return nil, err
		}
		return any(cm).(einomodel.BaseModel[M]), nil
	default:
		cm, err := newChatModel(ctx)
		if err != nil {
			return nil, err
		}
		return any(cm).(einomodel.BaseModel[M]), nil
	}
}

func newChatModel(ctx context.Context) (einomodel.ToolCallingChatModel, error) {
	modelType := strings.ToLower(firstEnv("VSJIN_MODEL_PROVIDER", "MODEL_TYPE"))
	if modelType == "ark" {
		return ark.NewChatModel(ctx, &ark.ChatModelConfig{
			APIKey:  firstEnv("VSJIN_MODEL_API_KEY", "ARK_API_KEY"),
			Model:   firstEnv("VSJIN_MODEL_NAME", "ARK_MODEL", "ARK_MODEL_ID"),
			BaseURL: firstEnv("VSJIN_MODEL_BASE_URL", "ARK_BASE_URL"),
			Thinking: &arkModel.Thinking{
				Type: arkModel.ThinkingTypeDisabled,
			},
		})
	}

	return openai.NewChatModel(ctx, &openai.ChatModelConfig{
		Timeout: 150 * time.Second,
		APIKey:  firstEnv("VSJIN_MODEL_API_KEY", "OPENAI_API_KEY"),
		Model:   firstEnv("VSJIN_MODEL_NAME", "OPENAI_MODEL", "OPENAI_MODEL_ID"),
		BaseURL: firstEnv("VSJIN_MODEL_BASE_URL", "OPENAI_BASE_URL"),
		ByAzure: os.Getenv("OPENAI_BY_AZURE") == "true",
	})
}

func newAgenticModel(ctx context.Context) (einomodel.AgenticModel, error) {
	modelType := strings.ToLower(firstEnv("VSJIN_MODEL_PROVIDER", "MODEL_TYPE"))
	timeout := 3 * time.Minute
	if modelType == "ark" {
		return agenticark.New(ctx, &agenticark.Config{
			APIKey:  firstEnv("VSJIN_MODEL_API_KEY", "ARK_API_KEY"),
			Model:   firstEnv("VSJIN_MODEL_NAME", "ARK_MODEL", "ARK_MODEL_ID"),
			BaseURL: firstEnv("VSJIN_MODEL_BASE_URL", "ARK_BASE_URL"),
			Timeout: &timeout,
		})
	}

	return agenticopenai.New(ctx, &agenticopenai.Config{
		APIKey:  firstEnv("VSJIN_MODEL_API_KEY", "OPENAI_API_KEY"),
		Model:   firstEnv("VSJIN_MODEL_NAME", "OPENAI_MODEL", "OPENAI_MODEL_ID"),
		BaseURL: firstEnv("VSJIN_MODEL_BASE_URL", "OPENAI_BASE_URL"),
		ByAzure: os.Getenv("OPENAI_BY_AZURE") == "true",
		Timeout: &timeout,
		Include: []openairesponses.ResponseIncludable{
			openairesponses.ResponseIncludableReasoningEncryptedContent,
		},
	})
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

func validateConfig() error {
	provider := strings.ToLower(firstEnv("VSJIN_MODEL_PROVIDER", "MODEL_TYPE"))
	if provider != "" && provider != "openai" && provider != "ark" {
		return fmt.Errorf("VSJIN_MODEL_PROVIDER must be openai or ark")
	}
	names, keys, bases := []string{"VSJIN_MODEL_NAME", "OPENAI_MODEL", "OPENAI_MODEL_ID"}, []string{"VSJIN_MODEL_API_KEY", "OPENAI_API_KEY"}, []string{"VSJIN_MODEL_BASE_URL", "OPENAI_BASE_URL"}
	if provider == "ark" {
		names = []string{"VSJIN_MODEL_NAME", "ARK_MODEL", "ARK_MODEL_ID"}
		keys = []string{"VSJIN_MODEL_API_KEY", "ARK_API_KEY"}
		bases = []string{"VSJIN_MODEL_BASE_URL", "ARK_BASE_URL"}
	}
	if firstEnv(names...) == "" {
		return fmt.Errorf("请配置 VSJIN_MODEL_NAME")
	}
	base := firstEnv(bases...)
	var local bool
	if base != "" {
		u, err := url.Parse(base)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("VSJIN_MODEL_BASE_URL must be an HTTP(S) base URL without credentials")
		}
		ip := net.ParseIP(u.Hostname())
		local = u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
	}
	if firstEnv(keys...) == "" && !local {
		return fmt.Errorf("请配置 VSJIN_MODEL_API_KEY")
	}
	return nil
}

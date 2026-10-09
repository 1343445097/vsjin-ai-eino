package chatmodel

import (
	"context"
	"github.com/cloudwego/eino/schema"
	"testing"
)

func TestStandaloneModelConfigurationValidation(t *testing.T) {
	for _, k := range []string{"VSJIN_MODEL_NAME", "VSJIN_MODEL_API_KEY", "VSJIN_MODEL_BASE_URL", "VSJIN_MODEL_PROVIDER", "OPENAI_MODEL", "OPENAI_MODEL_ID", "OPENAI_API_KEY", "OPENAI_BASE_URL", "MODEL_TYPE"} {
		t.Setenv(k, "")
	}
	if _, err := NewModel[*schema.Message](context.Background()); err == nil {
		t.Fatal("missing model credentials must fail before serving")
	}
	t.Setenv("VSJIN_MODEL_NAME", "test-model")
	t.Setenv("VSJIN_MODEL_API_KEY", "test-only-key")
	t.Setenv("VSJIN_MODEL_BASE_URL", "http://127.0.0.1:9999/v1")
	if _, err := NewModel[*schema.Message](context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VSJIN_MODEL_PROVIDER", "unsupported")
	if _, err := NewModel[*schema.Message](context.Background()); err == nil {
		t.Fatal("unsupported provider must not silently use OpenAI")
	}
}

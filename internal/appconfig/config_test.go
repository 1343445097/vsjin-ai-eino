package appconfig

import (
	"testing"
	"time"
)

func TestLocalDefaultsAndExplicitSkillGroups(t *testing.T) {
	c, err := Load(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if c.Host != "127.0.0.1" || c.Port != "18900" || c.MessageKind != "message" || c.MaxIterations != 12 || c.TurnTimeout != 180*time.Second {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if len(c.SkillGroups) != 3 || c.SkillGroups[0] != "knowledge.base" || c.SkillGroups[1] != "knowledge.cad" || c.SkillGroups[2] != "scai.reporting" {
		t.Fatalf("groups: %v", c.SkillGroups)
	}
}

func TestInvalidNetworkAndBudgetsFailBeforeStartup(t *testing.T) {
	cases := []map[string]string{
		{"VSJIN_AI_HOST": "0.0.0.0"},
		{"VSJIN_AI_PORT": "99999"},
		{"VSJIN_AI_MAX_ITERATIONS": "0"},
		{"VSJIN_AI_TURN_TIMEOUT_SECONDS": "-1"},
		{"VSJIN_MESSAGE_KIND": "invalid"},
	}
	for _, values := range cases {
		_, err := Load(func(k string) string { return values[k] })
		if err == nil {
			t.Errorf("accepted invalid config %v", values)
		}
	}
}

func TestAuthenticatedListenerAndCustomPaths(t *testing.T) {
	values := map[string]string{"VSJIN_AI_HOST": "0.0.0.0", "VSJIN_AI_API_TOKEN": "local-access-token", "VSJIN_AI_DATA_DIR": "/tmp/vsjin-test", "VSJIN_SKILLS_DIR": "/tmp/vsjin-skills", "VSJIN_SKILL_GROUPS": "knowledge.base, scai.reporting, knowledge.base", "VSJIN_MESSAGE_KIND": "agentic"}
	c, err := Load(func(k string) string { return values[k] })
	if err != nil {
		t.Fatal(err)
	}
	if c.APIToken != values["VSJIN_AI_API_TOKEN"] || c.DataDir != values["VSJIN_AI_DATA_DIR"] || c.SkillsDir != values["VSJIN_SKILLS_DIR"] || len(c.SkillGroups) != 2 {
		t.Fatal("configuration not preserved")
	}
}

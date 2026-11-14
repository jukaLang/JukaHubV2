package main

import "testing"

func TestRemoveRetiredVaultElements(t *testing.T) {
	cfg := &Config{
		Scenes: []SceneConfig{{
			Elements: []Element{
				{Type: "button", Text: "Vault", Trigger: "vault_search"},
				{Type: "button", Text: "Search", Trigger: "search"},
			},
		}},
		Variables: Variables{Custom: map[string]interface{}{
			"vault_top":   []VideoInfo{},
			"weatherUnit": "C",
		}},
	}
	removeRetiredVaultElements(cfg)
	if got := cfg.Scenes[0].Elements; len(got) != 1 || got[0].Text != "Search" {
		t.Fatalf("retired Vault control was not removed: %#v", got)
	}
	if len(cfg.Variables.Custom) != 1 || cfg.Variables.Custom["weatherUnit"] != "C" {
		t.Fatalf("retired Vault variables were not removed: %#v", cfg.Variables.Custom)
	}
}

func TestDropRetiredVaultVars(t *testing.T) {
	user := &UserConfig{Variables: UserVariables{Custom: map[string]interface{}{
		"vault_search_query": "query",
		"vault_top":          []VideoInfo{},
		"vault_error":        "old error",
		"weatherUnit":        "C",
	}}}
	if !dropRetiredVaultVars(user) {
		t.Fatal("expected retired Vault variables to be dropped")
	}
	if len(user.Variables.Custom) != 1 || user.Variables.Custom["weatherUnit"] != "C" {
		t.Fatalf("unrelated custom variable was changed: %#v", user.Variables.Custom)
	}
}

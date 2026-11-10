package main

import "testing"

func TestModelChoicesPutTheSuggestedFirst(t *testing.T) {
	models := map[string]catalogModel{
		"glm-5.3":         {Name: "GLM 5.3"},
		"deepseek-v4-pro": {Name: "DeepSeek V4 Pro"},
		"beam-free":       {Name: "Beam", Cost: &price{}},
	}
	var got []string
	for _, c := range modelChoices(models, "glm-5.3") {
		got = append(got, c.value+"|"+c.about)
	}
	want := []string{"glm-5.3|GLM 5.3", "beam-free|Beam · free", "deepseek-v4-pro|DeepSeek V4 Pro"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestProviderChoicesNeedAnAddress(t *testing.T) {
	cat := map[string]catalogProvider{
		"zai":       {Name: "Z.AI", API: "https://api.z.ai", Models: map[string]catalogModel{"glm": {}}},
		"anthropic": {Name: "Anthropic", Models: map[string]catalogModel{"claude": {}}},
		"empty":     {Name: "Empty", API: "https://x"},
	}
	got := providerChoices(cat)
	if len(got) != 1 || got[0].value != "zai" {
		t.Fatalf("got %v", got)
	}
}

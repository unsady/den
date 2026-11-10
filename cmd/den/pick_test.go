package main

import "testing"

func TestMatchModels(t *testing.T) {
	models := []catalogModel{
		{ID: "deepseek-v4-pro", Name: "DeepSeek V4 Pro"},
		{ID: "deepseek-v4.1-flash", Name: "DeepSeek V4.1 Flash"},
		{ID: "glm-5.3", Name: "GLM 5.3"},
		{ID: "glm-5.3-flash", Name: "GLM 5.3 Flash"},
	}
	for _, c := range []struct {
		query string
		want  []string
	}{
		{"deep", []string{"deepseek-v4-pro", "deepseek-v4.1-flash"}},
		{"Deep FLASH", []string{"deepseek-v4.1-flash"}},
		{"glm-5.3", []string{"glm-5.3"}},
		{"", []string{"deepseek-v4-pro", "deepseek-v4.1-flash", "glm-5.3", "glm-5.3-flash"}},
		{"claude", nil},
	} {
		var got []string
		for _, m := range matchModels(models, c.query) {
			got = append(got, m.ID)
		}
		if len(got) != len(c.want) {
			t.Fatalf("%q: got %v, want %v", c.query, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("%q: got %v, want %v", c.query, got, c.want)
			}
		}
	}
}

package genres

import (
	"strings"
	"testing"

	"storysmith/internal/config"
)

func TestNovelPromptBlockIncludesParameters(t *testing.T) {
	s := config.StoryConfig{
		Genre:                 "Fantasy",
		Subgenre:              "High Fantasy",
		NovelLength:           "Novel (Standard)",
		Structure:             "6-Act Structure",
		Sections:              StructureSections["6-Act Structure"],
		Tone:                  "Epico",
		DarknessLevel:         3,
		SpeculativeDensity:    5,
		TargetWordsPerChapter: 4000,
		Title:                 "El Reino Roto",
	}
	block := NovelPromptBlock(s, "en")
	for _, want := range []string{"El Reino Roto", "Fantasy / High Fantasy", "6-Act Structure", "Darkness level: 3/5", "Target words per chapter: 4000"} {
		if !strings.Contains(block, want) {
			t.Errorf("block missing %q; got:\n%s", want, block)
		}
	}
	// Subgenre flavour from the catalogue must leak into the guidance.
	cfg, err := Config("Fantasy", "High Fantasy")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Tones) > 0 && !strings.Contains(block, "typical tones:") {
		t.Errorf("expected subgenre hints in block; got:\n%s", block)
	}
}

func TestNovelPromptBlockEmptyWhenNoParams(t *testing.T) {
	if b := NovelPromptBlock(config.StoryConfig{}, "en"); b != "" {
		t.Errorf("expected empty block for unset params, got %q", b)
	}
}

func TestNovelPromptBlockBilingual(t *testing.T) {
	s := config.StoryConfig{Genre: "Mystery", Title: "La Sombra"}
	zh := NovelPromptBlock(s, "zh")
	en := NovelPromptBlock(s, "en")
	if !strings.Contains(zh, "小说参数") || !strings.Contains(en, "Novel Parameters") {
		t.Errorf("language headers wrong: zh=%q en=%q", zh, en)
	}
}

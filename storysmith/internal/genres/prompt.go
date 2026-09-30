package genres

import (
	"fmt"
	"strings"

	"storysmith/internal/config"
	"storysmith/internal/i18n"
)

// NovelPromptBlock renders the project's validated Novel Parameters as a
// compact, language-aware prompt fragment. Outline and chapter-generation
// tasks inject it into the system message so local models (Ollama) keep
// honoring genre/structure/tuning choices across long sessions.
// Returns "" when no parameters have been set yet.
func NovelPromptBlock(story config.StoryConfig, lang string) string {
	if strings.TrimSpace(story.Genre) == "" && strings.TrimSpace(story.NovelLength) == "" &&
		strings.TrimSpace(story.Structure) == "" && strings.TrimSpace(story.Title) == "" {
		return ""
	}
	zh := i18n.NormalizeLanguage(lang) == i18n.LangZH

	var b strings.Builder
	if zh {
		b.WriteString("## 小说参数（必须遵守）\n")
	} else {
		b.WriteString("## Novel Parameters (must be honored)\n")
	}

	add := func(zhLabel, enLabel, val string) {
		val = strings.TrimSpace(val)
		if val == "" {
			return
		}
		if zh {
			fmt.Fprintf(&b, "- %s：%s\n", zhLabel, val)
		} else {
			fmt.Fprintf(&b, "- %s: %s\n", enLabel, val)
		}
	}

	add("书名", "Title", story.Title)
	if story.Genre != "" {
		g := story.Genre
		if story.Subgenre != "" {
			g += " / " + story.Subgenre
		}
		add("类型", "Genre/Subgenre", g)
	}
	add("篇幅", "Length", story.NovelLength)
	if story.Structure != "" {
		s := story.Structure
		if len(story.Sections) > 0 {
			s += " — beats: " + strings.Join(story.Sections, " → ")
		}
		add("结构", "Structure", s)
	}
	add("风格", "Writing style", story.WritingStyle)
	add("视角", "POV", story.WritingPOV)
	if story.TargetWordsPerChapter > 0 {
		if zh {
			fmt.Fprintf(&b, "- 每章目标字数：%d\n", story.TargetWordsPerChapter)
		} else {
			fmt.Fprintf(&b, "- Target words per chapter: %d\n", story.TargetWordsPerChapter)
		}
	}
	add("主角类型", "Protagonist type", story.ProtagonistType)
	add("冲突规模", "Conflict scale", story.ConflictScale)
	add("基调", "Tone", story.Tone)
	add("时代背景", "Timeframe", story.Timeframe)
	add("主要场景", "Locations", story.Locations)
	add("性别比例", "Gender ratio", story.GenderRatio)
	add("恋爱比重", "Romance level", story.RomanceLevel)
	if story.DarknessLevel > 0 {
		if zh {
			fmt.Fprintf(&b, "- 黑暗程度：%d/5\n", story.DarknessLevel)
		} else {
			fmt.Fprintf(&b, "- Darkness level: %d/5\n", story.DarknessLevel)
		}
	}
	add("内容尺度", "Sexual content guidance", story.SexualContent)
	if story.SpeculativeDensity > 0 {
		if zh {
			fmt.Fprintf(&b, "- 设定浓度：%d/5\n", story.SpeculativeDensity)
		} else {
			fmt.Fprintf(&b, "- Speculative density: %d/5\n", story.SpeculativeDensity)
		}
	}

	// Subgenre AI-generation hints from the catalogue (settings, protagonist
	// types, conflict scales, tones) guide the model without hard-coding them.
	if story.Genre != "" && story.Subgenre != "" {
		if cfg, err := Config(story.Genre, story.Subgenre); err == nil {
			hints := []string{}
			if h := cfg.SettingsHint(); h != "" {
				hints = append(hints, "implied settings: "+h)
			}
			if len(cfg.ConflictScales) > 0 {
				hints = append(hints, "typical conflict scales: "+strings.Join(cfg.ConflictScales, ", "))
			}
			if len(cfg.Tones) > 0 {
				hints = append(hints, "typical tones: "+strings.Join(cfg.Tones, ", "))
			}
			if len(hints) > 0 {
				j := strings.Join(hints, "; ")
				if zh {
					fmt.Fprintf(&b, "- 子类型参考：%s\n", j)
				} else {
					fmt.Fprintf(&b, "- Subgenre references: %s\n", j)
				}
			}
		}
	}

	return b.String()
}

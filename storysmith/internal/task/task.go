// Package task runs the first generation workflows:
//   - outline: one-shot novel synopsis + chapter list honoring Novel Parameters
//   - write:   chapter-by-chapter writing with SSE streaming
//
// Both inject genres.NovelPromptBlock into the system message so local models
// (Ollama) keep honoring genre/structure/tuning choices. State lives in plain
// files under the project dir (outline.json / chapters/chNN.txt).
package task

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"storysmith/internal/config"
	"storysmith/internal/fsutil"
	"storysmith/internal/genres"
	"storysmith/internal/i18n"
	"storysmith/internal/llm"
	"storysmith/internal/sse"
	"storysmith/internal/story"
)

type OutlineChapter struct {
	Index      int    `json:"index"`
	Title      string `json:"title"`
	Summary    string `json:"summary"`
	Section    string `json:"section,omitempty"` // beat of the chosen structure
	KeyPoints  string `json:"key_points,omitempty"`
	Characters string `json:"characters,omitempty"`
}

type Outline struct {
	Title       string           `json:"title"`
	Synopsis    string           `json:"synopsis"`
	Themes      string           `json:"themes,omitempty"`
	Confirmed   bool             `json:"confirmed"`
	GeneratedAt string           `json:"generated_at,omitempty"`
	Chapters    []OutlineChapter `json:"chapters"`
}

func outlinePath(dir string) string { return filepath.Join(dir, "outline.json") }
func chaptersDir(dir string) string { return filepath.Join(dir, "chapters") }
func chapterPath(dir string, n int) string {
	return filepath.Join(chaptersDir(dir), fmt.Sprintf("ch%02d.txt", n))
}

// LoadOutline returns (nil, nil) when no outline exists yet.
func LoadOutline(dir string) (*Outline, error) {
	data, err := fsutil.ReadFileIfExists(outlinePath(dir))
	if err != nil || data == nil {
		return nil, err
	}
	var o Outline
	if err := json.Unmarshal(data, &o); err != nil {
		return nil, fmt.Errorf("parse outline.json: %w", err)
	}
	return &o, nil
}

func SaveOutline(dir string, o *Outline) error {
	if err := fsutil.WriteFileAtomic(outlinePath(dir), mustJSON(o)); err != nil {
		return err
	}
	return nil
}

func mustJSON(v any) []byte {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(err)
	}
	return data
}

// Runner serializes generation tasks; only one may run at a time.
type Runner struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	name   string
}

func NewRunner() *Runner { return &Runner{} }

func (r *Runner) Running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cancel != nil
}

func (r *Runner) TaskName() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.name
}

func (r *Runner) start(name string) (context.Context, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		return nil, fmt.Errorf("task already running: %s", r.name)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.name = name
	return ctx, nil
}

func (r *Runner) finish() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		r.cancel()
	}
	r.cancel = nil
	r.name = ""
}

// Cancel aborts the running task; returns false if none is active.
func (r *Runner) Cancel() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel == nil {
		return false
	}
	r.cancel()
	return true
}

// ---------------------------------------------------------------------------
// Prompts
// ---------------------------------------------------------------------------

func baseSystem(cfg *config.Config, promptKey, lang string) string {
	sys := i18n.SystemPromptFor(lang, promptKey)
	if block := genres.NovelPromptBlock(cfg.Story, lang); block != "" {
		sys += "\n\n" + block
	}
	return sys
}

// ---------------------------------------------------------------------------
// Outline generation
// ---------------------------------------------------------------------------

type OutlineRequest struct {
	ChapterCount int    `json:"chapter_count"`
	Idea         string `json:"idea"` // optional premise to seed the outline
}

const outlineJSONShape = `{
  "title": "...",
  "synopsis": "...",
  "themes": "...",
  "chapters": [
    {"index": 1, "title": "...", "summary": "...", "section": "...", "key_points": "...", "characters": "..."}
  ]
}`

// GenerateOutline produces (or replaces) outline.json for the project.
func (r *Runner) GenerateOutline(apiCfg *config.APIConfig, cfg *config.Config, projDir string, req OutlineRequest, logger *sse.LogBroadcaster) error {
	ctx, err := r.start("outline")
	if err != nil {
		return err
	}
	defer r.finish()

	count := req.ChapterCount
	if count <= 0 {
		count = 12
	}
	if count > 36 {
		count = 36
	}
	lang := cfg.Language
	zh := i18n.NormalizeLanguage(lang) == i18n.LangZH

	var user strings.Builder
	if zh {
		fmt.Fprintf(&user, "请为一部小说生成大纲。要求：共 %d 章；输出严格的 JSON（不要任何解释或代码块围栏），格式：\n%s\n", count, outlineJSONShape)
		user.WriteString("\n必须遵守以下小说参数，并把所选叙事结构的各节拍(section)均匀分配到章节：\n")
	} else {
		fmt.Fprintf(&user, "Generate a novel outline. Requirements: exactly %d chapters; output strict JSON (no prose, no code fences) shaped:\n%s\n", count, outlineJSONShape)
		user.WriteString("\nHonor these novel parameters and distribute the chosen structure's beats across chapters (use the section field):\n")
	}
	block := genres.NovelPromptBlock(cfg.Story, lang)
	if block == "" {
		block = "- (none set)"
	}
	user.WriteString(block)
	if idea := strings.TrimSpace(req.Idea); idea != "" {
		if zh {
			fmt.Fprintf(&user, "\n故事创意/前提：%s\n", idea)
		} else {
			fmt.Fprintf(&user, "\nStory idea/premise: %s\n", idea)
		}
	}

	logger.TaskStart("outline")
	logger.InfoBilingual("正在生成小说大纲...", "Generating novel outline...")
	raw, callErr := llm.CallAPIMessages(ctx, apiCfg, []llm.Message{
		{Role: "system", Content: baseSystem(cfg, "outline_editor_json", lang)},
		{Role: "user", Content: user.String()},
	})
	if callErr != nil {
		logger.TaskEnd("outline", false)
		return callErr
	}
	jsonStr := llm.ExtractJSON(raw)
	if jsonStr == "" {
		logger.TaskEnd("outline", false)
		return fmt.Errorf("model did not return valid JSON")
	}
	var o Outline
	if err := json.Unmarshal([]byte(jsonStr), &o); err != nil {
		logger.TaskEnd("outline", false)
		return fmt.Errorf("parse outline JSON: %w", err)
	}
	if len(o.Chapters) == 0 {
		logger.TaskEnd("outline", false)
		return fmt.Errorf("outline has no chapters")
	}
	for i := range o.Chapters {
		o.Chapters[i].Index = i + 1
	}
	o.Confirmed = false // pending user acceptance
	o.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	if o.Title == "" {
		o.Title = cfg.Story.Title
	}
	if err := SaveOutline(projDir, &o); err != nil {
		logger.TaskEnd("outline", false)
		return err
	}
	updateProgress(projDir, "outline", o.Title, 0)
	logger.SuccessBilingual(
		fmt.Sprintf("大纲已生成：%d 章（待确认）", len(o.Chapters)),
		fmt.Sprintf("Outline generated: %d chapters (pending confirmation)", len(o.Chapters)))
	logger.TaskEnd("outline", true)
	return nil
}

// ConfirmOutline marks the current outline as accepted/rejected.
func ConfirmOutline(projDir string, confirm bool) (*Outline, error) {
	o, err := LoadOutline(projDir)
	if err != nil {
		return nil, err
	}
	if o == nil {
		return nil, fmt.Errorf("no outline generated yet")
	}
	o.Confirmed = confirm
	if err := SaveOutline(projDir, o); err != nil {
		return nil, err
	}
	phase := "outline"
	if confirm {
		phase = "writing"
	}
	updateProgress(projDir, phase, o.Title, 0)
	return o, nil
}

// ---------------------------------------------------------------------------
// Chapter writing (streamed)
// ---------------------------------------------------------------------------

// WriteChapter generates one chapter from the confirmed outline, streaming
// tokens over SSE. chapterIdx is 1-based.
func (r *Runner) WriteChapter(apiCfg *config.APIConfig, cfg *config.Config, projDir string, chapterIdx int, feedback string, logger *sse.LogBroadcaster) error {
	ctx, err := r.start("write")
	if err != nil {
		return err
	}
	defer r.finish()

	o, err := LoadOutline(projDir)
	if err != nil {
		return err
	}
	if o == nil || chapterIdx < 1 || chapterIdx > len(o.Chapters) {
		return fmt.Errorf("invalid chapter index %d", chapterIdx)
	}
	if !o.Confirmed {
		return fmt.Errorf("outline not confirmed")
	}
	ch := o.Chapters[chapterIdx-1]

	lang := cfg.Language
	zh := i18n.NormalizeLanguage(lang) == i18n.LangZH
	target := cfg.Story.TargetWordsPerChapter
	if target <= 0 {
		target = 5000
	}

	var user strings.Builder
	if zh {
		fmt.Fprintf(&user, "请写出第 %d 章的正文（目标约 %d 字）。\n", chapterIdx, target)
		fmt.Fprintf(&user, "章节标题：%s\n内容概要：%s\n", ch.Title, ch.Summary)
		if ch.Section != "" {
			fmt.Fprintf(&user, "对应结构节拍：%s\n", ch.Section)
		}
		if ch.KeyPoints != "" {
			fmt.Fprintf(&user, "要点：%s\n", ch.KeyPoints)
		}
		if ch.Characters != "" {
			fmt.Fprintf(&user, "出场人物：%s\n", ch.Characters)
		}
		if prev := readPrevTail(projDir, chapterIdx); prev != "" {
			fmt.Fprintf(&user, "\n上一章结尾（保持连贯，勿重复）：\n…%s\n", prev)
		}
		if fb := strings.TrimSpace(feedback); fb != "" {
			fmt.Fprintf(&user, "\n用户对上一稿的修改意见（重写时遵守）：%s\n", fb)
		}
		user.WriteString("\n直接输出章节正文，不要元说明。")
	} else {
		fmt.Fprintf(&user, "Write chapter %d (target ~%d words).\n", chapterIdx, target)
		fmt.Fprintf(&user, "Title: %s\nSummary: %s\n", ch.Title, ch.Summary)
		if ch.Section != "" {
			fmt.Fprintf(&user, "Structure beat: %s\n", ch.Section)
		}
		if ch.KeyPoints != "" {
			fmt.Fprintf(&user, "Key points: %s\n", ch.KeyPoints)
		}
		if ch.Characters != "" {
			fmt.Fprintf(&user, "Characters: %s\n", ch.Characters)
		}
		if prev := readPrevTail(projDir, chapterIdx); prev != "" {
			fmt.Fprintf(&user, "\nPrevious chapter ending (keep continuity, do not repeat):\n...%s\n", prev)
		}
		if fb := strings.TrimSpace(feedback); fb != "" {
			fmt.Fprintf(&user, "\nUser revision notes for the previous draft (follow them): %s\n", fb)
		}
		user.WriteString("\nOutput only the chapter prose.")
	}

	logger.TaskStart("write")
	logger.StreamStart(chapterIdx)
	logger.InfoBilingual(
		fmt.Sprintf("正在写作第 %d 章...", chapterIdx),
		fmt.Sprintf("Writing chapter %d...", chapterIdx))

	result, callErr := llm.CallAPIStreamMessages(ctx, apiCfg, []llm.Message{
		{Role: "system", Content: baseSystem(cfg, "author_default", lang)},
		{Role: "user", Content: user.String()},
	}, func(text string) { logger.ContentChunk(chapterIdx, text) })

	canceled := ctx.Err() == context.Canceled
	text := result.Content
	if callErr != nil && !canceled {
		logger.TaskEnd("write", false)
		return callErr
	}
	if strings.TrimSpace(text) == "" {
		logger.TaskEnd("write", false)
		if canceled {
			return context.Canceled
		}
		return fmt.Errorf("empty chapter output")
	}
	if err := fsutil.WriteFileAtomic(chapterPath(projDir, chapterIdx), []byte(strings.TrimSpace(text))); err != nil {
		logger.TaskEnd("write", false)
		return err
	}
	updateProgress(projDir, "writing", o.Title, chapterIdx)
	if canceled {
		logger.WarnBilingual(
			fmt.Sprintf("第 %d 章被取消，已保存部分草稿", chapterIdx),
			fmt.Sprintf("Chapter %d cancelled; partial draft saved", chapterIdx))
	} else {
		logger.SuccessBilingual(
			fmt.Sprintf("第 %d 章完成", chapterIdx),
			fmt.Sprintf("Chapter %d done", chapterIdx))
	}
	logger.TaskEnd("write", true)
	return nil
}

func readPrevTail(projDir string, chapterIdx int) string {
	if chapterIdx <= 1 {
		return ""
	}
	data, err := fsutil.ReadFileIfExists(chapterPath(projDir, chapterIdx-1))
	if err != nil || data == nil {
		return ""
	}
	r := []rune(strings.TrimSpace(string(data)))
	if len(r) > 600 {
		r = r[len(r)-600:]
	}
	return string(r)
}

func updateProgress(projDir, phase, title string, chapterIdx int) {
	path := filepath.Join(projDir, "progress.json")
	p, _ := story.LoadProgress(path)
	if p == nil {
		p = &story.Progress{}
	}
	p.Phase = phase
	if title != "" {
		p.Title = title
	}
	if chapterIdx > 0 {
		p.CurrentChapterIdx = chapterIdx
	}
	_ = story.SaveProgress(path, p)
}

// GetChapter returns the stored prose for a chapter ("" if missing).
func GetChapter(projDir string, n int) (string, error) {
	data, err := fsutil.ReadFileIfExists(chapterPath(projDir, n))
	if err != nil || data == nil {
		return "", err
	}
	return string(data), nil
}

// ListChapters returns existing chapter numbers.
func ListChapters(projDir string) ([]int, error) {
	entries, err := fsutil.ListDirSorted(chaptersDir(projDir))
	if err != nil {
		return nil, err
	}
	nums := []int{}
	for _, e := range entries {
		base := strings.TrimSuffix(e, ".txt")
		if !strings.HasPrefix(base, "ch") {
			continue
		}
		if n, err := strconv.Atoi(base[2:]); err == nil {
			nums = append(nums, n)
		}
	}
	return nums, nil
}

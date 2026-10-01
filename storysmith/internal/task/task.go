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
	"os"
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
	mu          sync.Mutex
	cancel      context.CancelFunc
	name        string
	tokenLogger *sse.LogBroadcaster
	curUsage    *llm.TaskTokenUsage
}

func NewRunner() *Runner { return &Runner{} }

// TaskTokens returns the token usage tracker of the currently running task
// (nil when no task is active). Used by SSE progress reporting.
func (r *Runner) TaskTokens() *llm.TaskTokenUsage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.curUsage
}

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
	return r.startWithProgress(name, nil)
}

// startWithProgress is start() with an optional progress callback that LLM
// streaming handlers can use to report token deltas.
func (r *Runner) startWithProgress(name string, onProgress llm.ProgressFunc) (context.Context, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		return nil, fmt.Errorf("task already running: %s", r.name)
	}
	ctx := llm.WithProgress(context.Background(), onProgress)
	ctx, usage := llm.WithTaskTokens(ctx, r.tokenLogger)
	r.curUsage = usage
	ctx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	r.name = name
	return ctx, nil
}

// tokenLogger is optionally injected by the HTTP layer so task token usage
// is broadcast over SSE like in show-me-the-story.
func (r *Runner) SetTokenLogger(logger *sse.LogBroadcaster) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokenLogger = logger
}

func (r *Runner) finish() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		r.cancel()
	}
	r.cancel = nil
	r.name = ""
	r.curUsage = nil
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
	StartFrom    int    `json:"start_from,omitempty"`
	EndAt        int    `json:"end_at,omitempty"`
}

// RangeLabel renders the batch range for prompts ("1-12", "13-24" or "").
func (req OutlineRequest) RangeLabel() string {
	if req.StartFrom > 0 && req.EndAt >= req.StartFrom {
		return fmt.Sprintf("%d-%d", req.StartFrom, req.EndAt)
	}
	return ""
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
	if block := settingsBlockFor(projDir, zh); block != "" {
		user.WriteString("\n" + block)
	}
	if mem := memoryBlockFor(projDir, zh); mem != "" {
		user.WriteString("\n" + mem)
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

// ---------------------------------------------------------------------------
// Outline editing / revision
// ---------------------------------------------------------------------------

type ChapterOutlineEdit struct {
	Index      *int    `json:"index,omitempty"`
	Title      *string `json:"title,omitempty"`
	Summary    *string `json:"summary,omitempty"`
	Section    *string `json:"section,omitempty"`
	KeyPoints  *string `json:"key_points,omitempty"`
	Characters *string `json:"characters,omitempty"`
}

func strOr(p *string, cur string) string {
	if p == nil {
		return cur
	}
	return *p
}

// EditOutlineChapter applies a manual patch to one outline chapter. Editing
// any chapter resets confirmation so the user re-accepts the changed plan.
func EditOutlineChapter(projDir string, num int, edit ChapterOutlineEdit) (*Outline, error) {
	o, err := LoadOutline(projDir)
	if err != nil {
		return nil, err
	}
	if o == nil {
		return nil, fmt.Errorf("no outline generated yet")
	}
	if num < 1 || num > len(o.Chapters) {
		return nil, fmt.Errorf("invalid chapter number %d", num)
	}
	ch := &o.Chapters[num-1]
	ch.Title = strOr(edit.Title, ch.Title)
	ch.Summary = strOr(edit.Summary, ch.Summary)
	ch.Section = strOr(edit.Section, ch.Section)
	ch.KeyPoints = strOr(edit.KeyPoints, ch.KeyPoints)
	ch.Characters = strOr(edit.Characters, ch.Characters)
	o.Confirmed = false
	if err := SaveOutline(projDir, o); err != nil {
		return nil, err
	}
	return o, nil
}

// ReviseOutline asks the model to rewrite the whole outline given feedback,
// keeping the same chapter count unless the feedback says otherwise.
func (r *Runner) ReviseOutline(apiCfg *config.APIConfig, cfg *config.Config, projDir, feedback string, logger *sse.LogBroadcaster) error {
	ctx, err := r.start("outline-revise")
	if err != nil {
		return err
	}
	defer r.finish()

	o, err := LoadOutline(projDir)
	if err != nil {
		return err
	}
	if o == nil {
		return fmt.Errorf("no outline generated yet")
	}
	lang := cfg.Language
	zh := i18n.NormalizeLanguage(lang) == i18n.LangZH

	cur, _ := json.MarshalIndent(o, "", "  ")
	var user strings.Builder
	if zh {
		fmt.Fprintf(&user, "以下是当前小说大纲（JSON）：\n%s\n\n请根据修改意见重写完整大纲，保持 JSON 结构不变（不要代码块围栏），输出修订后的完整 JSON。\n修改意见：%s\n", string(cur), feedback)
	} else {
		fmt.Fprintf(&user, "Current novel outline (JSON):\n%s\n\nRewrite the full outline honoring the revision feedback; keep the same JSON shape (no code fences); output only the revised JSON.\nFeedback: %s\n", string(cur), feedback)
	}

	logger.TaskStart("outline-revise")
	logger.InfoBilingual("正在修订大纲...", "Revising outline...")
	raw, callErr := llm.CallAPIMessages(ctx, apiCfg, []llm.Message{
		{Role: "system", Content: baseSystem(cfg, "outline_editor_json", lang)},
		{Role: "user", Content: user.String()},
	})
	if callErr != nil {
		logger.TaskEnd("outline-revise", false)
		return callErr
	}
	jsonStr := llm.ExtractJSON(raw)
	if jsonStr == "" {
		logger.TaskEnd("outline-revise", false)
		return fmt.Errorf("model did not return valid JSON")
	}
	var no Outline
	if err := json.Unmarshal([]byte(jsonStr), &no); err != nil {
		logger.TaskEnd("outline-revise", false)
		return fmt.Errorf("parse revised outline JSON: %w", err)
	}
	if len(no.Chapters) == 0 {
		logger.TaskEnd("outline-revise", false)
		return fmt.Errorf("revised outline has no chapters")
	}
	for i := range no.Chapters {
		no.Chapters[i].Index = i + 1
	}
	no.Confirmed = false
	no.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	if no.Title == "" {
		no.Title = o.Title
	}
	if err := SaveOutline(projDir, &no); err != nil {
		logger.TaskEnd("outline-revise", false)
		return err
	}
	logger.SuccessBilingual(
		fmt.Sprintf("大纲已修订：%d 章（待确认）", len(no.Chapters)),
		fmt.Sprintf("Outline revised: %d chapters (pending confirmation)", len(no.Chapters)))
	logger.TaskEnd("outline-revise", true)
	return nil
}

// DeleteOutline removes outline.json and returns the project to setup phase.
func DeleteOutline(projDir string) error {
	if err := fsutil.Delete(outlinePath(projDir)); err != nil && !os.IsNotExist(err) {
		return err
	}
	updateProgress(projDir, "setup", "", 0)
	return nil
}

// ---------------------------------------------------------------------------
// Chapter review / polish / deletion
// ---------------------------------------------------------------------------

// ReviewChapter runs a critique pass over a written chapter (structure beat,
// POV, style, continuity) without rewriting it. Returns the review text.
func (r *Runner) ReviewChapter(apiCfg *config.APIConfig, cfg *config.Config, projDir string, chapterIdx int, logger *sse.LogBroadcaster) (string, error) {
	ctx, err := r.start("review")
	if err != nil {
		return "", err
	}
	defer r.finish()

	o, _ := LoadOutline(projDir)
	text, err := GetChapter(projDir, chapterIdx)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("chapter %d has no content to review", chapterIdx)
	}
	lang := cfg.Language
	zh := i18n.NormalizeLanguage(lang) == i18n.LangZH

	var ctxInfo string
	if o != nil && chapterIdx <= len(o.Chapters) {
		ch := o.Chapters[chapterIdx-1]
		ctxInfo = fmt.Sprintf("%s | %s", ch.Title, ch.Summary)
	}

	var user strings.Builder
	if zh {
		fmt.Fprintf(&user, "请评审第 %d 章正文。大纲要点：%s\n\n以要点列表输出：与大纲/结构的偏离、视角一致性问题、风格问题、连贯性问题、以及最多 3 条具体改进建议。不要改写正文。\n\n正文：\n%s\n", chapterIdx, ctxInfo, text)
	} else {
		fmt.Fprintf(&user, "Review chapter %d. Outline intent: %s\n\nOutput a bullet list covering: deviations from outline/structure, POV consistency issues, style issues, continuity problems, and up to 3 concrete improvement suggestions. Do not rewrite the prose.\n\nProse:\n%s\n", chapterIdx, ctxInfo, text)
	}

	logger.TaskStart("review")
	logger.InfoBilingual(
		fmt.Sprintf("正在评审第 %d 章...", chapterIdx),
		fmt.Sprintf("Reviewing chapter %d...", chapterIdx))
	review, callErr := llm.CallAPIMessages(ctx, apiCfg, []llm.Message{
		{Role: "system", Content: baseSystem(cfg, "consistency_reviewer", lang)},
		{Role: "user", Content: user.String()},
	})
	if callErr != nil {
		logger.TaskEnd("review", false)
		return "", callErr
	}
	if err := fsutil.WriteFileAtomic(reviewPath(projDir, chapterIdx), []byte(review)); err != nil {
		logger.TaskEnd("review", false)
		return review, err
	}
	logger.SuccessBilingual(
		fmt.Sprintf("第 %d 章评审完成", chapterIdx),
		fmt.Sprintf("Chapter %d review ready", chapterIdx))
	logger.TaskEnd("review", true)
	return review, nil
}

// PolishChapter rewrites the chapter applying the given instruction while
// keeping all plot beats intact (segment-level or whole-chapter).
func (r *Runner) PolishChapter(apiCfg *config.APIConfig, cfg *config.Config, projDir string, chapterIdx int, instruction string, logger *sse.LogBroadcaster) error {
	ctx, err := r.start("polish")
	if err != nil {
		return err
	}
	defer r.finish()

	text, err := GetChapter(projDir, chapterIdx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("chapter %d has no content to polish", chapterIdx)
	}
	lang := cfg.Language
	zh := i18n.NormalizeLanguage(lang) == i18n.LangZH

	var user strings.Builder
	if zh {
		fmt.Fprintf(&user, "请按以下要求润色第 %d 章，保留全部情节推进，只输出修改后的完整正文（无标题、无说明）。\n要求：%s\n\n原文：\n%s\n", chapterIdx, instruction, text)
	} else {
		fmt.Fprintf(&user, "Polish chapter %d per the instruction below. Keep every plot beat intact; output only the full revised prose (no title, no commentary).\nInstruction: %s\n\nOriginal:\n%s\n", chapterIdx, instruction, text)
	}

	logger.TaskStart("polish")
	logger.StreamStart(chapterIdx)
	logger.InfoBilingual(
		fmt.Sprintf("正在润色第 %d 章...", chapterIdx),
		fmt.Sprintf("Polishing chapter %d...", chapterIdx))
	result, callErr := llm.CallAPIStreamMessages(ctx, apiCfg, []llm.Message{
		{Role: "system", Content: baseSystem(cfg, "polish_editor", lang)},
		{Role: "user", Content: user.String()},
	}, func(t string) { logger.ContentChunk(chapterIdx, t) })
	canceled := ctx.Err() == context.Canceled
	if callErr != nil && !canceled {
		logger.TaskEnd("polish", false)
		return callErr
	}
	if strings.TrimSpace(result.Content) == "" {
		logger.TaskEnd("polish", false)
		if canceled {
			return context.Canceled
		}
		return fmt.Errorf("empty polish output")
	}
	if err := fsutil.WriteFileAtomic(chapterPath(projDir, chapterIdx), []byte(strings.TrimSpace(result.Content))); err != nil {
		logger.TaskEnd("polish", false)
		return err
	}
	logger.TaskEnd("polish", true)
	return nil
}

// DeleteChapter removes one chapter's prose (and its review file).
func DeleteChapter(projDir string, n int) error {
	if err := fsutil.Delete(chapterPath(projDir, n)); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = fsutil.Delete(reviewPath(projDir, n))
	return nil
}

// ---------------------------------------------------------------------------
// Knowledge extraction (facts + foreshadows) after writing a chapter
// ---------------------------------------------------------------------------

const factsJSONShape = `{"facts":[{"content":"...","category":"character|location|item|event|promise|other"}],
 "foreshadows":[{"name":"...","description":"...","action":"plant|advance|resolve","note":"..."}]}`

// TrackChapterFacts reads the chapter prose and extracts durable facts plus
// foreshadow events into progress.json (the knowledge/memory ledger).
func (r *Runner) TrackChapterFacts(apiCfg *config.APIConfig, cfg *config.Config, projDir string, chapterIdx int, logger *sse.LogBroadcaster) error {
	ctx, err := r.start("track")
	if err != nil {
		return err
	}
	defer r.finish()

	text, err := GetChapter(projDir, chapterIdx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("chapter %d has no content to analyze", chapterIdx)
	}
	lang := cfg.Language
	zh := i18n.NormalizeLanguage(lang) == i18n.LangZH

	// Trim very long chapters for small local models.
	runes := []rune(text)
	if len(runes) > 12000 {
		text = string(runes[:12000])
	}

	pPath := filepath.Join(projDir, "progress.json")
	p, err := story.LoadProgress(pPath)
	if err != nil {
		return err
	}
	if p == nil {
		p = &story.Progress{}
	}

	var known strings.Builder
	for _, m := range p.MemoryEntries {
		fmt.Fprintf(&known, "- [%s] %s\n", m.Category, m.Content)
	}
	for _, f := range p.Foreshadows {
		fmt.Fprintf(&known, "- foreshadow #%d %q status=%s\n", f.ID, f.Name, f.Status)
	}

	var user strings.Builder
	if zh {
		fmt.Fprintf(&user, "分析第 %d 章正文，提取需要长期记住的事实和伏笔动态。输出严格 JSON（无代码块围栏），格式：\n%s\n规则：事实须是不可逆的设定变化或关键信息；伏笔 action 为 plant(新埋)/advance(推进)/resolve(回收)。只输出相对已知记忆的新增内容。\n\n已知记忆：\n%s\n\n正文：\n%s\n", chapterIdx, factsJSONShape, known.String(), text)
	} else {
		fmt.Fprintf(&user, "Analyze chapter %d prose. Extract durable facts and foreshadow activity. Output strict JSON (no code fences) shaped:\n%s\nRules: facts must be irreversible setting changes or key information; foreshadow action is plant/advance/resolve. Only emit content new relative to known memory.\n\nKnown memory:\n%s\n\nProse:\n%s\n", chapterIdx, factsJSONShape, known.String(), text)
	}

	logger.TaskStart("track")
	logger.InfoBilingual(
		fmt.Sprintf("正在从第 %d 章提取事实与伏笔...", chapterIdx),
		fmt.Sprintf("Extracting facts & foreshadows from chapter %d...", chapterIdx))
	raw, callErr := llm.CallAPIMessages(ctx, apiCfg, []llm.Message{
		{Role: "system", Content: baseSystem(cfg, "fact_checker_json", lang)},
		{Role: "user", Content: user.String()},
	})
	if callErr != nil {
		logger.TaskEnd("track", false)
		return callErr
	}
	jsonStr := llm.ExtractJSON(raw)
	if jsonStr == "" {
		logger.TaskEnd("track", false)
		return fmt.Errorf("model did not return valid JSON")
	}
	var out struct {
		Facts []struct {
			Content  string `json:"content"`
			Category string `json:"category"`
		} `json:"facts"`
		Foreshadows []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Action      string `json:"action"`
			Note        string `json:"note"`
		} `json:"foreshadows"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &out); err != nil {
		logger.TaskEnd("track", false)
		return fmt.Errorf("parse facts JSON: %w", err)
	}

	addedFacts, addedFS := 0, 0
	knownFact := map[string]bool{}
	for _, m := range p.MemoryEntries {
		knownFact[m.Content] = true
	}
	for _, f := range out.Facts {
		c := strings.TrimSpace(f.Content)
		if c == "" || knownFact[c] {
			continue
		}
		knownFact[c] = true
		p.AddMemory(story.MemoryEntry{Content: c, Category: strings.TrimSpace(f.Category), Chapter: chapterIdx})
		addedFacts++
	}
	byName := map[string]*story.Foreshadow{}
	for i := range p.Foreshadows {
		f := &p.Foreshadows[i]
		byName[strings.ToLower(strings.TrimSpace(f.Name))] = f
	}
	for _, fs := range out.Foreshadows {
		name := strings.TrimSpace(fs.Name)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		existing := byName[key]
		switch strings.ToLower(strings.TrimSpace(fs.Action)) {
		case "plant":
			if existing != nil {
				existing.Events = append(existing.Events, story.ForeshadowEvent{Chapter: chapterIdx, Note: fs.Note})
				existing.Status = story.ForeshadowPlanted
			} else {
				nf := p.AddForeshadow(story.Foreshadow{
					Name: name, Description: fs.Description, PlantChapter: chapterIdx,
					Status: story.ForeshadowPlanted,
					Events: []story.ForeshadowEvent{{Chapter: chapterIdx, Note: fs.Note}},
				})
				byName[key] = &p.Foreshadows[len(p.Foreshadows)-1]
				_ = nf
			}
			addedFS++
		case "advance":
			if existing == nil {
				existing = &story.Foreshadow{}
				*existing = p.AddForeshadow(story.Foreshadow{Name: name, Description: fs.Description, PlantChapter: chapterIdx})
			}
			existing.Events = append(existing.Events, story.ForeshadowEvent{Chapter: chapterIdx, Note: fs.Note})
			existing.Status = story.ForeshadowAdvanced
			addedFS++
		case "resolve":
			if existing != nil {
				existing.Events = append(existing.Events, story.ForeshadowEvent{Chapter: chapterIdx, Note: fs.Note})
				existing.Status = story.ForeshadowResolved
				if fs.Note != "" {
					existing.Resolution = fs.Note
				}
				addedFS++
			}
		}
	}
	if err := story.SaveProgress(pPath, p); err != nil {
		logger.TaskEnd("track", false)
		return err
	}
	logger.SuccessBilingual(
		fmt.Sprintf("知识更新：新增 %d 条事实，%d 项伏笔动态", addedFacts, addedFS),
		fmt.Sprintf("Knowledge updated: %d new facts, %d foreshadow events", addedFacts, addedFS))
	logger.TaskEnd("track", true)
	return nil
}

// ---------------------------------------------------------------------------
// Book-level diagnosis (proofreading pass)
// ---------------------------------------------------------------------------

// DiagnoseBook reads summaries/tails of all written chapters and produces a
// whole-book consistency & pacing report saved to diagnosis.md.
func (r *Runner) DiagnoseBook(apiCfg *config.APIConfig, cfg *config.Config, projDir string, logger *sse.LogBroadcaster) (string, error) {
	ctx, err := r.start("diagnose")
	if err != nil {
		return "", err
	}
	defer r.finish()

	o, _ := LoadOutline(projDir)
	nums, err := ListChapters(projDir)
	if err != nil {
		return "", err
	}
	if len(nums) == 0 {
		return "", fmt.Errorf("no chapters written yet")
	}
	lang := cfg.Language
	zh := i18n.NormalizeLanguage(lang) == i18n.LangZH

	var digest strings.Builder
	pPath := filepath.Join(projDir, "progress.json")
	if p, err := story.LoadProgress(pPath); err == nil && p != nil {
		for _, f := range p.Foreshadows {
			fmt.Fprintf(&digest, "- foreshadow #%d %q [%s] planted ch%d, events: %d\n", f.ID, f.Name, f.Status, f.PlantChapter, len(f.Events))
		}
	}
	if o != nil {
		for _, ch := range o.Chapters {
			has := false
			for _, n := range nums {
				if n == ch.Index {
					has = true
				}
			}
			status := "written"
			if !has {
				status = "missing"
			}
			fmt.Fprintf(&digest, "ch%d %q (%s) — %s\n", ch.Index, ch.Title, ch.Section, status)
		}
	}
	for _, n := range nums {
		text, _ := GetChapter(projDir, n)
		runes := []rune(text)
		tail := runes
		if len(tail) > 400 {
			tail = tail[len(tail)-400:]
		}
		fmt.Fprintf(&digest, "\n--- ch%d ending ---\n%s\n", n, string(tail))
	}

	var user strings.Builder
	if zh {
		fmt.Fprintf(&user, "基于以下全书摘要材料，输出一份诊断报告（Markdown）：主线推进是否符合所选结构节拍、伏笔是否烂尾、节奏问题、人物一致性风险、以及按优先级排序的修改工单列表。不要改写正文。\n\n%s\n", digest.String())
	} else {
		fmt.Fprintf(&user, "Using the book digest below, produce a Markdown diagnosis report: main-plot progression vs. the chosen structure beats, unresolved foreshadows, pacing issues, character-consistency risks, and a prioritized fix roadmap. Do not rewrite prose.\n\n%s\n", digest.String())
	}

	logger.TaskStart("diagnose")
	logger.InfoBilingual("正在生成全书诊断报告...", "Generating book diagnosis report...")
	report, callErr := llm.CallAPIMessages(ctx, apiCfg, []llm.Message{
		{Role: "system", Content: baseSystem(cfg, "book_diagnosis", lang)},
		{Role: "user", Content: user.String()},
	})
	if callErr != nil {
		logger.TaskEnd("diagnose", false)
		return "", callErr
	}
	if err := fsutil.WriteFileAtomic(filepath.Join(projDir, "diagnosis.md"), []byte(report)); err != nil {
		logger.TaskEnd("diagnose", false)
		return report, err
	}
	logger.SuccessBilingual("全书诊断报告已生成", "Book diagnosis report ready")
	logger.TaskEnd("diagnose", true)
	return report, nil
}

// ---------------------------------------------------------------------------
// AI generation of world bible (characters / worldview / organizations)
// ---------------------------------------------------------------------------

// GenerateSettings drafts characters, worldview entries and organizations
// from the novel parameters + idea, returning them for user confirmation
// (nothing is persisted here; the UI posts accepted entities to /api/*).
func (r *Runner) GenerateSettings(apiCfg *config.APIConfig, cfg *config.Config, idea string, logger *sse.LogBroadcaster) (*story.ProjectSettings, error) {
	ctx, err := r.start("gen-settings")
	if err != nil {
		return nil, err
	}
	defer r.finish()

	lang := cfg.Language
	zh := i18n.NormalizeLanguage(lang) == i18n.LangZH
	shape := `{
 "characters":[{"name":"...","age":"...","appearance":"...","personality":"...","background":"...","motivation":"...","abilities":"..."}],
 "worldview":[{"category":"magic|technology|geography|history|culture|general","name":"...","description":"..."}],
 "organizations":[{"name":"...","type":"...","description":"...","members":["char name"]}]}
`
	var user strings.Builder
	if zh {
		fmt.Fprintf(&user, "根据以下小说参数设计故事设定集。输出严格 JSON（无代码块围栏），格式：\n%s\n要求：3–6 个主要人物、3–6 条世界观条目、1–3 个组织。创意：%s\n\n%s\n", shape, idea, genres.NovelPromptBlock(cfg.Story, lang))
	} else {
		fmt.Fprintf(&user, "Design a story bible from these novel parameters. Output strict JSON (no code fences) shaped:\n%s\nRequirements: 3-6 main characters, 3-6 worldview entries, 1-3 organizations. Idea: %s\n\n%s\n", shape, idea, genres.NovelPromptBlock(cfg.Story, lang))
	}

	logger.TaskStart("gen-settings")
	logger.InfoBilingual("正在生成设定集草稿...", "Drafting story bible...")
	raw, callErr := llm.CallAPIMessages(ctx, apiCfg, []llm.Message{
		{Role: "system", Content: baseSystem(cfg, "narrative_architect_json", lang)},
		{Role: "user", Content: user.String()},
	})
	if callErr != nil {
		logger.TaskEnd("gen-settings", false)
		return nil, callErr
	}
	jsonStr := llm.ExtractJSON(raw)
	if jsonStr == "" {
		logger.TaskEnd("gen-settings", false)
		return nil, fmt.Errorf("model did not return valid JSON")
	}
	var ps story.ProjectSettings
	if err := json.Unmarshal([]byte(jsonStr), &ps); err != nil {
		logger.TaskEnd("gen-settings", false)
		return nil, fmt.Errorf("parse settings JSON: %w", err)
	}
	ps.AssignNewIDs()
	logger.SuccessBilingual(
		fmt.Sprintf("设定集草稿完成：%d 人物 / %d 世界观 / %d 组织", len(ps.Characters), len(ps.Worldview), len(ps.Organizations)),
		fmt.Sprintf("Story bible draft ready: %d characters / %d worldview / %d orgs", len(ps.Characters), len(ps.Worldview), len(ps.Organizations)))
	logger.TaskEnd("gen-settings", true)
	return &ps, nil
}

func reviewPath(dir string, n int) string {
	return filepath.Join(dir, "reviews", fmt.Sprintf("ch%02d.md", n))
}

// GetReview returns a stored chapter review ("" if missing).
func GetReview(projDir string, n int) (string, error) {
	data, err := fsutil.ReadFileIfExists(reviewPath(projDir, n))
	if err != nil || data == nil {
		return "", err
	}
	return string(data), nil
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

// settingsBlockFor renders the project world bible for prompt injection.
func settingsBlockFor(projDir string, zh bool) string {
	ps, err := story.LoadProjectSettings(filepath.Join(projDir, "settings.json"))
	if err != nil || ps == nil {
		return ""
	}
	return ps.SettingsPromptBlock(zh)
}

// memoryBlockFor renders extracted facts + open foreshadows for continuity.
func memoryBlockFor(projDir string, zh bool) string {
	p, err := story.LoadProgress(filepath.Join(projDir, "progress.json"))
	if err != nil || p == nil {
		return ""
	}
	var b strings.Builder
	if len(p.MemoryEntries) > 0 {
		if zh {
			b.WriteString("【已确立事实（不可矛盾）】\n")
		} else {
			b.WriteString("[Established facts — do not contradict]\n")
		}
		for _, m := range p.MemoryEntries {
			fmt.Fprintf(&b, "- [%s] %s\n", m.Category, m.Content)
		}
	}
	open := []story.Foreshadow{}
	for _, f := range p.Foreshadows {
		if f.Status != story.ForeshadowResolved && f.Status != story.ForeshadowAbandoned {
			open = append(open, f)
		}
	}
	if len(open) > 0 {
		if zh {
			b.WriteString("【伏笔（未回收，可自然推进）】\n")
		} else {
			b.WriteString("[Open foreshadows — may be advanced naturally]\n")
		}
		for _, f := range open {
			fmt.Fprintf(&b, "- %q (%s), planted ch%d: %s\n", f.Name, f.Status, f.PlantChapter, f.Description)
		}
	}
	return b.String()
}

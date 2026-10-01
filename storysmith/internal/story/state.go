// Package story holds per-project on-disk state. This first slice covers the
// project index (current.txt) and progress.json; chapter/outline state will
// grow here in later milestones.
package story

import (
	"encoding/json"
	"os"
	"path/filepath"
	"storysmith/internal/fsutil"
)

// ForeshadowStatus tracks a planted hint through to payoff.
type ForeshadowStatus string

const (
	ForeshadowPlanned   ForeshadowStatus = "planned"
	ForeshadowPlanted   ForeshadowStatus = "planted"
	ForeshadowAdvanced  ForeshadowStatus = "advanced"
	ForeshadowResolved  ForeshadowStatus = "resolved"
	ForeshadowAbandoned ForeshadowStatus = "abandoned"
)

// ForeshadowEvent is one chapter appearance of a foreshadow thread.
type ForeshadowEvent struct {
	Chapter int    `json:"chapter"`
	Note    string `json:"note"`
}

// Foreshadow is a tracked narrative promise (planted → resolved).
type Foreshadow struct {
	ID            int               `json:"id"`
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	PlantChapter  int               `json:"plant_chapter"`
	TargetChapter int               `json:"target_chapter,omitempty"`
	TargetHorizon string            `json:"target_horizon,omitempty"`
	Status        ForeshadowStatus  `json:"status"`
	Events        []ForeshadowEvent `json:"events"`
	Resolution    string            `json:"resolution"`
}

// MemoryEntry is an extracted fact from written prose (the knowledge base).
type MemoryEntry struct {
	ID       int    `json:"id"`
	Content  string `json:"content"`
	Category string `json:"category"` // character | location | item | event | promise | other
	Chapter  int    `json:"chapter,omitempty"`
	Snippet  string `json:"snippet,omitempty"`
}

// Progress is the workflow-state file kept per project. It also carries the
// foreshadow ledger and extracted-fact memory, mirroring show-me-the-story.
type Progress struct {
	Phase             string `json:"phase"`
	Title             string `json:"title"`
	CurrentChapterIdx int    `json:"current_chapter_index"`

	NextForeshadowID int           `json:"next_foreshadow_id,omitempty"`
	Foreshadows      []Foreshadow  `json:"foreshadows,omitempty"`
	NextMemoryID     int           `json:"next_memory_id,omitempty"`
	MemoryEntries    []MemoryEntry `json:"memory_entries,omitempty"`
}

// AddForeshadow assigns the next id and stores the thread.
func (p *Progress) AddForeshadow(f Foreshadow) Foreshadow {
	if p.NextForeshadowID == 0 {
		p.NextForeshadowID = 1
	}
	f.ID = p.NextForeshadowID
	p.NextForeshadowID++
	if f.Status == "" {
		f.Status = ForeshadowPlanned
	}
	if f.Events == nil {
		f.Events = []ForeshadowEvent{}
	}
	p.Foreshadows = append(p.Foreshadows, f)
	return f
}

func (p *Progress) FindForeshadow(id int) *Foreshadow {
	for i := range p.Foreshadows {
		if p.Foreshadows[i].ID == id {
			return &p.Foreshadows[i]
		}
	}
	return nil
}

func (p *Progress) DeleteForeshadow(id int) bool {
	out := p.Foreshadows[:0]
	found := false
	for _, f := range p.Foreshadows {
		if f.ID == id {
			found = true
			continue
		}
		out = append(out, f)
	}
	p.Foreshadows = out
	return found
}

// AddMemory stores an extracted fact with the next id.
func (p *Progress) AddMemory(m MemoryEntry) MemoryEntry {
	if p.NextMemoryID == 0 {
		p.NextMemoryID = 1
	}
	m.ID = p.NextMemoryID
	p.NextMemoryID++
	if m.Category == "" {
		m.Category = "other"
	}
	p.MemoryEntries = append(p.MemoryEntries, m)
	return m
}

func (p *Progress) DeleteMemory(id int) bool {
	out := p.MemoryEntries[:0]
	found := false
	for _, m := range p.MemoryEntries {
		if m.ID == id {
			found = true
			continue
		}
		out = append(out, m)
	}
	p.MemoryEntries = out
	return found
}

// LoadProgress reads progress.json; a missing file yields (nil, nil).
func LoadProgress(path string) (*Progress, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var p Progress
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func SaveProgress(path string, p *Progress) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, data)
}

// ReadCurrentProject returns the name stored in <progDir>/current.txt, or ""
// when no project has been selected yet.
func ReadCurrentProject(progDir string) string {
	data, err := os.ReadFile(filepath.Join(progDir, "current.txt"))
	if err != nil {
		return ""
	}
	name := string(data)
	// trim trailing whitespace/newlines
	for len(name) > 0 && (name[len(name)-1] == '\n' || name[len(name)-1] == '\r' || name[len(name)-1] == ' ') {
		name = name[:len(name)-1]
	}
	return name
}

func WriteCurrentProject(progDir, name string) error {
	return fsutil.WriteFileAtomic(filepath.Join(progDir, "current.txt"), []byte(name))
}

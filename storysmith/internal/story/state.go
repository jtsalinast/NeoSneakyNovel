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

// Progress is the lightweight workflow-state file kept per project.
type Progress struct {
	Phase             string `json:"phase"`
	Title             string `json:"title"`
	CurrentChapterIdx int    `json:"current_chapter_index"`
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

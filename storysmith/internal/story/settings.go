// Project "settings" (world bible): characters, worldview entries,
// organizations and relations — the entity model ported from
// show-me-the-story's settings pages. Stored per project in settings.json.
package story

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"storysmith/internal/fsutil"
)

type Character struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Age         string `json:"age,omitempty"`
	Appearance  string `json:"appearance,omitempty"`
	Personality string `json:"personality,omitempty"`
	Background  string `json:"background,omitempty"`
	Motivation  string `json:"motivation,omitempty"`
	Abilities   string `json:"abilities,omitempty"`
	Notes       string `json:"notes,omitempty"`
}

type WorldviewEntry struct {
	ID          string `json:"id"`
	Category    string `json:"category"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Tags        string `json:"tags,omitempty"`
}

type Organization struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Members     []string `json:"members,omitempty"`
}

type Relation struct {
	ID         string `json:"id"`
	SourceID   string `json:"source_id"`
	SourceType string `json:"source_type"`
	TargetID   string `json:"target_id"`
	TargetType string `json:"target_type"`
	Label      string `json:"label"`
}

type ProjectSettings struct {
	Characters    []Character      `json:"characters"`
	Worldview     []WorldviewEntry `json:"worldview"`
	Organizations []Organization   `json:"organizations"`
	Relations     []Relation       `json:"relations"`
}

func NewProjectSettings() *ProjectSettings {
	return &ProjectSettings{
		Characters:    []Character{},
		Worldview:     []WorldviewEntry{},
		Organizations: []Organization{},
		Relations:     []Relation{},
	}
}

// LoadProjectSettings reads settings.json; a missing file yields an empty set.
func LoadProjectSettings(path string) (*ProjectSettings, error) {
	data, err := fsutil.ReadFileIfExists(path)
	if err != nil || data == nil {
		return NewProjectSettings(), err
	}
	var ps ProjectSettings
	if err := json.Unmarshal(data, &ps); err != nil {
		return nil, fmt.Errorf("parse settings.json: %w", err)
	}
	if ps.Characters == nil {
		ps.Characters = []Character{}
	}
	if ps.Worldview == nil {
		ps.Worldview = []WorldviewEntry{}
	}
	if ps.Organizations == nil {
		ps.Organizations = []Organization{}
	}
	if ps.Relations == nil {
		ps.Relations = []Relation{}
	}
	return &ps, nil
}

func SaveProjectSettings(path string, ps *ProjectSettings) error {
	data, err := json.MarshalIndent(ps, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, data)
}

var idSanitizer = regexp.MustCompile(`[^a-z0-9]+`)

// makeEntityID builds a stable, filesystem-safe id like "char-elena-vane".
func makeEntityID(kind, name string) string {
	slug := strings.ToLower(strings.TrimSpace(name))
	slug = idSanitizer.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "unnamed"
	}
	return kind + "-" + slug
}

// ensureUniqueID appends -2, -3... when the id already exists among taken.
func ensureUniqueID(base string, taken map[string]bool) string {
	id := base
	for n := 2; taken[id]; n++ {
		id = fmt.Sprintf("%s-%d", base, n)
	}
	return id
}

func (ps *ProjectSettings) AssignNewIDs() {
	taken := map[string]bool{}
	for _, c := range ps.Characters {
		taken[c.ID] = true
	}
	for _, w := range ps.Worldview {
		taken[w.ID] = true
	}
	for _, o := range ps.Organizations {
		taken[o.ID] = true
	}
	for _, r := range ps.Relations {
		taken[r.ID] = true
	}
	for i := range ps.Characters {
		if ps.Characters[i].ID == "" {
			ps.Characters[i].ID = ensureUniqueID(makeEntityID("char", ps.Characters[i].Name), taken)
			taken[ps.Characters[i].ID] = true
		}
	}
	for i := range ps.Worldview {
		if ps.Worldview[i].ID == "" {
			ps.Worldview[i].ID = ensureUniqueID(makeEntityID("world", ps.Worldview[i].Name), taken)
			taken[ps.Worldview[i].ID] = true
		}
	}
	for i := range ps.Organizations {
		if ps.Organizations[i].ID == "" {
			ps.Organizations[i].ID = ensureUniqueID(makeEntityID("org", ps.Organizations[i].Name), taken)
			taken[ps.Organizations[i].ID] = true
		}
	}
	for i := range ps.Relations {
		if ps.Relations[i].ID == "" {
			base := makeEntityID("rel", ps.Relations[i].SourceID+"-"+ps.Relations[i].TargetID+"-"+ps.Relations[i].Label)
			ps.Relations[i].ID = ensureUniqueID(base, taken)
			taken[ps.Relations[i].ID] = true
		}
	}
}

// ---------------------------------------------------------------------------
// CRUD helpers (return the updated list so handlers can answer with one call)
// ---------------------------------------------------------------------------

func (ps *ProjectSettings) AddCharacter(c Character) (Character, error) {
	if strings.TrimSpace(c.Name) == "" {
		return c, fmt.Errorf("character name is required")
	}
	if c.ID == "" {
		c.ID = makeEntityID("char", c.Name)
	}
	ps.Characters = append(ps.Characters, c)
	ps.AssignNewIDs()
	return c, nil
}

func (ps *ProjectSettings) UpdateCharacter(id string, in Character) (Character, error) {
	for i := range ps.Characters {
		if ps.Characters[i].ID == id {
			in.ID = id
			if strings.TrimSpace(in.Name) == "" {
				in.Name = ps.Characters[i].Name
			}
			ps.Characters[i] = in
			return in, nil
		}
	}
	return in, fmt.Errorf("character not found: %s", id)
}

func (ps *ProjectSettings) DeleteCharacter(id string) bool {
	out := ps.Characters[:0]
	found := false
	for _, c := range ps.Characters {
		if c.ID == id {
			found = true
			continue
		}
		out = append(out, c)
	}
	ps.Characters = out
	return found
}

func (ps *ProjectSettings) AddWorldview(w WorldviewEntry) (WorldviewEntry, error) {
	if strings.TrimSpace(w.Name) == "" {
		return w, fmt.Errorf("worldview name is required")
	}
	if strings.TrimSpace(w.Category) == "" {
		w.Category = "general"
	}
	if w.ID == "" {
		w.ID = makeEntityID("world", w.Name)
	}
	ps.Worldview = append(ps.Worldview, w)
	ps.AssignNewIDs()
	return w, nil
}

func (ps *ProjectSettings) UpdateWorldview(id string, in WorldviewEntry) (WorldviewEntry, error) {
	for i := range ps.Worldview {
		if ps.Worldview[i].ID == id {
			in.ID = id
			if strings.TrimSpace(in.Name) == "" {
				in.Name = ps.Worldview[i].Name
			}
			ps.Worldview[i] = in
			return in, nil
		}
	}
	return in, fmt.Errorf("worldview entry not found: %s", id)
}

func (ps *ProjectSettings) DeleteWorldview(id string) bool {
	out := ps.Worldview[:0]
	found := false
	for _, w := range ps.Worldview {
		if w.ID == id {
			found = true
			continue
		}
		out = append(out, w)
	}
	ps.Worldview = out
	return found
}

func (ps *ProjectSettings) AddOrganization(o Organization) (Organization, error) {
	if strings.TrimSpace(o.Name) == "" {
		return o, fmt.Errorf("organization name is required")
	}
	if strings.TrimSpace(o.Type) == "" {
		o.Type = "group"
	}
	if o.ID == "" {
		o.ID = makeEntityID("org", o.Name)
	}
	ps.Organizations = append(ps.Organizations, o)
	ps.AssignNewIDs()
	return o, nil
}

func (ps *ProjectSettings) UpdateOrganization(id string, in Organization) (Organization, error) {
	for i := range ps.Organizations {
		if ps.Organizations[i].ID == id {
			in.ID = id
			if strings.TrimSpace(in.Name) == "" {
				in.Name = ps.Organizations[i].Name
			}
			ps.Organizations[i] = in
			return in, nil
		}
	}
	return in, fmt.Errorf("organization not found: %s", id)
}

func (ps *ProjectSettings) DeleteOrganization(id string) bool {
	out := ps.Organizations[:0]
	found := false
	for _, o := range ps.Organizations {
		if o.ID == id {
			found = true
			continue
		}
		out = append(out, o)
	}
	ps.Organizations = out
	return found
}

func (ps *ProjectSettings) AddRelation(r Relation) (Relation, error) {
	if strings.TrimSpace(r.SourceID) == "" || strings.TrimSpace(r.TargetID) == "" {
		return r, fmt.Errorf("relation requires source_id and target_id")
	}
	if strings.TrimSpace(r.Label) == "" {
		return r, fmt.Errorf("relation label is required")
	}
	if r.SourceType == "" {
		r.SourceType = "character"
	}
	if r.TargetType == "" {
		r.TargetType = "character"
	}
	if r.ID == "" {
		r.ID = makeEntityID("rel", r.SourceID+"_"+r.TargetID+"_"+r.Label)
	}
	ps.Relations = append(ps.Relations, r)
	ps.AssignNewIDs()
	return r, nil
}

func (ps *ProjectSettings) UpdateRelation(id string, in Relation) (Relation, error) {
	for i := range ps.Relations {
		if ps.Relations[i].ID == id {
			in.ID = id
			ps.Relations[i] = in
			return in, nil
		}
	}
	return in, fmt.Errorf("relation not found: %s", id)
}

func (ps *ProjectSettings) DeleteRelation(id string) bool {
	out := ps.Relations[:0]
	found := false
	for _, r := range ps.Relations {
		if r.ID == id {
			found = true
			continue
		}
		out = append(out, r)
	}
	ps.Relations = out
	return found
}

// FindEntity resolves an id across all four lists, returning (entity, type).
func (ps *ProjectSettings) FindEntity(id string) (any, string, bool) {
	for _, c := range ps.Characters {
		if c.ID == id {
			return c, "character", true
		}
	}
	for _, w := range ps.Worldview {
		if w.ID == id {
			return w, "worldview", true
		}
	}
	for _, o := range ps.Organizations {
		if o.ID == id {
			return o, "organization", true
		}
	}
	return nil, "", false
}

// EntityNames returns sorted names of all entities (for validation messages).
func (ps *ProjectSettings) EntityNames() []string {
	names := []string{}
	for _, c := range ps.Characters {
		names = append(names, c.Name)
	}
	for _, w := range ps.Worldview {
		names = append(names, w.Name)
	}
	for _, o := range ps.Organizations {
		names = append(names, o.Name)
	}
	sort.Strings(names)
	return names
}

// SettingsPromptBlock renders the world bible as a compact system-prompt
// fragment so chapter writing honors registered characters/world rules.
func (ps *ProjectSettings) SettingsPromptBlock(zh bool) string {
	var b strings.Builder
	if len(ps.Characters) > 0 {
		if zh {
			b.WriteString("【已登记人物】\n")
		} else {
			b.WriteString("[Registered characters]\n")
		}
		for _, c := range ps.Characters {
			parts := []string{c.Name}
			add := func(label, v string) {
				if strings.TrimSpace(v) != "" {
					parts = append(parts, label+": "+v)
				}
			}
			if zh {
				add("年龄", c.Age)
				add("性格", c.Personality)
				add("背景", c.Background)
				add("动机", c.Motivation)
				add("能力", c.Abilities)
			} else {
				add("age", c.Age)
				add("personality", c.Personality)
				add("background", c.Background)
				add("motivation", c.Motivation)
				add("abilities", c.Abilities)
			}
			b.WriteString("- " + strings.Join(parts, ", ") + "\n")
		}
	}
	if len(ps.Worldview) > 0 {
		if zh {
			b.WriteString("【世界观设定】\n")
		} else {
			b.WriteString("[Worldview entries]\n")
		}
		for _, w := range ps.Worldview {
			b.WriteString(fmt.Sprintf("- [%s] %s: %s\n", w.Category, w.Name, w.Description))
		}
	}
	if len(ps.Organizations) > 0 {
		if zh {
			b.WriteString("【组织势力】\n")
		} else {
			b.WriteString("[Organizations]\n")
		}
		for _, o := range ps.Organizations {
			b.WriteString(fmt.Sprintf("- %s (%s): %s\n", o.Name, o.Type, o.Description))
		}
	}
	if len(ps.Relations) > 0 {
		if zh {
			b.WriteString("【人物关系】\n")
		} else {
			b.WriteString("[Relations]\n")
		}
		nameOf := func(id string) string {
			e, _, ok := ps.FindEntity(id)
			if !ok {
				return id
			}
			switch v := e.(type) {
			case Character:
				return v.Name
			case WorldviewEntry:
				return v.Name
			case Organization:
				return v.Name
			}
			return id
		}
		for _, r := range ps.Relations {
			b.WriteString(fmt.Sprintf("- %s → %s: %s\n", nameOf(r.SourceID), nameOf(r.TargetID), r.Label))
		}
	}
	return b.String()
}

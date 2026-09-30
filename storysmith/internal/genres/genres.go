// Package genres exposes the novel-parameter catalogue (genres, subgenres,
// story structures) used by the Parameters tab. The genre/subgenre data is a
// re-implementation of the option sets popularized by NovelWriter-style tools;
// structures/lengths follow their own maps.
package genres

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

//go:embed genre_configs.json
var genreData []byte

// SubgenreConfig holds AI-generation hints for one subgenre.
type SubgenreConfig struct {
	ImpliedSettings map[string]bool     `json:"implied_settings"`
	ProtagonistType []string            `json:"protagonist_types"`
	ConflictScales  []string            `json:"conflict_scales"`
	Tones           []string            `json:"tones"`
	SettingLists    map[string][]string `json:"setting_lists,omitempty"` // timeframes/locations/tech_levels/...
	SettingNotes    map[string]any      `json:"setting_notes,omitempty"` // free-form notes kept verbatim
}

// Catalogue is the full genre -> subgenre -> config tree.
type Catalogue map[string]map[string]SubgenreConfig

var catalogue Catalogue

func init() {
	if err := json.Unmarshal(genreData, &catalogue); err != nil {
		panic("genres: bad embedded catalogue: " + err.Error())
	}
}

// GenreNames returns supported genres in stable alphabetical order.
func GenreNames() []string {
	names := make([]string, 0, len(catalogue))
	for g := range catalogue {
		names = append(names, g)
	}
	sort.Strings(names)
	return names
}

// SubgenreNames lists subgenres for a genre (alphabetical).
func SubgenreNames(genre string) ([]string, error) {
	subs, ok := catalogue[genre]
	if !ok {
		return nil, fmt.Errorf("unknown genre %q", genre)
	}
	names := make([]string, 0, len(subs))
	for s := range subs {
		names = append(names, s)
	}
	sort.Strings(names)
	return names, nil
}

// Config returns AI-generation hints for a genre+subgenre pair.
func Config(genre, subgenre string) (*SubgenreConfig, error) {
	subs, ok := catalogue[genre]
	if !ok {
		return nil, fmt.Errorf("unknown genre %q", genre)
	}
	cfg, ok := subs[subgenre]
	if !ok {
		return nil, fmt.Errorf("unknown subgenre %q for genre %q", subgenre, genre)
	}
	return &cfg, nil
}

// SettingsHint renders implied settings as a compact prompt fragment,
// e.g. "magic system, medieval setting".
func (c *SubgenreConfig) SettingsHint() string {
	keys := make([]string, 0, len(c.ImpliedSettings))
	for k, v := range c.ImpliedSettings {
		if v {
			keys = append(keys, strings.ReplaceAll(k, "_", " "))
		}
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

// ----- Length / structure options (mirrors the classic parameter flow) -----

var LengthOptions = []string{"Short Story", "Novella", "Novel (Standard)", "Novel (Epic)"}

var StructureMap = map[string][]string{
	"Short Story":      {"3-Act Structure", "Fichtean Curve", "Freytag's Pyramid"},
	"Novella":          {"3-Act Structure", "Seven-Point Structure", "Hero's Journey (Simplified)"},
	"Novel (Standard)": {"3-Act Structure", "6-Act Structure", "Save the Cat!", "Hero's Journey"},
	"Novel (Epic)":     {"6-Act Structure", "Hero's Journey", "Save the Cat!", "Episodic Structure"},
}

var DefaultStructure = map[string]string{
	"Short Story":      "3-Act Structure",
	"Novella":          "3-Act Structure",
	"Novel (Standard)": "6-Act Structure",
	"Novel (Epic)":     "6-Act Structure",
}

var StructureSections = map[string][]string{
	"3-Act Structure":       {"Act 1: Setup", "Act 2: Confrontation", "Act 3: Resolution"},
	"6-Act Structure":       {"Beginning", "Rising Action", "First Climax", "Solution Finding", "Second Climax", "Resolution"},
	"Fichtean Curve":        {"Inciting Incident", "Rising Action", "Climax", "Falling Action", "Denouement"},
	"Freytag's Pyramid":     {"Exposition", "Rising Action", "Climax", "Falling Action", "Catastrophe/Denouement"},
	"Seven-Point Structure": {"Hook", "Plot Point 1", "Pinch Point 1", "Midpoint", "Pinch Point 2", "Plot Point 2", "Resolution"},
	"Hero's Journey": {"The Ordinary World", "The Call to Adventure", "Refusal of the Call", "Meeting the Mentor",
		"Crossing the Threshold", "Tests, Allies, and Enemies", "Approach to the Inmost Cave",
		"The Ordeal", "Reward (Seizing the Sword)", "The Road Back", "The Resurrection", "Return with the Elixir"},
	"Hero's Journey (Simplified)": {"Departure", "Initiation", "Return"},
	"Save the Cat!": {"Opening Image", "Theme Stated", "Set-up", "Catalyst", "Debate", "Break into Two", "B Story",
		"Fun and Games", "Midpoint", "Bad Guys Close In", "All Is Lost", "Dark Night of the Soul",
		"Break into Three", "Finale", "Final Image"},
	"Episodic Structure": {"Episode 1: Introduction", "Episode 2: Rising Action", "Episode 3: Midpoint/Turning Point",
		"Episode 4: Climax Actions", "Episode 5: Resolution/Lead to Next"},
}

// GenderBias presets: label -> (female%, male%).
type BiasPreset struct {
	Female int `json:"female"`
	Male   int `json:"male"`
}

var GenderBiasMap = map[string]BiasPreset{
	"Balanced (50F/50M)":           {50, 50},
	"Slightly Female (60F/40M)":    {60, 40},
	"Mostly Female (75F/25M)":      {75, 25},
	"Primarily Female (90F/10M)":   {90, 10},
	"Slightly Male (40F/60M)":      {40, 60},
	"Mostly Male (25F/75M)":        {25, 75},
	"Primarily Male (10F/90M)":     {10, 90},
	"Exclusively Female (100F/0M)": {100, 0},
	"Exclusively Male (0F/100M)":   {0, 100},
}

// IsGenre reports whether genre exists in the catalogue.
func IsGenre(genre string) bool {
	_, ok := catalogue[genre]
	return ok
}

// IsLength reports whether length is one of the supported options.
func IsLength(length string) bool {
	for _, l := range LengthOptions {
		if l == length {
			return true
		}
	}
	return false
}

// IsValidStructure reports whether structure is valid for the given length.
func IsValidStructure(length, structure string) bool {
	for _, s := range StructureMap[length] {
		if s == structure {
			return true
		}
	}
	return false
}

// Options bundles everything the frontend needs to render the Parameters tab.
type Options struct {
	Genres            []string              `json:"genres"`
	Subgenres         map[string][]string   `json:"subgenres"`
	LengthOptions     []string              `json:"length_options"`
	StructureMap      map[string][]string   `json:"structure_map"`
	DefaultStructure  map[string]string     `json:"default_structure"`
	StructureSections map[string][]string   `json:"structure_sections"`
	GenderBias        map[string]BiasPreset `json:"gender_bias"`
}

func GetOptions() Options {
	subs := map[string][]string{}
	for _, g := range GenreNames() {
		list, _ := SubgenreNames(g)
		subs[g] = list
	}
	return Options{
		Genres:            GenreNames(),
		Subgenres:         subs,
		LengthOptions:     LengthOptions,
		StructureMap:      StructureMap,
		DefaultStructure:  DefaultStructure,
		StructureSections: StructureSections,
		GenderBias:        GenderBiasMap,
	}
}

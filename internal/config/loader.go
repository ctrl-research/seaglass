package config

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed presets/*.yaml
var presetFS embed.FS

// Preset is a named, embedded ruleset shipped with seaglass.
type Preset struct {
	Name    string
	Rules   Ruleset
	rawYAML string
}

// Presets returns the embedded presets by name, sorted.
func Presets() ([]Preset, error) {
	entries, err := presetFS.ReadDir("presets")
	if err != nil {
		return nil, err
	}
	var out []Preset
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		raw, err := presetFS.ReadFile("presets/" + e.Name())
		if err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(e.Name(), ".yaml")
		var rs Ruleset
		if err := yaml.Unmarshal(raw, &rs); err != nil {
			return nil, fmt.Errorf("preset %s: %w", name, err)
		}
		if err := rs.Validate("preset " + name); err != nil {
			return nil, err
		}
		out = append(out, Preset{Name: name, Rules: rs, rawYAML: string(raw)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// PresetYAML returns a preset's raw source, for `seaglass presets show`.
func PresetYAML(name string) (string, error) {
	presets, err := Presets()
	if err != nil {
		return "", err
	}
	for _, p := range presets {
		if p.Name == name {
			return p.rawYAML, nil
		}
	}
	return "", fmt.Errorf("no preset named %q", name)
}

// UserConfigPath returns the path to the user's seaglass.yaml.
func UserConfigPath() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "seaglass", "seaglass.yaml"), nil
}

// UserConfig is the top-level seaglass.yaml. Only the rules are read here;
// other settings (theme, keymap) come later.
type UserConfig struct {
	Ruleset `yaml:",inline"`
	// DisablePresets names presets to skip entirely.
	DisablePresets []string `yaml:"disablePresets,omitempty"`
}

// Load assembles the effective ruleset: every embedded preset (minus any
// the user disabled) followed by the user's own rules, which therefore win
// when engines resolve by name. A missing user file is not an error.
func Load() (Ruleset, error) {
	path, err := UserConfigPath()
	if err != nil {
		return Ruleset{}, err
	}
	return loadFrom(path)
}

func loadFrom(userPath string) (Ruleset, error) {
	var user UserConfig
	if b, err := os.ReadFile(userPath); err == nil {
		if err := yaml.Unmarshal(b, &user); err != nil {
			return Ruleset{}, fmt.Errorf("%s: %w", userPath, err)
		}
		if err := user.Validate(userPath); err != nil {
			return Ruleset{}, err
		}
	} else if !os.IsNotExist(err) {
		return Ruleset{}, fmt.Errorf("%s: %w", userPath, err)
	}

	disabled := map[string]bool{}
	for _, d := range user.DisablePresets {
		disabled[d] = true
	}

	presets, err := Presets()
	if err != nil {
		return Ruleset{}, err
	}
	var out Ruleset
	for _, p := range presets {
		if disabled[p.Name] {
			continue
		}
		out.Merge(p.Rules)
	}
	out.Merge(user.Ruleset)
	return out, nil
}

package app

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Settings is the only state that survives a restart. File metadata is
// deliberately not persisted — see the retention rationale in CLAUDE.md.
type Settings struct {
	Alias            string `json:"alias"`
	BaseDir          string `json:"baseDir,omitempty"`
	RetentionSeconds int    `json:"retentionSeconds"`
	ClearOnShutdown  bool   `json:"clearOnShutdown"`
}

// DefaultRetention is one hour, matching the "use it and leave" intent.
const DefaultRetention = 3600

func DefaultSettings() Settings {
	return Settings{RetentionSeconds: DefaultRetention, ClearOnShutdown: true}
}

func settingsFile() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "justswap", "settings.json"), nil
}

func LoadSettings() Settings {
	s := DefaultSettings()
	p, err := settingsFile()
	if err != nil {
		return s
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return s
	}
	_ = json.Unmarshal(b, &s)
	if s.RetentionSeconds <= 0 {
		s.RetentionSeconds = DefaultRetention
	}
	return s
}

func SaveSettings(s Settings) error {
	p, err := settingsFile()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644)
}

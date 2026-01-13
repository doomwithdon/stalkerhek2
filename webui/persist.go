package webui

import (
    "encoding/json"
    "errors"
    "io"
    "os"
    "path/filepath"
)

var profilesFile = "profiles.json"

func InitProfilesFileFromEnv() {
	if v := os.Getenv("STALKERHEK_PROFILES_FILE"); v != "" {
		profilesFile = v
	}
}

// SaveProfiles writes current profiles to disk
func SaveProfiles() error {
	profMu.RLock()
	snap := make([]Profile, len(profiles))
	copy(snap, profiles)
	profMu.RUnlock()

	if dir := filepath.Dir(profilesFile); dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}

	tmpDir := filepath.Dir(profilesFile)
	if tmpDir == "" || tmpDir == "." {
		tmpDir = "."
	}
	f, err := os.CreateTemp(tmpDir, ".profiles.json.*")
	if err != nil {
		return err
	}
	tmpName := f.Name()
	defer func() {
		_ = f.Close()
		_ = os.Remove(tmpName)
	}()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(snap); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, profilesFile); err != nil {
		return err
	}
	return nil
}

// LoadProfiles loads profiles from disk (if exists)
func LoadProfiles() error {
	if dir := filepath.Dir(profilesFile); dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
    f, err := os.Open(profilesFile)
    if err != nil {
        if os.IsNotExist(err) {
            return nil
        }
        return err
    }
    defer f.Close()
    var arr []Profile
    if err := json.NewDecoder(f).Decode(&arr); err != nil {
        if errors.Is(err, io.EOF) {
            return nil
        }
        return err
    }
    profMu.Lock()
    profiles = arr
    nextProfile = 1
    for _, p := range profiles { if p.ID >= nextProfile { nextProfile = p.ID + 1 } }
    profMu.Unlock()
    return nil
}

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
    defer profMu.RUnlock()
	if dir := filepath.Dir(profilesFile); dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
    f, err := os.Create(profilesFile)
    if err != nil { return err }
    defer f.Close()
    enc := json.NewEncoder(f)
    enc.SetIndent("", "  ")
    return enc.Encode(profiles)
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

package filterstore

import (
	"encoding/json"
	"path/filepath"
	"os"
	"strings"
	"sync"

	"github.com/kidpoleon/stalkerhek/stalker"
)

type ProfileFilters struct {
	DisabledGenres   map[string]bool `json:"disabled_genres"`
	DisabledChannels map[string]bool `json:"disabled_channels"`
	EnabledChannels  map[string]bool `json:"enabled_channels"`
}

func IsGenreDisabled(profileID int, genreID string) bool {
	genreID = strings.TrimSpace(genreID)
	if genreID == "" {
		return false
	}
	s.mu.RLock()
	pf := s.filters[profileID]
	if pf == nil {
		s.mu.RUnlock()
		return false
	}
	v := pf.DisabledGenres[genreID]
	s.mu.RUnlock()
	return v
}

func IsChannelDisabled(profileID int, cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return false
	}
	s.mu.RLock()
	pf := s.filters[profileID]
	if pf == nil {
		s.mu.RUnlock()
		return false
	}
	v := pf.DisabledChannels[cmd]
	s.mu.RUnlock()
	return v
}

type store struct {
	mu       sync.RWMutex
	filters  map[int]*ProfileFilters
	versions map[int]uint64
}

var s = &store{
	filters:  map[int]*ProfileFilters{},
	versions: map[int]uint64{},
}

func ensure(id int) *ProfileFilters {
	pf, ok := s.filters[id]
	if !ok || pf == nil {
		pf = &ProfileFilters{DisabledGenres: map[string]bool{}, DisabledChannels: map[string]bool{}, EnabledChannels: map[string]bool{}}
		s.filters[id] = pf
	}
	if pf.DisabledGenres == nil {
		pf.DisabledGenres = map[string]bool{}
	}
	if pf.DisabledChannels == nil {
		pf.DisabledChannels = map[string]bool{}
	}
	if pf.EnabledChannels == nil {
		pf.EnabledChannels = map[string]bool{}
	}
	return pf
}

func GetVersion(profileID int) uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.versions[profileID]
}

func IsAllowed(profileID int, ch *stalker.Channel) bool {
	if ch == nil {
		return false
	}
	cmd := strings.TrimSpace(ch.CMD)
	genreID := strings.TrimSpace(ch.GenreID)
	if cmd == "" {
		return false
	}

	s.mu.RLock()
	pf := s.filters[profileID]
	if pf == nil {
		s.mu.RUnlock()
		return true
	}
	blockedCmd := pf.DisabledChannels[cmd]
	blockedGenre := false
	if genreID != "" {
		blockedGenre = pf.DisabledGenres[genreID]
	}
	enabledOverride := false
	if pf.EnabledChannels != nil {
		enabledOverride = pf.EnabledChannels[cmd]
	}
	s.mu.RUnlock()
	if blockedCmd {
		return false
	}
	if blockedGenre {
		return enabledOverride
	}
	return true
}

func SetGenreDisabled(profileID int, genreID string, disabled bool) {
	genreID = strings.TrimSpace(genreID)
	s.mu.Lock()
	pf := ensure(profileID)
	if genreID != "" {
		if disabled {
			pf.DisabledGenres[genreID] = true
		} else {
			delete(pf.DisabledGenres, genreID)
		}
	}
	s.versions[profileID]++
	s.mu.Unlock()
}

func SetChannelDisabled(profileID int, cmd string, disabled bool) {
	cmd = strings.TrimSpace(cmd)
	s.mu.Lock()
	pf := ensure(profileID)
	if cmd != "" {
		if disabled {
			pf.DisabledChannels[cmd] = true
			delete(pf.EnabledChannels, cmd)
		} else {
			delete(pf.DisabledChannels, cmd)
			pf.EnabledChannels[cmd] = true
		}
	}
	s.versions[profileID]++
	s.mu.Unlock()
}

func ResetProfile(profileID int) {
	s.mu.Lock()
	pf := ensure(profileID)
	for k := range pf.DisabledGenres {
		delete(pf.DisabledGenres, k)
	}
	for k := range pf.DisabledChannels {
		delete(pf.DisabledChannels, k)
	}
	for k := range pf.EnabledChannels {
		delete(pf.EnabledChannels, k)
	}
	s.versions[profileID]++
	s.mu.Unlock()
}

func DeleteProfile(profileID int) {
	s.mu.Lock()
	delete(s.filters, profileID)
	delete(s.versions, profileID)
	s.mu.Unlock()
}

func Snapshot() map[int]ProfileFilters {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[int]ProfileFilters, len(s.filters))
	for id, pf := range s.filters {
		if pf == nil {
			continue
		}
		cp := ProfileFilters{DisabledGenres: map[string]bool{}, DisabledChannels: map[string]bool{}, EnabledChannels: map[string]bool{}}
		for k, v := range pf.DisabledGenres {
			if v {
				cp.DisabledGenres[k] = true
			}
		}
		for k, v := range pf.DisabledChannels {
			if v {
				cp.DisabledChannels[k] = true
			}
		}
		for k, v := range pf.EnabledChannels {
			if v {
				cp.EnabledChannels[k] = true
			}
		}
		out[id] = cp
	}
	return out
}

func LoadFromFile(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var data map[int]ProfileFilters
	if err := json.Unmarshal(b, &data); err != nil {
		return err
	}
	s.mu.Lock()
	for id, pf := range data {
		cp := pf
		if cp.DisabledGenres == nil {
			cp.DisabledGenres = map[string]bool{}
		}
		if cp.DisabledChannels == nil {
			cp.DisabledChannels = map[string]bool{}
		}
		if cp.EnabledChannels == nil {
			cp.EnabledChannels = map[string]bool{}
		}
		s.filters[id] = &cp
		s.versions[id]++
	}
	s.mu.Unlock()
	return nil
}

func SaveToFile(path string) error {
	data := Snapshot()
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}

	tmpDir := filepath.Dir(path)
	if tmpDir == "" || tmpDir == "." {
		tmpDir = "."
	}
	f, err := os.CreateTemp(tmpDir, ".filters.json.*")
	if err != nil {
		return err
	}
	tmpName := f.Name()
	defer func() {
		_ = f.Close()
		_ = os.Remove(tmpName)
	}()

	if _, err := f.Write(b); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return nil
}

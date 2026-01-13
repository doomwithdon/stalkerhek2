package webui

import (
	"os"
	"path/filepath"

	"github.com/kidpoleon/stalkerhek/filterstore"
)

var filtersFile = "filters.json"

func InitFiltersFileDefault() {
	// If profiles file is /data/profiles.json, default filters to /data/filters.json.
	// Otherwise, keep filters.json in CWD.
	dir := filepath.Dir(profilesFile)
	if dir != "." {
		filtersFile = filepath.Join(dir, "filters.json")
	}
}

func LoadFilters() error {
	InitFiltersFileDefault()
	if dir := filepath.Dir(filtersFile); dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	return filterstore.LoadFromFile(filtersFile)
}

func SaveFilters() error {
	InitFiltersFileDefault()
	if dir := filepath.Dir(filtersFile); dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	return filterstore.SaveToFile(filtersFile)
}

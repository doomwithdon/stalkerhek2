package webui

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/kidpoleon/stalkerhek/hls"
	"github.com/kidpoleon/stalkerhek/proxy"
)

type RuntimeSettings struct {
	PlaylistDelaySegments int `json:"playlist_delay_segments"`
	ResponseHeaderTimeoutSeconds int `json:"response_header_timeout_seconds"`
	MaxIdleConnsPerHost int `json:"max_idle_conns_per_host"`
}

var (
	settingsMu sync.RWMutex
	runtimeSettings = RuntimeSettings{
		PlaylistDelaySegments: 0,
		ResponseHeaderTimeoutSeconds: 15,
		MaxIdleConnsPerHost: 64,
	}
)

func GetRuntimeSettings() RuntimeSettings {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return runtimeSettings
}

func applyRuntimeSettings(s RuntimeSettings) {
	if s.PlaylistDelaySegments < 0 {
		s.PlaylistDelaySegments = 0
	}
	if s.PlaylistDelaySegments > 40 {
		s.PlaylistDelaySegments = 40
	}
	if s.ResponseHeaderTimeoutSeconds < 1 {
		s.ResponseHeaderTimeoutSeconds = 1
	}
	if s.ResponseHeaderTimeoutSeconds > 120 {
		s.ResponseHeaderTimeoutSeconds = 120
	}
	if s.MaxIdleConnsPerHost < 2 {
		s.MaxIdleConnsPerHost = 2
	}
	if s.MaxIdleConnsPerHost > 256 {
		s.MaxIdleConnsPerHost = 256
	}

	settingsMu.Lock()
	runtimeSettings = s
	settingsMu.Unlock()

	hls.SetPlaylistDelaySegments(s.PlaylistDelaySegments)
	hls.UpdateResponseHeaderTimeout(time.Duration(s.ResponseHeaderTimeoutSeconds) * time.Second)
	hls.UpdateMaxIdleConnsPerHost(s.MaxIdleConnsPerHost)

	proxy.UpdateResponseHeaderTimeout(time.Duration(s.ResponseHeaderTimeoutSeconds) * time.Second)
	proxy.UpdateMaxIdleConnsPerHost(s.MaxIdleConnsPerHost)
}

func RegisterSettingsHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/api/settings", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(GetRuntimeSettings())
			return
		case http.MethodPost:
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			cur := GetRuntimeSettings()
			if v := r.FormValue("playlist_delay_segments"); v != "" {
				cur.PlaylistDelaySegments = atoiSafe(v)
			}
			if v := r.FormValue("response_header_timeout_seconds"); v != "" {
				cur.ResponseHeaderTimeoutSeconds = atoiSafe(v)
			}
			if v := r.FormValue("max_idle_conns_per_host"); v != "" {
				cur.MaxIdleConnsPerHost = atoiSafe(v)
			}
			applyRuntimeSettings(cur)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "settings": GetRuntimeSettings()})
			return
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
	})
}

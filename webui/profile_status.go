package webui

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/kidpoleon/stalkerhek/filterstore"
	"github.com/kidpoleon/stalkerhek/stalker"
)

// ProfileStatus represents per-profile status in dashboard
type ProfileStatus struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Phase    string `json:"phase"`
	Message  string `json:"message"`
	Channels int    `json:"channels"`
	TimeZone string `json:"timezone"`
	SampleChannels []string `json:"sample_channels"`
	CategoriesCount int `json:"categories_count"`
	SampleCategories []string `json:"sample_categories"`
	HLS      string `json:"hls"`
	Proxy    string `json:"proxy"`
	Running  bool   `json:"running"`
	Busy     bool   `json:"busy"`
}

var (
	psMu   sync.RWMutex
	pstate = map[int]ProfileStatus{}
)

// GetProfileStatus returns the current status for a profile ID
func GetProfileStatus(id int) ProfileStatus {
	psMu.RLock()
	defer psMu.RUnlock()
	s, ok := pstate[id]
	if !ok {
		return ProfileStatus{ID: id, Name: "", Phase: "idle", Message: "Unknown", Running: false}
	}
	return s
}

func SetProfileValidating(id int, name, msg string) {
	psMu.Lock()
	s := pstate[id]
	s.ID, s.Name, s.Phase, s.Message = id, name, "validating", msg
	pstate[id] = s
	psMu.Unlock()
}

func SetProfileError(id int, name, msg string) {
	psMu.Lock()
	s := pstate[id]
	s.ID, s.Name, s.Phase, s.Message, s.Running = id, name, "error", msg, false
	pstate[id] = s
	psMu.Unlock()
}

func SetProfileSuccess(id int, name string, channels int, hls, proxy string, running bool) {
	psMu.Lock()
	msg := "Ready"
	if !running {
		msg = "Verified"
	}
	prev := pstate[id]
	prev.ID = id
	prev.Name = name
	prev.Phase = "success"
	prev.Message = msg
	prev.Channels = channels
	prev.HLS = hls
	prev.Proxy = proxy
	prev.Running = running
	pstate[id] = prev
	psMu.Unlock()
}

func SetProfileSummary(id int, tz string, sampleChannels []string, categoriesCount int, sampleCategories []string) {
	psMu.Lock()
	s := pstate[id]
	s.ID = id
	s.TimeZone = tz
	s.SampleChannels = sampleChannels
	s.CategoriesCount = categoriesCount
	s.SampleCategories = sampleCategories
	pstate[id] = s
	psMu.Unlock()
}

func SetProfileStopped(id int) {
	psMu.Lock()
	s := pstate[id]
	s.Phase, s.Message, s.Running = "idle", "Stopped", false
	pstate[id] = s
	psMu.Unlock()
}

// RegisterProfileStatusHandlers mounts /api/profile_status for polling
func RegisterProfileStatusHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/api/profile_status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		psMu.RLock()
		arr := make([]ProfileStatus, 0, len(pstate))
		for _, v := range pstate {
			arr = append(arr, v)
		}
		psMu.RUnlock()
		// annotate with in-flight start state (read startBusy under startMu)
		for i := range arr {
			startMu.Lock()
			arr[i].Busy = startBusy[arr[i].ID]
			startMu.Unlock()
		}
		sort.Slice(arr, func(i, j int) bool { return arr[i].ID < arr[j].ID })
		_ = json.NewEncoder(w).Encode(arr)
	})

	// JSON stop endpoint for AJAX calls (no redirects): POST /api/profiles/stop
	mux.HandleFunc("/api/profiles/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		idStr := r.FormValue("id")
		if idStr == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		id := atoiSafe(idStr)
		startMu.Lock()
		stopRequested[id] = true
		startMu.Unlock()
		AppendProfileLog(id, "Stop requested (user)")
		_ = StopRunner(id)
		SetProfileStopped(id)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})

	// stop endpoint: POST /profiles/{id}/stop
	mux.HandleFunc("/profiles/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		idStr := r.FormValue("id")
		if idStr == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		id := atoiSafe(idStr)
		startMu.Lock()
		stopRequested[id] = true
		startMu.Unlock()
		AppendProfileLog(id, "Stop requested (user)")
		_ = StopRunner(id)
		SetProfileStopped(id)
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
	})

	// delete endpoint: POST /profiles/{id}/delete
	mux.HandleFunc("/profiles/delete", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		id := atoiSafe(r.FormValue("id"))
		startMu.Lock()
		stopRequested[id] = true
		startMu.Unlock()
		AppendProfileLog(id, "Profile deleted")
		ClearProfileChannels(id)
		filterstore.DeleteProfile(id)
		_ = SaveFilters()
		DeleteProfile(id)
		_ = SaveProfiles()
		SetProfileStopped(id)
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
	})

	// start endpoint: POST /profiles/start
	mux.HandleFunc("/profiles/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		id := atoiSafe(r.FormValue("id"))
		p, ok := GetProfile(id)
		if !ok {
			http.Error(w, "profile not found", http.StatusNotFound)
			return
		}
		// idempotent: restart if already running
		_ = StopRunner(p.ID)
		AppendProfileLog(p.ID, "Start requested (user)")
		SetProfileValidating(p.ID, p.Name, "Starting...")
		go StartProfileServices(p)
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
	})

	// JSON start endpoint for AJAX calls (no redirects): POST /api/profiles/start
	mux.HandleFunc("/api/profiles/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		idStr := r.FormValue("id")
		if idStr == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		id := atoiSafe(idStr)
		p, ok := GetProfile(id)
		if !ok {
			http.Error(w, "profile not found", http.StatusNotFound)
			return
		}
		_ = StopRunner(p.ID)
		AppendProfileLog(p.ID, "Start requested (user)")
		SetProfileValidating(p.ID, p.Name, "Starting...")
		go StartProfileServices(p)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
}

// helper: safe atoi
func atoiSafe(s string) int { n := 0; for _, c := range s { if c < '0' || c > '9' { break }; n = n*10 + int(c-'0') }; return n }

func itoa(n int) string { return strconv.Itoa(n) }

// linkForHost composes an http://host:port/ link given a raw host header
func linkForHost(raw string, port int) string {
	host := raw
	if i := strings.Index(host, ":"); i > -1 {
		host = host[:i]
	}
	return "http://" + host + ":" + itoa(port) + "/"
}

// DefaultPortal returns a baseline Portal config used for verification
func DefaultPortal() *stalker.Portal {
return &stalker.Portal{
Model:        "MAG254",
SerialNumber: "0000000000000",
DeviceID:     strings.Repeat("f", 64),
DeviceID2:    strings.Repeat("f", 64),
Signature:    strings.Repeat("f", 64),
TimeZone:     "UTC",
DeviceIdAuth: true,
WatchDogTime: 5,
}
}

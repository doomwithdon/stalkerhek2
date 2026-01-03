package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/url"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kidpoleon/stalkerhek/hls"
	"github.com/kidpoleon/stalkerhek/proxy"
	"github.com/kidpoleon/stalkerhek/stalker"
)

// Profile represents a user-defined configuration profile
// containing portal credentials and per-profile service ports.
type Profile struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	PortalURL string `json:"portal_url"`
	MAC       string `json:"mac"`
	HlsPort   int    `json:"hls_port"`
	ProxyPort int    `json:"proxy_port"`
}

func externalHost(r *http.Request) string {
	host := r.Host
	if xf := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); xf != "" {
		host = strings.TrimSpace(strings.Split(xf, ",")[0])
	}
	if fwd := strings.TrimSpace(r.Header.Get("Forwarded")); fwd != "" {
		fwd = strings.TrimSpace(strings.Split(fwd, ",")[0])
		parts := strings.Split(fwd, ";")
		for _, p := range parts {
			kv := strings.SplitN(strings.TrimSpace(p), "=", 2)
			if len(kv) != 2 {
				continue
			}
			k := strings.ToLower(strings.TrimSpace(kv[0]))
			v := strings.Trim(strings.TrimSpace(kv[1]), "\"")
			if k == "host" && v != "" {
				host = v
			}
		}
	}
	if i := strings.Index(host, ":"); i > -1 {
		host = host[:i]
	}
	return host
}

var (
	profMu      sync.RWMutex
	profiles    = make([]Profile, 0, 8)
	nextProfile = 1
	startMu     sync.Mutex
	startBusy   = map[int]bool{}
	stopRequested = map[int]bool{}
)

const defaultPortalURL = "http://<HOST>/portal.php"

// StartProfileServices launches authentication, channel retrieval, and HLS/Proxy services for a single profile in its own goroutine.
func StartProfileServices(p Profile) {
	startMu.Lock()
	if startBusy[p.ID] {
		startMu.Unlock()
		AppendProfileLog(p.ID, "Start ignored (already starting)")
		return
	}
	startBusy[p.ID] = true
	stopRequested[p.ID] = false
	startMu.Unlock()
	defer func() {
		startMu.Lock()
		delete(startBusy, p.ID)
		startMu.Unlock()
	}()
	shouldStop := func(stage string) bool {
		startMu.Lock()
		sr := stopRequested[p.ID]
		if sr {
			stopRequested[p.ID] = false
		}
		startMu.Unlock()
		if sr {
			AppendProfileLog(p.ID, "Stop requested; aborting ("+stage+")")
			SetProfileStopped(p.ID)
			return true
		}
		return false
	}

	log.Printf("[PROFILE %s] Starting services...", p.Name)
	AppendProfileLog(p.ID, "Starting services")
	SetProfileValidating(p.ID, p.Name, "Connecting... (attempt 1/3)")
	AppendProfileLog(p.ID, "Connecting to portal")
	// Build per-profile config
	cfg := &stalker.Config{
		Portal: &stalker.Portal{
			Model:        "MAG254",
			SerialNumber: "0000000000000",
			DeviceID:     strings.Repeat("f", 64),
			DeviceID2:    strings.Repeat("f", 64),
			Signature:    strings.Repeat("f", 64),
			TimeZone:     "UTC",
			DeviceIdAuth: true,
			WatchDogTime: 5,
			Location:     p.PortalURL,
			MAC:          p.MAC,
		},
		HLS: struct {
			Enabled bool   `yaml:"enabled"`
			Bind    string `yaml:"bind"`
		}{Enabled: true, Bind: fmt.Sprintf("0.0.0.0:%d", p.HlsPort)},
		Proxy: struct {
			Enabled bool   `yaml:"enabled"`
			Bind    string `yaml:"bind"`
			Rewrite bool   `yaml:"rewrite"`
		}{Enabled: true, Bind: fmt.Sprintf("0.0.0.0:%d", p.ProxyPort), Rewrite: true},
	}
	// Authenticate (soft timeout: 3 tries)
	{
		const maxAttempts = 3
		const perAttemptTimeout = 20 * time.Second
		var lastErr error
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			if shouldStop("before authentication") {
				return
			}
			SetProfileValidating(p.ID, p.Name, fmt.Sprintf("Connecting... (attempt %d/%d)", attempt, maxAttempts))
			AppendProfileLog(p.ID, fmt.Sprintf("Auth attempt %d/%d", attempt, maxAttempts))
			errCh := make(chan error, 1)
			go func() { errCh <- cfg.Portal.Start() }()
			select {
			case err := <-errCh:
				if err == nil {
					lastErr = nil
					attempt = maxAttempts
					break
				}
				lastErr = err
				AppendProfileLog(p.ID, "Authentication failed: "+err.Error())
			case <-time.After(perAttemptTimeout):
				lastErr = fmt.Errorf("authentication timed out after %s", perAttemptTimeout)
				AppendProfileLog(p.ID, lastErr.Error())
			}
			if lastErr == nil {
				break
			}
			if shouldStop("after authentication attempt") {
				return
			}
			if attempt < maxAttempts {
				time.Sleep(time.Duration(attempt) * time.Second)
			}
		}
		if lastErr != nil {
			SetProfileError(p.ID, p.Name, lastErr.Error())
			log.Printf("[PROFILE %s] Authentication failed: %v", p.Name, lastErr)
			return
		}
	}
	if shouldStop("after authentication") {
		return
	}
	SetProfileValidating(p.ID, p.Name, "Retrieving channels...")
	AppendProfileLog(p.ID, "Authentication OK")
	AppendProfileLog(p.ID, "Retrieving channels")
	// Retrieve channels (soft timeout: 3 tries)
	chs := map[string]*stalker.Channel{}
	{
		const maxAttempts = 3
		const perAttemptTimeout = 30 * time.Second
		var lastErr error
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			if shouldStop("before channel retrieval") {
				return
			}
			SetProfileValidating(p.ID, p.Name, fmt.Sprintf("Retrieving channels... (attempt %d/%d)", attempt, maxAttempts))
			AppendProfileLog(p.ID, fmt.Sprintf("Retrieve channels attempt %d/%d", attempt, maxAttempts))
			type result struct {
				chs map[string]*stalker.Channel
				err error
			}
			resCh := make(chan result, 1)
			go func() {
				c, err := cfg.Portal.RetrieveChannels()
				resCh <- result{chs: c, err: err}
			}()
			select {
			case res := <-resCh:
				if res.err == nil {
					chs = res.chs
					lastErr = nil
					attempt = maxAttempts
					break
				}
				lastErr = res.err
				AppendProfileLog(p.ID, "Channel retrieval failed: "+res.err.Error())
			case <-time.After(perAttemptTimeout):
				lastErr = fmt.Errorf("channel retrieval timed out after %s", perAttemptTimeout)
				AppendProfileLog(p.ID, lastErr.Error())
			}
			if lastErr == nil {
				break
			}
			if shouldStop("after channel retrieval attempt") {
				return
			}
			if attempt < maxAttempts {
				time.Sleep(time.Duration(attempt) * time.Second)
			}
		}
		if lastErr != nil {
			SetProfileError(p.ID, p.Name, lastErr.Error())
			log.Printf("[PROFILE %s] Channel retrieval failed: %v", p.Name, lastErr)
			return
		}
	}
	if shouldStop("after channel retrieval") {
		return
	}
	if len(chs) == 0 {
		AppendProfileLog(p.ID, "No IPTV channels retrieved")
		SetProfileError(p.ID, p.Name, "no IPTV channels retrieved")
		log.Printf("[PROFILE %s] No channels retrieved", p.Name)
		return
	}
	SetProfileSuccess(p.ID, p.Name, len(chs), "", "", true)
	AppendProfileLog(p.ID, fmt.Sprintf("Retrieved %d channels", len(chs)))
	if shouldStop("before starting services") {
		return
	}

	// Summary for Fetch step widget.
	// Keep it light and robust: best-effort sampling.
	{
		// sample channel names
		names := make([]string, 0, len(chs))
		catSet := map[string]struct{}{}
		for k, ch := range chs {
			nm := strings.TrimSpace(k)
			if nm == "" && ch != nil {
				nm = strings.TrimSpace(ch.Title)
			}
			if nm != "" {
				names = append(names, nm)
			}
			if ch != nil {
				g := strings.TrimSpace(ch.Genre())
				if g != "" {
					catSet[g] = struct{}{}
				}
			}
		}
		sort.Strings(names)
		sampleChannels := names
		if len(sampleChannels) > 5 {
			sampleChannels = sampleChannels[:5]
		}
		cats := make([]string, 0, len(catSet))
		for c := range catSet {
			cats = append(cats, c)
		}
		sort.Strings(cats)
		sampleCats := cats
		if len(sampleCats) > 6 {
			sampleCats = sampleCats[:6]
		}
		SetProfileSummary(p.ID, cfg.Portal.TimeZone, sampleChannels, len(catSet), sampleCats)
	}
	log.Printf("[PROFILE %s] Retrieved %d channels", p.Name, len(chs))

	// Create per-profile context
	pCtx, pCancel := context.WithCancel(context.Background())
	RegisterRunner(p.ID, pCancel)

	// Start HLS
	go func(channels map[string]*stalker.Channel) {
		log.Printf("[PROFILE %s] Starting HLS service on %s", p.Name, cfg.HLS.Bind)
		AppendProfileLog(p.ID, "Starting HLS on "+cfg.HLS.Bind)
		hls.StartWithContext(pCtx, channels, cfg.HLS.Bind)
		log.Printf("[PROFILE %s] HLS service stopped on %s", p.Name, cfg.HLS.Bind)
		AppendProfileLog(p.ID, "HLS stopped")
	}(chs)

	// Start Proxy
	go func(channels map[string]*stalker.Channel) {
		log.Printf("[PROFILE %s] Starting proxy service on %s", p.Name, cfg.Proxy.Bind)
		AppendProfileLog(p.ID, "Starting Proxy on "+cfg.Proxy.Bind)
		proxy.StartWithContext(pCtx, cfg, channels)
		log.Printf("[PROFILE %s] Proxy service stopped on %s", p.Name, cfg.Proxy.Bind)
		AppendProfileLog(p.ID, "Proxy stopped")
	}(chs)
}

func normalizePortalURL(in string) string {
	s := strings.TrimSpace(in)
	if s == "" {
		return ""
	}
	if !strings.HasPrefix(strings.ToLower(s), "http://") && !strings.HasPrefix(strings.ToLower(s), "https://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u == nil {
		return s
	}

	path := strings.TrimSpace(u.Path)
	if path == "" || path == "/" {
		path = "/portal.php"
	}
	lower := strings.ToLower(path)
	// Force compatibility: /portal.php yields best results.
	if strings.HasSuffix(lower, "/load.php") {
		path = strings.TrimSuffix(path, "/load.php") + "/portal.php"
		lower = strings.ToLower(path)
	}
	// If user pasted some other php endpoint, override to the canonical one.
	if strings.HasSuffix(lower, ".php") && !strings.HasSuffix(lower, "/portal.php") {
		path = "/portal.php"
		lower = strings.ToLower(path)
	}
	// If user pasted a directory path, append portal.php.
	if !strings.HasSuffix(lower, "/portal.php") {
		if strings.HasSuffix(path, "/") {
			path = path + "portal.php"
		} else {
			path = strings.TrimRight(path, "/") + "/portal.php"
		}
	}
	u.Path = path
	return u.String()
}

// AddProfile appends a new profile to memory and returns it.
func AddProfile(p Profile) Profile {
	profMu.Lock()
	defer profMu.Unlock()
	p.ID = nextProfile
	nextProfile++
	profiles = append(profiles, p)
	return p
}

// ListProfiles returns a copy of current profiles.
func ListProfiles() []Profile {
	profMu.RLock()
	defer profMu.RUnlock()
	out := make([]Profile, len(profiles))
	copy(out, profiles)
	return out
}

// RegisterProfileHandlers mounts profile CRUD and control endpoints.
func RegisterProfileHandlers(mux *http.ServeMux, onStart func()) {
	mux.HandleFunc("/api/profiles", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ListProfiles())
	})

	// Basic create endpoint supporting form-encoded submissions
	mux.HandleFunc("/profiles", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		editIDStr := strings.TrimSpace(r.FormValue("edit_id"))
		name := strings.TrimSpace(r.FormValue("name"))
		portal := normalizePortalURL(r.FormValue("portal"))
		if portal == "" {
			portal = defaultPortalURL
		}
		mac := strings.ToUpper(strings.TrimSpace(r.FormValue("mac")))
		hlsStr := strings.TrimSpace(r.FormValue("hls_port"))
		proxyStr := strings.TrimSpace(r.FormValue("proxy_port"))
		if portal == "" || mac == "" || hlsStr == "" || proxyStr == "" {
			http.Error(w, "portal, mac, hls_port, proxy_port are required", http.StatusBadRequest)
			return
		}
		hlsPort, err1 := strconv.Atoi(hlsStr)
		proxyPort, err2 := strconv.Atoi(proxyStr)
		if err1 != nil || err2 != nil || hlsPort <= 0 || proxyPort <= 0 {
			http.Error(w, "invalid ports", http.StatusBadRequest)
			return
		}
		// Update existing profile if edit_id was provided
		if editIDStr != "" {
			id := atoiSafe(editIDStr)
			// stop running services (if any) before updating
			_ = StopRunner(id)
			SetProfileStopped(id)
			updated := false
			profMu.Lock()
			for i := range profiles {
				if profiles[i].ID == id {
					profiles[i].Name = name
					profiles[i].PortalURL = portal
					profiles[i].MAC = mac
					profiles[i].HlsPort = hlsPort
					profiles[i].ProxyPort = proxyPort
					updated = true
					break
				}
			}
			profMu.Unlock()
			if !updated {
				http.Error(w, "profile not found", http.StatusNotFound)
				return
			}
			_ = SaveProfiles()
			p, _ := GetProfile(id)
			go StartProfileServices(p)
			http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
			return
		}

		p := AddProfile(Profile{
			Name:      name,
			PortalURL: portal,
			MAC:       mac,
			HlsPort:   hlsPort,
			ProxyPort: proxyPort,
		})
		_ = SaveProfiles()
		// Immediately start services for this profile in a goroutine
		go StartProfileServices(p)
		// redirect back to dashboard
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		_ = p
	})

	// Start signal: when invoked, the outer caller can proceed to start services.
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if len(ListProfiles()) == 0 {
			http.Error(w, "no profiles defined", http.StatusBadRequest)
			return
		}
		if onStart != nil {
			onStart()
		}
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
	})

	// Minimal dashboard HTML if not provided by status.go
	mux.HandleFunc("/dashboard", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		host := externalHost(r)
		data := struct {
			Host     string
			Settings RuntimeSettings
			Profiles []Profile
		}{Host: host, Settings: GetRuntimeSettings(), Profiles: ListProfiles()}

		const tpl = `<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <link rel="icon" href="https://i.ibb.co/MyxmyVzz/STALKERHEK-LOGO-1500x1500.png">
  <link rel="stylesheet" href="https://cdnjs.cloudflare.com/ajax/libs/font-awesome/6.5.1/css/all.min.css" referrerpolicy="no-referrer" />
  <title>Stalkerhek Dashboard</title>
  <style>
    :root{--bg:#0a0f0a;--panel:#0d1410;--panel2:#111815;--border:#1f2e23;--text:#e0e6e0;--muted:#9aaa9a;--brand:#2d7a4e;--brand-hover:#3a8f5e;--ok:#3fb970;--warn:#d4a94a;--bad:#e85d4d}
    *{box-sizing:border-box}
    body{margin:0;font-family:system-ui,-apple-system,Segoe UI,Roboto,Ubuntu,Helvetica,Arial,sans-serif;background:linear-gradient(180deg, #0d1410 0%, #0a0f0a 100%);color:var(--text);min-height:100dvh}
    a{color:var(--brand);text-decoration:none} a:hover{color:var(--brand-hover);text-decoration:underline}
    .wrap{max-width:1200px;margin:0 auto;
      padding-top:calc(clamp(22px, 4.2vw, 40px) + env(safe-area-inset-top));
      padding-left:calc(clamp(20px, 4vw, 36px) + env(safe-area-inset-left));
      padding-right:calc(clamp(20px, 4vw, 36px) + env(safe-area-inset-right));
      padding-bottom:calc(130px + env(safe-area-inset-bottom));
      min-height:100dvh;display:flex;flex-direction:column;gap:14px}
    .topbar{display:flex;align-items:center;justify-content:center;gap:12px;flex-wrap:wrap;margin-bottom:6px}
    .banner{width:100%;max-width:1200px;border-radius:18px;border:1px solid var(--border);background:rgba(13,20,16,.55);box-shadow:0 18px 48px rgba(0,0,0,.42);overflow:hidden;height:clamp(72px, 13vw, 132px)}
    .banner img{width:100%;height:100%;display:block;object-fit:cover;object-position:center}
    .title{display:none}
    h1{margin:0;font-size:28px;letter-spacing:.1px;color:var(--text)}
    .sub{color:#c4d4c4;font-size:15px;line-height:1.45}
    .pill{display:inline-flex;align-items:center;gap:10px;padding:10px 14px;border:1px solid var(--border);border-radius:999px;background:rgba(31,46,35,.55);color:var(--muted);font-size:13px}
    .bottompills{position:fixed;left:50%;bottom:16px;transform:translateX(-50%);z-index:10;max-width:calc(1200px - 32px);width:calc(100% - 32px);display:flex;justify-content:center;pointer-events:none}
    .bottompills .pillrow{pointer-events:auto;display:flex;gap:10px;flex-wrap:wrap;justify-content:center}
    .bottompills .pill{background:rgba(13,20,16,.88);backdrop-filter:blur(10px);box-shadow:0 14px 40px rgba(0,0,0,.38)}
    a.pilllink{color:var(--muted);text-decoration:none}
    a.pilllink:hover{color:var(--text);text-decoration:none;border-color:rgba(45,122,78,.55);background:rgba(13,20,16,.92)}
    .grid{display:grid;grid-template-columns:1fr;gap:16px;flex:1 1 auto}
    @media(min-width:900px){.grid{grid-template-columns: 1fr}}
    .card{background:linear-gradient(180deg, rgba(17,24,21,.96), rgba(13,20,16,.94));border:1px solid var(--border);border-radius:18px;padding:24px;box-shadow:0 12px 32px rgba(0,0,0,.4)}
    .card h2{margin:0 0 12px 0;font-size:18px;color:var(--text)}
    .step{display:flex;gap:12px;align-items:flex-start;margin:12px 0}
    .num{display:none}
    .step p{margin:0;color:var(--muted);font-size:14px;line-height:1.4}
    label{display:block;font-size:13px;color:#c5d1c5;margin:12px 0 6px}
    .hint{font-size:13px;color:var(--muted);margin-top:8px;line-height:1.4}
    .row{display:grid;grid-template-columns:1fr;gap:12px}
    @media(min-width:520px){.row.two{grid-template-columns:1fr 1fr}}
    input{width:100%;padding:14px 14px;border-radius:12px;border:1px solid var(--border);background:#0f1612;color:var(--text);outline:none;font-size:16px;transition:border-color .2s,box-shadow .2s}
    input:focus{border-color:var(--brand);box-shadow:0 0 0 3px rgba(45,122,78,.2)}
    .err{display:none;margin-top:6px;color:var(--bad);font-size:12px}
    .btnbar{display:flex;gap:12px;flex-wrap:wrap;margin-top:16px}
    button{cursor:pointer;border:1px solid var(--border);border-radius:12px;padding:14px 16px;font-size:15px;font-weight:650;transition:background .2s,filter .2s, transform .18s ease, border-color .2s ease;display:inline-flex;align-items:center;gap:10px;background:rgba(13,20,16,.35);color:var(--text)}
    button:active{transform:translateY(1px)}
    button:focus-visible, a.ghost:focus-visible, a.ok:focus-visible, a.edit:focus-visible, a.danger:focus-visible{outline:none;box-shadow:0 0 0 3px rgba(45,122,78,.22)}
    .primary:hover{background:rgba(45,122,78,.16);border-color:rgba(45,122,78,.65)}
    .ghost{background:rgba(13,20,16,.25)}
    .ghost:hover{background:rgba(45,122,78,.12);border-color:rgba(45,122,78,.55)}
    a.ghost{display:inline-flex;align-items:center;justify-content:center;padding:12px 14px;border-radius:12px;font-size:13px;font-weight:700;gap:10px}
    .danger:hover{background:rgba(232,93,77,.14);border-color:rgba(232,93,77,.45)}
    .ok:hover{background:rgba(63,185,112,.14);border-color:rgba(63,185,112,.35)}
    .edit:hover{background:rgba(212,169,74,.12);border-color:rgba(212,169,74,.38)}
    .profiles{display:grid;grid-template-columns:1fr;gap:14px}
    .p{padding:16px;border-radius:16px;border:1px solid var(--border);background:rgba(13,20,16,.82);transition:transform .18s ease,border-color .18s ease,box-shadow .18s ease}
    .p:hover{transform:translateY(-1px);border-color:rgba(45,122,78,.55);box-shadow:0 14px 30px rgba(0,0,0,.25)}
    .phead{display:flex;justify-content:space-between;gap:12px;flex-wrap:wrap;align-items:center}
    .pname{font-weight:800;color:var(--text)}
    .badg{font-size:12px;padding:6px 10px;border-radius:999px;border:1px solid var(--border);color:var(--muted)}
    .badg.ok{border-color:rgba(63,185,112,.3);color:#bfffd3}
    .badg.err{border-color:rgba(232,93,77,.35);color:#ffd0d0}
    .badg.run{border-color:rgba(45,122,78,.35);color:#d7e6ff}
    .meta{margin-top:12px;color:var(--muted);font-size:13px;line-height:1.4;display:grid;gap:6px}
    .links{display:none}
    .actions{display:flex;gap:10px;flex-wrap:wrap;margin-top:14px;align-items:center}
    .actions .spacer{flex:1 1 auto}
    .linkbtn{display:inline-flex;align-items:center;gap:10px;text-decoration:none}
    .linkbtn:hover{text-decoration:none}
    .btntext{display:inline}

    @media (max-width: 560px){
      .wdesc{display:none}
      button{padding:11px 11px}
      a.ghost{padding:12px 12px}
      .btntext{display:none}
      .p{padding:14px}
      .actions{gap:8px}
      .proxybtn{display:none}
    }
    .footnote{margin-top:12px;color:var(--muted);font-size:12px;line-height:1.4}
    .toast{position:sticky;top:12px;z-index:20;max-width:1200px;margin:0 auto 12px auto;background:rgba(13,20,16,.92);border:1px solid var(--border);border-radius:14px;padding:12px 14px;display:none;box-shadow:0 16px 40px rgba(0,0,0,.35);backdrop-filter: blur(6px)}
    .toast strong{display:block;margin-bottom:4px}
    .toast .small{color:var(--muted);font-size:12px;margin-top:2px}

    /* Wizard stepper */
    [x-cloak]{display:none !important}
    .wizard{display:flex;gap:12px;flex-wrap:wrap;align-items:center;justify-content:center;margin:12px 0 18px 0}
    .wstep{display:flex;align-items:center;gap:10px;padding:14px 18px;border-radius:16px;border:1px solid var(--border);background:rgba(13,20,16,.55);color:#cfe0cf;cursor:pointer;user-select:none;transition:transform .18s ease, border-color .18s ease, background .18s ease;font-size:16px}
    .wstep:hover{transform:translateY(-1px);border-color:rgba(45,122,78,.6);background:rgba(13,20,16,.75)}
    .wstep.active{border-color:rgba(45,122,78,.75);box-shadow:0 0 0 3px rgba(45,122,78,.16) inset}
    .wbadge{display:none}
    .wmeta{display:flex;flex-direction:column;gap:1px}
    .wtitle{font-weight:800;color:#e6f2e6}
    .wdesc{font-size:12px;color:var(--muted)}

    /* Panels + subtle pattern backgrounds */
    .pane{position:relative;overflow:hidden}
    .pane::before{content:"";position:absolute;inset:0;pointer-events:none;opacity:.38}
    .pane.create::before{
      background-image:
        linear-gradient(to right, rgba(255,255,255,.035) 1px, transparent 1px),
        linear-gradient(to bottom, rgba(255,255,255,.03) 1px, transparent 1px);
      background-size: 44px 44px;
    }
    .pane.manage::before{
      background-image:
        radial-gradient(circle, rgba(255,255,255,.07) 1.7px, transparent 1.9px);
      background-size: 16px 16px;
      opacity:.55;
    }
	.pane.advanced::before{
	  background-image:
		repeating-linear-gradient(135deg, rgba(255,255,255,.05) 0px, rgba(255,255,255,.05) 1px, transparent 1px, transparent 12px),
		repeating-linear-gradient(45deg, rgba(255,255,255,.035) 0px, rgba(255,255,255,.035) 1px, transparent 1px, transparent 14px);
	  background-size: 180px 180px;
	  opacity:.28;
	}
    .pane > *{position:relative}

    .fade{transition:opacity .18s ease, transform .18s ease}
    .fadeIn{opacity:1;transform:none}
    .fadeOut{opacity:0;transform:translateY(6px)}

    .logbox{border:1px solid var(--border);border-radius:14px;background:rgba(15,22,18,.7);padding:12px;min-height:180px;max-height:360px;overflow:auto;scrollbar-gutter:stable}
    .logline{font-family:ui-monospace,SFMono-Regular,Menlo,Monaco,Consolas,"Liberation Mono","Courier New",monospace;font-size:12.5px;color:#d6e4d6;line-height:1.45;padding:6px 8px;border-bottom:1px dashed rgba(31,46,35,.65)}
    .logline:last-child{border-bottom:none}
    .kpi{display:grid;grid-template-columns:repeat(auto-fit, minmax(180px, 1fr));gap:10px;margin-top:12px}
    .kpi .stat{background:rgba(31,46,35,.25);border:1px solid var(--border);border-radius:12px;padding:10px 12px}
    .kpi .stat .lbl{color:var(--muted);font-size:12px;text-transform:uppercase;letter-spacing:.5px}
    .kpi .stat .val{color:#e6f2e6;font-weight:850;margin-top:4px;font-size:18px}

    /* Advanced pane layout */
    .advgrid{display:grid;grid-template-columns:repeat(auto-fit,minmax(240px,1fr));gap:12px;margin-top:12px;max-width:980px;margin-left:auto;margin-right:auto}
    .advgrid .stat{background:rgba(31,46,35,.18);border:1px solid var(--border);border-radius:12px;padding:12px}
    .advgrid .stat .lbl{color:var(--muted);font-size:12px;text-transform:uppercase;letter-spacing:.5px}
    .advgrid .stat input{margin-top:8px}
    .advactions{display:flex;justify-content:center;margin-top:14px}
  </style>
</head>
<body x-data="wizard()" x-init="init()" x-cloak>
  <!-- Alpine.js (CDN). Keeping it minimal avoids a Node build pipeline and stays stable for Go projects. -->
  <script defer src="https://unpkg.com/alpinejs@3/dist/cdn.min.js"></script>

  <div class="wrap">
    <div id="toast" class="toast"><strong id="toastTitle"></strong><div id="toastMsg"></div><div class="small" id="toastSmall"></div></div>

    <div class="topbar">
      <div class="banner" aria-label="Stalkerhek">
        <img src="https://i.ibb.co/b53gtx6G/STALKERHEK-BANNER-3840x2160.png" alt="Stalkerhek" />
      </div>
    </div>

    <!-- Wizard stepper (reactive single-page flow) -->
    <div class="wizard" role="navigation" aria-label="Wizard">
      <div class="wstep" :class="step==='create' ? 'active' : ''" @click="go('create')" title="Create or edit profiles">
        <i class="fa-solid fa-plus"></i>
        <div class="wmeta"><div class="wtitle">Create</div><div class="wdesc">Add / Edit credentials</div></div>
      </div>
      <div class="wstep" :class="step==='manage' ? 'active' : ''" @click="go('manage')" title="Manage profiles">
		<i class="fa-solid fa-layer-group"></i>
        <div class="wmeta"><div class="wtitle">Manage</div><div class="wdesc">Start / Stop / Links</div></div>
      </div>
	  <div class="wstep" :class="step==='advanced' ? 'active' : ''" @click="go('advanced')" title="Advanced settings">
		<i class="fa-solid fa-sliders"></i>
		<div class="wmeta"><div class="wtitle">Advanced</div><div class="wdesc">Stability / Tuning</div></div>
	  </div>
    </div>

    <div class="grid">
      <!-- Create step -->
      <div class="card pane create" x-show="step==='create'" x-transition.opacity.duration.180ms>
        <h2>Create / Edit Profile</h2>
        <div class="step"><div class="num">1</div><p><b>Portal URL</b>: paste what your provider gave you. We'll fix it automatically if needed.</p></div>
        <div class="step"><div class="num">2</div><p><b>MAC</b>: copy/paste your MAC address (uppercase with colons).</p></div>
        <div class="step"><div class="num">3</div><p><b>Ports</b>: choose free ports (different for each profile).</p></div>

        <!--
          Developer note:
          This form supports both Create and Edit. When edit_id is set (by the Edit button), the server updates that profile,
          stops old services, and restarts with the new credentials.
        -->
        <form id="addForm" method="post" action="/profiles" novalidate @submit="onCreateSubmit()">
          <input type="hidden" id="edit_id" name="edit_id" value="" />
          <label for="name">Profile name (optional)</label>
          <input id="name" name="name" placeholder="Living Room / Office / Backup" title="Optional: give it a name so you can recognize it" />

          <label for="portal">Portal URL (required)</label>
          <input id="portal" name="portal" required placeholder="http://example.com/portal.php" title="Paste your portal URL here" />
          <div id="portalErr" class="err">Please paste a valid portal link. We'll try to fix it automatically.</div>

          <label for="mac">MAC address (required)</label>
          <input id="mac" name="mac" required placeholder="00:1A:79:12:34:56" title="Example format: 00:1A:79:12:34:56" />
          <div id="macErr" class="err">MAC must look like <b>00:1A:79:12:34:56</b>.</div>

          <div class="row two">
            <div>
              <label for="hls_port">HLS Port</label>
              <input id="hls_port" name="hls_port" required inputmode="numeric" title="This is the port your HLS players will use" />
            </div>
            <div>
              <label for="proxy_port">Proxy Port</label>
              <input id="proxy_port" name="proxy_port" required inputmode="numeric" title="This is used by STB-style apps (optional for most users)" />
            </div>
          </div>

          <div class="btnbar">
            <button class="primary" id="saveBtn" type="submit" title="Saves profile to the list below"><i class="fa-regular fa-floppy-disk"></i> <span class="btntext">Save Profile</span></button>
            <button class="ghost" type="button" id="cancelEdit" style="display:none" title="Cancel editing and reset the form"><i class="fa-solid fa-xmark"></i> <span class="btntext">Cancel Edit</span></button>
          </div>
          <div class="hint" id="formHint">Tip: After saving, it will start automatically. When it's ready, you'll see the copy buttons in Manage.</div>
        </form>
      </div>

      <!-- Manage step -->
      <div class="card pane manage" x-show="step==='manage'" x-transition.opacity.duration.180ms>
      <h2>Manage Profiles</h2>
      <div class="sub">Start or stop streaming, copy your links, or update your details. Editing will stop the stream first for safety.</div>
      <div id="profiles" class="profiles">
        {{range .Profiles}}
          <div class="p" data-id="{{.ID}}" data-name="{{.Name}}" data-portal="{{.PortalURL}}" data-mac="{{.MAC}}" data-hls="{{.HlsPort}}" data-proxy="{{.ProxyPort}}">
            <div class="phead">
              <div>
                <div class="pname">{{if .Name}}{{.Name}}{{else}}Profile {{.ID}}{{end}}</div>
                <div class="sub" style="margin-top:6px">Portal: <span style="color:#c5d1c5">{{.PortalURL}}</span></div>
                <div class="sub">MAC: <span style="color:#c5d1c5">{{.MAC}}</span></div>
              </div>
              <div class="badg" id="badge-{{.ID}}" title="Current status of this profile">Idle</div>
            </div>

            <div class="actions">
              <form method="post" action="#" style="margin:0" onsubmit="return false" title="Starts this profile (authenticates, fetches channels, and launches HLS/Proxy)">
                <input type="hidden" name="id" value="{{.ID}}" />
                <button class="ok" id="startbtn-{{.ID}}" type="button" @click="onStartClicked({{.ID}})" title="Start this profile"><i class="fa-solid fa-play"></i> <span class="btntext">Start</span></button>
              </form>
              <form method="post" action="#" style="margin:0" onsubmit="return false" title="Stops streaming for this profile">
                <input type="hidden" name="id" value="{{.ID}}" />
                <button class="ghost" id="stopbtn-{{.ID}}" type="submit" title="Stop this profile"><i class="fa-solid fa-stop"></i> <span class="btntext">Stop</span></button>
              </form>
              <form method="post" action="#" style="margin:0" onsubmit="return false" title="Edit this profile (fills the form above)">
                <button class="edit" type="button" data-action="edit" title="Edit this profile"><i class="fa-solid fa-pen"></i> <span class="btntext">Edit</span></button>
              </form>
              <form method="post" action="/profiles/delete" style="margin:0" onsubmit="return confirm('Delete this profile? This cannot be undone.')" title="Removes this profile from the list">
                <input type="hidden" name="id" value="{{.ID}}" />
                <button class="danger" type="submit" title="Delete this profile"><i class="fa-solid fa-trash"></i> <span class="btntext">Delete</span></button>
              </form>
              <div class="spacer"></div>
              <a class="ghost linkbtn" id="hls-{{.ID}}" href="#" data-copy="http://{{$.Host}}:{{.HlsPort}}/" title="Copy HLS endpoint"><i class="fa-solid fa-film"></i> <span class="btntext">HLS</span></a>
              <a class="ghost linkbtn proxybtn" id="pxy-{{.ID}}" href="#" data-copy="http://{{$.Host}}:{{.ProxyPort}}/" title="Copy Proxy endpoint"><i class="fa-solid fa-right-left"></i> <span class="btntext">Proxy</span></a>
            </div>
            <div class="meta" id="meta-{{.ID}}" title="Detailed status and channel count"></div>
          </div>
        {{else}}
          <div class="p">
            <div class="pname">No profiles yet</div>
            <div class="sub" style="margin-top:6px">Use <b>Add a Profile</b> to create your first profile.</div>
          </div>
        {{end}}
      </div>
    </div>

	  <!-- Advanced step -->
	  <div class="card pane advanced" x-show="step==='advanced'" x-transition.opacity.duration.180ms>
		<h2>Advanced Settings</h2>
		<div class="sub">Optional tuning for stability and proxies. These apply immediately and are process-local.</div>
		<form id="settingsForm" onsubmit="return false">
		  <div class="advgrid">
			<div class="stat">
			  <div class="lbl">Playlist delay (segments)</div>
			  <input id="s_delay" name="playlist_delay_segments" inputmode="numeric" placeholder="Example: 10" title="Helps prevent random buffering by playing a little behind live.&#10;&#10;Good starting point:&#10;- 10&#10;If it still buffers:&#10;- 15 to 20" />
			  <div class="hint">Adds latency but can reduce buffering.</div>
			</div>
			<div class="stat">
			  <div class="lbl">Upstream header timeout (sec)</div>
			  <input id="s_rht" name="response_header_timeout_seconds" inputmode="numeric" placeholder="Example: 15" title="How long we wait for your provider to start responding.&#10;&#10;Recommended:&#10;- 15 (default)&#10;- 20 to 30 if your provider is slow" />
			  <div class="hint">How long to wait for upstream response headers.</div>
			</div>
			<div class="stat">
			  <div class="lbl">Max idle conns/host</div>
			  <input id="s_idle" name="max_idle_conns_per_host" inputmode="numeric" placeholder="Example: 64" title="How many spare connections we keep ready.&#10;&#10;Recommended:&#10;- 64 (default)&#10;- 96 to 128 if many devices stream at once" />
			  <div class="hint">Higher can improve concurrency.</div>
			</div>
		  </div>
		  <div class="advactions">
			<button class="primary" type="button" id="saveSettings"><i class="fa-regular fa-floppy-disk"></i> <span class="btntext">Save Settings</span></button>
		  </div>
		</form>
		<div class="footnote">Tip: Leave a box empty if you don't want to change that setting.</div>
	  </div>
  </div>

  <script>
    //
    // Wizard controller (Alpine.js)
    //
    // Goals:
    // - Keep this page single-file and Go-served (no Node build).
    // - Provide a clean 2-step experience (Create -> Manage).
    // - Reuse existing /api/profile_status polling for live updates.
    //
    function wizard(){
      return {
        step: 'create',
        activeID: '',
        activeName: '',
        init(){
          // Default to manage if there are existing profiles.
          try{
            const hasProfiles = document.querySelectorAll('#profiles .p[data-id]').length > 0;
            if(hasProfiles) this.step = 'manage';
          }catch(e){}
        },
        go(s){ this.step = s; },
        setActiveFromCard(card){
          if(!card) return;
          this.activeID = card.getAttribute('data-id')||'';
          this.activeName = card.getAttribute('data-name') || ('Profile '+this.activeID);
        },
        onStartClicked(id){
          this.activeID = String(id||'');
		  this.activeName = 'Profile ' + this.activeID;
          this.step = 'manage';
          try{ postForm('/api/profiles/start', {id: this.activeID}); }catch(e){}
        },
		onStopClicked(id){
		  const pid = String(id||'');
		  if(!pid) return;
		  try{ postForm('/api/profiles/stop', {id: pid}); }catch(e){}
		  showToast('Stopped', 'Profile stopped.');
		},
        onCreateSubmit(){
          // When user saves/updates a profile, keep the UI simple.
          this.step = 'manage';
        }
      }
    }

    const macRe = /^[0-9A-F]{2}(:[0-9A-F]{2}){5}$/;
    function normalizePortal(raw){
      let s = (raw||'').trim();
      if(!s) return '';
      if(!/^https?:\/\//i.test(s)) s = 'http://' + s;
      try{
        const u = new URL(s);
        let p = (u.pathname||'/').trim();
        if(!p || p === '/') p = '/portal.php';
        if(/\/load\.php$/i.test(p)) p = p.replace(/\/load\.php$/i, '/portal.php');
        if(!/\/portal\.php$/i.test(p)){
          if(/\.php$/i.test(p)) p = '/portal.php';
          else p = (p.replace(/\/+$/,'') || '') + '/portal.php';
        }
        u.pathname = p;
        return u.toString();
      }catch(e){
        return s;
      }
    }
    function showToast(title, msg){
      const t=document.getElementById('toast');
      document.getElementById('toastTitle').textContent=title;
      document.getElementById('toastMsg').textContent=msg;
      const small=document.getElementById('toastSmall');
      if(small) small.textContent='';
      t.style.display='block';
      clearTimeout(window.__toastTimer);
      window.__toastTimer=setTimeout(()=>t.style.display='none', 3800);
    }

    // Developer note: use form-encoded bodies for maximum compatibility with Go's r.FormValue().
    async function postForm(url, data){
      const body = Object.entries(data).map(([k,v]) => encodeURIComponent(k)+'='+encodeURIComponent(String(v))).join('&');
      return fetch(url, {method:'POST', headers:{'Content-Type':'application/x-www-form-urlencoded'}, body});
    }
    function validate(){
      const portal=document.getElementById('portal');
      const mac=document.getElementById('mac');
      const portalErr=document.getElementById('portalErr');
      const macErr=document.getElementById('macErr');
      let ok=true;
      const v=normalizePortal(portal.value||'');
      portal.value=v;
      const m=(mac.value||'').trim().toUpperCase();
      mac.value=m;
      const portalOk = /^https?:\/\//i.test(v) && /portal\.php(\?.*)?$/i.test(v);
      if(!portalOk){ portalErr.style.display='block'; ok=false } else portalErr.style.display='none';
      if(!macRe.test(m)){ macErr.style.display='block'; ok=false } else macErr.style.display='none';
      return ok;
    }
    document.getElementById('addForm').addEventListener('submit', (e)=>{
      if(!validate()){
        e.preventDefault();
        showToast('Fix required fields', 'Please correct Portal URL and MAC format, then try again.');
      }
    });
    function copyText(text){
      if(!text) return Promise.reject(new Error('empty'));
      if(navigator.clipboard && navigator.clipboard.writeText){
        return navigator.clipboard.writeText(text);
      }
      return new Promise((resolve, reject)=>{
        try{
          const ta=document.createElement('textarea');
          ta.value=text;
          ta.setAttribute('readonly','');
          ta.style.position='fixed';
          ta.style.top='-1000px';
          document.body.appendChild(ta);
          ta.select();
          const ok=document.execCommand('copy');
          document.body.removeChild(ta);
          if(ok) resolve(); else reject(new Error('copy failed'));
        }catch(e){ reject(e); }
      });
    }

    document.getElementById('profiles').addEventListener('click', (e)=>{
      const a = e.target && e.target.closest ? e.target.closest('a[data-copy]') : null;
      if(!a) return;
      e.preventDefault();
      const url = a.getAttribute('data-copy') || '';
      copyText(url).then(()=>{
        showToast('Copied', url);
      }).catch(()=>{
        showToast('Copy failed', 'Your browser blocked clipboard access.');
      });
    });

    function resetEdit(){
      document.getElementById('edit_id').value='';
      document.getElementById('saveBtn').innerHTML='<i class="fa-regular fa-floppy-disk"></i> <span class="btntext">Save Profile</span>';
      document.getElementById('cancelEdit').style.display='none';
      const hint=document.getElementById('formHint');
      if(hint) hint.textContent='Tip: After saving, the profile will start automatically. Links will appear below once ready.';
    }
    document.getElementById('cancelEdit').addEventListener('click', ()=>{
      resetEdit();
      document.getElementById('addForm').reset();
      showToast('Edit canceled', 'Form reset back to create mode.');
    });

    document.getElementById('profiles').addEventListener('click', (e)=>{
      const btn = e.target && e.target.closest ? e.target.closest('button[data-action="edit"]') : null;
      if(!btn) return;
      const card = btn.closest('.p');
      if(!card) return;
      const id = card.getAttribute('data-id')||'';
      const name = card.getAttribute('data-name')||'';
      const portal = card.getAttribute('data-portal')||'';
      const mac = card.getAttribute('data-mac')||'';
      const hls = card.getAttribute('data-hls')||'';
      const proxy = card.getAttribute('data-proxy')||'';

	  // Stop running services first to ensure safe credential update
	  fetch('/api/profiles/stop', {
		method: 'POST',
		headers: {'Content-Type':'application/x-www-form-urlencoded'},
		body: 'id=' + encodeURIComponent(id)
	  }).catch(()=>{});

      document.getElementById('edit_id').value=id;
      document.getElementById('name').value=name;
      document.getElementById('portal').value=portal;
      document.getElementById('mac').value=mac;
      document.getElementById('hls_port').value=hls;
      document.getElementById('proxy_port').value=proxy;

      document.getElementById('saveBtn').innerHTML='<i class="fa-regular fa-floppy-disk"></i> <span class="btntext">Save Changes</span>';
      document.getElementById('cancelEdit').style.display='inline-flex';
      const hint=document.getElementById('formHint');
      if(hint) hint.textContent='Editing will update this profile, stop any running services, then restart automatically.';
	  showToast('Editing profile', 'Stopped the running playlist for safety. Make changes and click Save Changes to apply.');
      window.scrollTo({top:0, behavior:'smooth'});
    });

    async function poll(){
      try{
        const r = await fetch('/api/profile_status', {cache:'no-store'});
        const a = await r.json();
        for(const s of a){
          const badge=document.getElementById('badge-'+s.id);
          const meta=document.getElementById('meta-'+s.id);
          const startBtn=document.getElementById('startbtn-'+s.id);
          if(!badge || !meta) continue;
          badge.className='badg';
          if(s.phase==='success') badge.classList.add('ok');
          if(s.phase==='error') badge.classList.add('err');
          if(s.running) badge.classList.add('run');
          const label = s.busy ? 'Starting…' : (s.running ? 'Running' : (s.phase==='success' ? (s.message||'Ready') : (s.phase==='error' ? 'Error' : (s.phase==='validating' ? 'Working…' : 'Idle'))));
          badge.textContent = label;
          let lines=[];
          if(s.message) lines.push(s.message);
          if(s.phase==='error') lines.push('Tip: open Instance Logs for details.');
          if(s.channels) lines.push('Channels: '+s.channels);
          if(s.busy) lines.push('Starting in progress…');
          if(lines.length===0) lines.push('');
          meta.innerHTML = '<div>'+lines.map(x=>String(x).replace(/</g,'&lt;')).join('</div><div>')+'</div>';
          if(startBtn){
            const disabled = !!s.busy || !!s.running;
            startBtn.disabled = disabled;
            startBtn.title = s.busy ? 'Already starting… please wait' : (s.running ? 'Already running' : 'Start this profile');
            			startBtn.style.opacity = disabled ? '0.65' : '';
			startBtn.style.cursor = disabled ? 'not-allowed' : '';
		  }
		}
      }catch(e){}
    }
    setInterval(poll, 1200);
    poll();

	// Save advanced settings
	document.getElementById('saveSettings').addEventListener('click', async ()=>{
		const delay=document.getElementById('s_delay').value||'';
		const rht=document.getElementById('s_rht').value||'';
		const idle=document.getElementById('s_idle').value||'';
		try{
			await postForm('/api/settings', {
				playlist_delay_segments: delay,
				response_header_timeout_seconds: rht,
				max_idle_conns_per_host: idle,
			});
			showToast('Saved', 'Settings applied.');
		}catch(e){
			showToast('Save failed', 'Could not save settings.');
		}
	});
  </script>

  <div class="bottompills">
    <div class="pillrow">
      <div class="pill" title="This is the address your devices should use">
        <i class="fa-solid fa-network-wired"></i> Host: <b>{{.Host}}</b>
      </div>
      <a class="pill pilllink" href="/logs" target="_blank" rel="noopener" title="Open live logs (helps with troubleshooting)">
        <i class="fa-regular fa-file-lines"></i> Logs
      </a>
    </div>
  </div>
</body>
</html>`

		t := template.Must(template.New("dash").Parse(tpl))
		_ = t.Execute(w, data)
	})
}

// GetProfile returns a profile by ID
func GetProfile(id int) (Profile, bool) {
    profMu.RLock()
    defer profMu.RUnlock()
    for _, p := range profiles {
        if p.ID == id { return p, true }
    }
    return Profile{}, false
}

// DeleteProfile removes a profile by ID
func DeleteProfile(id int) {
    profMu.Lock()
    defer profMu.Unlock()
    out := make([]Profile, 0, len(profiles))
    for _, p := range profiles {
        if p.ID != id { out = append(out, p) }
    }
    profiles = out
}

package webui

import (
    "crypto/rand"
    "encoding/hex"
    "html/template"
    "net/http"
    "net/url"
    "os"
    "strings"
    "sync"
    "time"
)

// Simple in-memory session store for admin logins
type sessionStore struct {
    mu    sync.RWMutex
    tokens map[string]time.Time // token -> expiry
}

var (
    sess = &sessionStore{tokens: map[string]time.Time{}}
)

func (s *sessionStore) put(tok string, ttl time.Duration) {
    s.mu.Lock()
    s.tokens[tok] = time.Now().Add(ttl)
    s.mu.Unlock()
}

func (s *sessionStore) valid(tok string) bool {
    if tok == "" { return false }
    s.mu.RLock()
    exp, ok := s.tokens[tok]
    s.mu.RUnlock()
    if !ok { return false }
    if time.Now().After(exp) {
        s.mu.Lock()
        delete(s.tokens, tok)
        s.mu.Unlock()
        return false
    }
    return true
}

func (s *sessionStore) delete(tok string) {
    if tok == "" { return }
    s.mu.Lock()
    delete(s.tokens, tok)
    s.mu.Unlock()
}

func adminUser() string {
    u := strings.TrimSpace(os.Getenv("STALKERHEK_ADMIN_USER"))
    if u == "" { u = "admin" }
    return u
}

func adminPass() string { return strings.TrimSpace(os.Getenv("STALKERHEK_ADMIN_PASSWORD")) }

func authEnabled() bool { return adminPass() != "" }

func randToken(n int) string {
    if n <= 0 { n = 32 }
    b := make([]byte, n)
    if _, err := rand.Read(b); err != nil { return "" }
    return hex.EncodeToString(b)
}

// RegisterAuthHandlers mounts /login and /logout handlers
func RegisterAuthHandlers(mux *http.ServeMux) {
    mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
        // If auth not enabled, redirect to dashboard
        if !authEnabled() {
            http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
            return
        }
        switch r.Method {
        case http.MethodGet:
            next := r.URL.Query().Get("next")
            renderLogin(w, "", next)
            return
        case http.MethodPost:
            if err := r.ParseForm(); err != nil {
                http.Error(w, "bad request", http.StatusBadRequest)
                return
            }
            u := strings.TrimSpace(r.FormValue("username"))
            p := strings.TrimSpace(r.FormValue("password"))
            next := strings.TrimSpace(r.FormValue("next"))
            if u == adminUser() && p == adminPass() {
                tok := randToken(32)
                if tok == "" {
                    http.Error(w, "could not sign in", http.StatusInternalServerError)
                    return
                }
                // 24h session
                sess.put(tok, 24*time.Hour)
                cookie := &http.Cookie{Name: "stk_adm", Value: tok, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode}
                http.SetCookie(w, cookie)
                if next == "" { next = "/dashboard" }
                http.Redirect(w, r, next, http.StatusSeeOther)
                return
            }
            renderLogin(w, "Invalid credentials", next)
            return
        default:
            w.WriteHeader(http.StatusMethodNotAllowed)
            return
        }
    })

    mux.HandleFunc("/logout", func(w http.ResponseWriter, r *http.Request) {
        if !authEnabled() {
            http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
            return
        }
        if c, err := r.Cookie("stk_adm"); err == nil { sess.delete(c.Value) }
        // expire cookie
        http.SetCookie(w, &http.Cookie{Name: "stk_adm", Value: "", Path: "/", Expires: time.Unix(0,0), MaxAge: -1})
        http.Redirect(w, r, "/login", http.StatusSeeOther)
    })
}

// authMiddleware enforces admin session when enabled
func authMiddleware(next http.Handler) http.Handler {
    // Allow-list of public endpoints even when auth is enabled
    public := func(p string) bool {
        if p == "/login" { return true }
        if strings.HasPrefix(p, "/health") { return true }
        if strings.HasPrefix(p, "/metrics") { return true }
        if strings.HasPrefix(p, "/status") { return true }
        if strings.HasPrefix(p, "/info") { return true }
        return false
    }
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if !authEnabled() {
            next.ServeHTTP(w, r)
            return
        }
        if public(r.URL.Path) {
            next.ServeHTTP(w, r)
            return
        }
        c, err := r.Cookie("stk_adm")
        if err != nil || !sess.valid(c.Value) {
            // redirect to login, preserving next
            n := r.URL.RequestURI()
            http.Redirect(w, r, "/login?next="+url.QueryEscape(n), http.StatusSeeOther)
            return
        }
        next.ServeHTTP(w, r)
    })
}

func renderLogin(w http.ResponseWriter, errMsg, next string) {
    if next == "" { next = "/dashboard" }
    data := struct{ Error, Next string }{Error: strings.TrimSpace(errMsg), Next: next}
    const tpl = `<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Stalkerhek Login</title>
  <style>
    :root{--bg:#0a0f0a;--panel:#0d1410;--border:#1f2e23;--text:#e0e6e0;--muted:#9aaa9a;--brand:#2d7a4e;--brand-hover:#3a8f5e;--bad:#e85d4d}
    *{box-sizing:border-box}
    body{margin:0;font-family:system-ui,-apple-system,Segoe UI,Roboto,Ubuntu,Helvetica,Arial,sans-serif;background:linear-gradient(180deg,#0d1410 0%,#0a0f0a 100%);color:var(--text);min-height:100dvh;display:flex;align-items:center;justify-content:center;padding:18px}
    .card{max-width:420px;width:100%;border:1px solid var(--border);border-radius:18px;background:rgba(13,20,16,.86);padding:18px;box-shadow:0 18px 48px rgba(0,0,0,.42)}
    h1{margin:0 0 10px 0;font-size:20px}
    label{display:block;font-size:13px;color:#c5d1c5;margin:10px 0 6px}
    input{width:100%;padding:12px 12px;border-radius:12px;border:1px solid var(--border);background:#0f1612;color:var(--text);outline:none;font-size:15px}
    input:focus{border-color:var(--brand);box-shadow:0 0 0 3px rgba(45,122,78,.2)}
    .row{display:grid;gap:10px}
    .btns{display:flex;justify-content:flex-end;margin-top:14px}
    button{cursor:pointer;border:1px solid var(--border);border-radius:12px;padding:12px 14px;font-size:14px;font-weight:700;background:rgba(13,20,16,.35);color:var(--text)}
    button:hover{background:rgba(45,122,78,.16);border-color:rgba(45,122,78,.65)}
    .err{color:var(--bad);font-size:13px;margin-top:6px;display:{{if .Error}}block{{else}}none{{end}}}
    .muted{font-size:12px;color:var(--muted);margin-top:8px}
  </style>
  <link rel="icon" href="https://i.ibb.co/MyxmyVzz/STALKERHEK-LOGO-1500x1500.png">
  <link rel="stylesheet" href="https://cdnjs.cloudflare.com/ajax/libs/font-awesome/6.5.1/css/all.min.css" referrerpolicy="no-referrer" />
  <meta http-equiv="Content-Security-Policy" content="default-src 'self' https:; style-src 'self' 'unsafe-inline' https:; img-src 'self' data: https:; script-src 'self' https:; connect-src 'self' https:;">
  </head>
<body>
  <form class="card" method="post" action="/login">
    <h1><i class="fa-solid fa-lock"></i> Admin Login</h1>
    <div class="row">
      <input type="hidden" name="next" value="{{.Next}}" />
      <label for="u">Username</label>
      <input id="u" name="username" autocomplete="username" placeholder="admin" required />
      <label for="p">Password</label>
      <input id="p" name="password" type="password" autocomplete="current-password" required />
      <div class="err">{{.Error}}</div>
    </div>
    <div class="btns"><button type="submit">Sign In</button></div>
    <div class="muted">Set STALKERHEK_ADMIN_PASSWORD to enable login.</div>
  </form>
</body>
</html>`
    t := template.Must(template.New("login").Parse(tpl))
    w.Header().Set("Content-Type", "text/html; charset=utf-8")
    _ = t.Execute(w, data)
}

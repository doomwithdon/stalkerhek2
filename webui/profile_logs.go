package webui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

type profileLogEntry struct {
	ID  int64
	TS  time.Time
	Msg string
}

type profileLogStore struct {
	mu         sync.RWMutex
	nextID     int64
	maxEntries int
	entries    []profileLogEntry
	subs       map[chan profileLogEntry]struct{}
}

func newProfileLogStore(maxEntries int) *profileLogStore {
	if maxEntries <= 0 {
		maxEntries = 600
	}
	return &profileLogStore{maxEntries: maxEntries, entries: make([]profileLogEntry, 0, maxEntries), subs: map[chan profileLogEntry]struct{}{}}
}

var (
	plMu    sync.Mutex
	plStore = map[int]*profileLogStore{}
)

func getProfileLogStore(id int) *profileLogStore {
	plMu.Lock()
	s := plStore[id]
	if s == nil {
		s = newProfileLogStore(600)
		plStore[id] = s
	}
	plMu.Unlock()
	return s
}

func AppendProfileLog(id int, msg string) {
	if id <= 0 || msg == "" {
		return
	}
	s := getProfileLogStore(id)
	s.mu.Lock()
	s.nextID++
	e := profileLogEntry{ID: s.nextID, TS: time.Now(), Msg: msg}
	if len(s.entries) >= s.maxEntries {
		copy(s.entries, s.entries[1:])
		s.entries = s.entries[:len(s.entries)-1]
	}
	s.entries = append(s.entries, e)
	for ch := range s.subs {
		select {
		case ch <- e:
		default:
		}
	}
	s.mu.Unlock()
}

func streamProfileLogs(w http.ResponseWriter, r *http.Request, id int) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	s := getProfileLogStore(id)
	ch := make(chan profileLogEntry, 32)

	s.mu.Lock()
	s.subs[ch] = struct{}{}
	history := make([]profileLogEntry, len(s.entries))
	copy(history, s.entries)
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
		close(ch)
	}()

	write := func(e profileLogEntry) error {
		payload, _ := json.Marshal(map[string]any{"id": e.ID, "ts": e.TS.Format(time.RFC3339), "msg": e.Msg})
		_, err := fmt.Fprintf(w, "id: %d\nevent: log\ndata: %s\n\n", e.ID, payload)
		if err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	for _, e := range history {
		if err := write(e); err != nil {
			return
		}
	}

	ka := time.NewTicker(15 * time.Second)
	defer ka.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ka.C:
			_, _ = fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		case e := <-ch:
			if err := write(e); err != nil {
				return
			}
		}
	}
}

func RenderProfileLogsPage(id int, name string) string {
	n := strings.TrimSpace(name)
	if n == "" {
		n = "Profile " + fmt.Sprintf("%d", id)
	}
	return fmt.Sprintf(`<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Logs - %s</title>
  <style>
    :root{--bg:#0a0f0a;--panel:#0d1410;--border:#1f2e23;--text:#e0e6e0;--muted:#9aaa9a;--brand:#2d7a4e;--brand-hover:#3a8f5e}
    *{box-sizing:border-box}
    body{margin:0;font-family:system-ui,-apple-system,Segoe UI,Roboto,Ubuntu,Helvetica,Arial,sans-serif;background:linear-gradient(180deg, #0d1410 0%%, #0a0f0a 100%%);color:var(--text);min-height:100vh}
    a{color:var(--brand);text-decoration:none} a:hover{color:var(--brand-hover);text-decoration:underline}
    .wrap{max-width:1100px;margin:0 auto;padding:18px 12px 40px}
    h1{margin:0 0 6px 0;font-size:20px}
    .sub{color:var(--muted);font-size:13px;line-height:1.45;margin-bottom:12px}
    .box{border:1px solid var(--border);border-radius:14px;background:rgba(15,22,18,.7);padding:12px;min-height:65vh;max-height:76vh;overflow:auto}
    .ln{font-family:ui-monospace,SFMono-Regular,Menlo,Monaco,Consolas,"Liberation Mono","Courier New",monospace;font-size:12.5px;line-height:1.45;padding:6px 8px;border-bottom:1px dashed rgba(31,46,35,.65);white-space:pre-wrap}
    .ln:last-child{border-bottom:none}
    .ts{color:var(--muted);margin-right:10px}
  </style>
</head>
<body>
  <div class="wrap">
    <h1>Real-time Logs — %s</h1>
    <div class="sub">
      This page streams logs live with timestamps. 
      <a href="/dashboard">Back to Dashboard</a>
    </div>
    <div class="box" id="box"></div>
  </div>
  <script>
    const box=document.getElementById('box');
    function addLine(ts,msg){
      const div=document.createElement('div');
      div.className='ln';
      const t=document.createElement('span');
      t.className='ts';
      t.textContent='['+ts+'] ';
      div.appendChild(t);
      div.appendChild(document.createTextNode(msg||''));
      box.appendChild(div);
      box.scrollTop=box.scrollHeight;
    }
    const es=new EventSource('/api/profiles/%d/logs/stream');
    es.addEventListener('log',(ev)=>{
      try{
        const d=JSON.parse(ev.data);
        addLine(d.ts||'?', d.msg||'');
      }catch(e){
        addLine('?', ev.data||'');
      }
    });
    es.onerror=()=>{ addLine(new Date().toISOString(), 'connection lost; retrying...'); };
  </script>
</body>
</html>`, templateEscape(n), templateEscape(n), id)
}

func templateEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	s = strings.ReplaceAll(s, "'", "&#39;")
	return s
}

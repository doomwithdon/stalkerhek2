package proxy

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kidpoleon/stalkerhek/filterstore"
	"github.com/kidpoleon/stalkerhek/stalker"
)

type serverState struct {
	profileID  int
	portalBase string
	cfg        *stalker.Config
	channels   map[string]*stalker.Channel
}

func externalBase(r *http.Request) (scheme string, host string) {
	host = r.Host
	if xf := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); xf != "" {
		host = strings.TrimSpace(strings.Split(xf, ",")[0])
	}
	if fwd := strings.TrimSpace(r.Header.Get("Forwarded")); fwd != "" {
		// Forwarded: proto=https;host=example.com
		fwd = strings.TrimSpace(strings.Split(fwd, ",")[0])
		parts := strings.Split(fwd, ";")
		for _, p := range parts {
			kv := strings.SplitN(strings.TrimSpace(p), "=", 2)
			if len(kv) != 2 {
				continue
			}
			k := strings.ToLower(strings.TrimSpace(kv[0]))
			v := strings.Trim(strings.TrimSpace(kv[1]), "\"")
			switch k {
			case "proto":
				if scheme == "" {
					scheme = v
				}
			case "host":
				if v != "" {
					host = v
				}
			}
		}
	}
	if scheme == "" {
		if xf := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); xf != "" {
			scheme = strings.TrimSpace(strings.Split(xf, ",")[0])
		}
	}
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	return scheme, host
}

// Start starts main routine.
func Start(profileID int, c *stalker.Config, chs map[string]*stalker.Channel) {
	StartWithContext(context.Background(), profileID, c, chs)
}

// StartWithContext starts main routine with graceful shutdown support.
func StartWithContext(ctx context.Context, profileID int, c *stalker.Config, chs map[string]*stalker.Channel) {
	// Channels will be matched by CMD field, not by title
	newChannels := make(map[string]*stalker.Channel)
	for _, v := range chs {
		newChannels[v.CMD] = v
	}

	// Extract scheme://hostname:port from given URL, so we don't have to do it later.
	// Avoid log.Fatalln here: killing the whole process due to one bad profile is too harsh.
	link, err := url.Parse(c.Portal.Location)
	if err != nil {
		log.Printf("Proxy config error (bad portal URL): %v", err)
		return
	}
	portalBase := link.Scheme + "://" + link.Host

	s := &serverState{profileID: profileID, portalBase: portalBase, cfg: c, channels: newChannels}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.requestHandler)

	server := &http.Server{
		Addr:    c.Proxy.Bind,
		Handler: mux,
	}

	log.Println("Proxy service should be started!")

	// Start server in goroutine
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("Proxy server error: %v", err)
		}
	}()

	// Wait for context cancellation
	<-ctx.Done()
	log.Println("Proxy shutdown: draining new requests for 3 seconds...")
	time.Sleep(3 * time.Second)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("Proxy server shutdown error: %v", err)
	} else {
		log.Println("Proxy server shutdown complete")
	}
}

func (s *serverState) requestHandler(w http.ResponseWriter, r *http.Request) {
	log.Println(r.RequestURI)

	query := r.URL.Query()

	var tagAction string
	if tmp, found := query["action"]; found {
		tagAction = tmp[0]
	}

	var tagType string
	if tmp, found := query["type"]; found {
		tagType = tmp[0]
	}

	var tagCMD string
	if tmp, found := query["cmd"]; found {
		tagCMD = tmp[0]
	}

	// ################################################
	// Ignore/fake some requests

	// Handshake
	if tagAction == "handshake" {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"js":{"token":"` + s.cfg.Portal.Token + `","random":"b8c4ef93de04e675350605eb0086bffe51507b88e6a1662e71fe9372"},"text":"generated in: 0.01s; query counter: 1; cache hits: 0; cache miss: 0; php errors: 0; sql errors: 0;"}`))
		return
	}

	// Watchdog
	if tagAction == "get_events" && tagType == "watchdog" {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"js":{"data":{"msgs":0,"additional_services_on":"1"}},"text":"generated in: 0.01s; query counter: 4; cache hits: 0; cache miss: 0; php errors: 0; sql errors: 0;"}`))
		return
	}

	// Log
	if tagAction == "get_events" && tagType == "log" {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"js":1,"text":"generated in: 0.001s; query counter: 7; cache hits: 0; cache miss: 0; php errors: 0; sql errors: 0;"}`))
		return
	}

	// Authentication
	if tagAction == "do_auth" {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"js":true,"text":"array(2) {\n  [\"status\"]=>\n  string(2) \"OK\"\n  [\"results\"]=>\n  bool(true)\n}\ngenerated in: 1.033s; query counter: 7; cache hits: 0; cache miss: 0; php errors: 0; sql errors: 0;"}`))
		return
	}

	// Logout
	if tagAction == "logout" {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"js":true,"text":"generated in: 0.011s; query counter: 4; cache hits: 0; cache miss: 0; php errors: 0; sql errors: 0;"}`))
		return
	}

	// Rewrite links
	if s.cfg.Proxy.Rewrite && tagAction == "create_link" {
		if tagCMD == "" {
			log.Println("STB requested 'create_link', but did not give 'cmd' key in URL query...")
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		// Find Stalker channel
		channel, found := s.channels[tagCMD]
		if !found {
			log.Println("STB requested 'create_link', but gave invalid CMD:", tagCMD)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if !filterstore.IsAllowed(s.profileID, channel) {
			log.Println("STB requested blocked channel CMD:", tagCMD)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		// We must give full path to IPTV stream. Serve at root without "/iptv".
		// Prefer reverse-proxy aware host/proto so links are correct behind HTTPS proxies.
		scheme, host := externalBase(r)
		_, portHLS, _ := net.SplitHostPort(s.cfg.HLS.Bind)
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		streamURL := scheme + "://" + host + ":" + portHLS + "/" + url.PathEscape(channel.Title)

		w.WriteHeader(http.StatusOK)

		responseText := generateNewChannelLink(streamURL, channel.CMD_ID, channel.CMD_CH_ID)
		w.Write([]byte(responseText))

		fmt.Println(responseText)

		return
	}

	// ################################################
	// Rewrite some URL query values

	// Serial number
	if _, exists := query["sn"]; exists {
		query["sn"] = []string{s.cfg.Portal.SerialNumber}
	}

	// Device ID
	if _, exists := query["device_id"]; exists {
		query["device_id"] = []string{s.cfg.Portal.DeviceID}
	}

	// Device ID2
	if _, exists := query["device_id2"]; exists {
		query["device_id2"] = []string{s.cfg.Portal.DeviceID2}
	}

	// Signature
	if _, exists := query["signature"]; exists {
		query["signature"] = []string{s.cfg.Portal.Signature}
	}

	// ################################################
	// Proxy modified request to real Stalker portal and return the response

	// Build (modified) URL
	finalLink := s.portalBase + r.URL.Path

	if len(r.URL.RawQuery) != 0 {
		finalLink += "?" + query.Encode()
	}

	// Perform request
	resp, err := getRequest(finalLink, r, s.cfg)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	// Send response
	addHeaders(resp.Header, w.Header())
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

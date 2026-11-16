package main

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strings"
)

//go:embed ui
var uiFiles embed.FS

// Backend is what the window talks to: the tray application in real life,
// a stub in tests.
type Backend interface {
	Section(name string) (any, bool)
	Action(name string, arg map[string]string) (any, error)
}

type apiServer struct {
	Token string
	Port  int
	ln    net.Listener
}

func (s *apiServer) URL(view string) string {
	u := fmt.Sprintf("http://127.0.0.1:%d/?t=%s", s.Port, s.Token)
	if view != "" {
		u += "&v=" + view
	}
	return u
}

// startAPI serves the window's pages and data on a local port. Data is
// given only to a caller that knows the token.
func startAPI(b Backend, port int) (*apiServer, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, err
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		ln.Close()
		return nil, err
	}
	s := &apiServer{Token: hex.EncodeToString(raw), Port: ln.Addr().(*net.TCPAddr).Port, ln: ln}
	static, _ := fs.Sub(uiFiles, "ui")
	files := http.FileServer(http.FS(static))
	host := fmt.Sprintf("127.0.0.1:%d", s.Port)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		if r.Host != host || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Token")), []byte(s.Token)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		path := strings.TrimPrefix(r.URL.Path, "/api/")
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(path, "get/"):
			v, ok := b.Section(strings.TrimPrefix(path, "get/"))
			if !ok {
				http.Error(w, `{"error":"unknown"}`, http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(v)
		case r.Method == http.MethodPost && strings.HasPrefix(path, "do/"):
			arg := map[string]string{}
			body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
			if len(body) > 0 && json.Unmarshal(body, &arg) != nil {
				http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
				return
			}
			v, err := b.Action(strings.TrimPrefix(path, "do/"), arg)
			if err != nil {
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			if v == nil {
				v = map[string]bool{"ok": true}
			}
			json.NewEncoder(w).Encode(v)
		default:
			http.Error(w, `{"error":"unknown"}`, http.StatusNotFound)
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Host != host {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		files.ServeHTTP(w, r)
	})
	go http.Serve(ln, mux)
	return s, nil
}

// ConfigMap lists the settings as they stand in config.ini.
func ConfigMap(c Config) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(c.Serialize(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			m[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return m
}

// SetConfigKey changes one setting the way a line in config.ini would.
func SetConfigKey(c Config, key, val string) (Config, bool) {
	if _, known := ConfigMap(c)[key]; !known || strings.ContainsAny(key+val, "\r\n") {
		return c, false
	}
	return ParseConfig(c.Serialize() + "\n" + key + " = " + val + "\n"), true
}

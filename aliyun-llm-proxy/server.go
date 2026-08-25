package main

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

//go:embed dashboard/dist/*
var dashboardFiles embed.FS

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
func addCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	return host == "localhost" || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}
func isLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func localIPv4Address() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "127.0.0.1"
	}
	type candidate struct{ name, ip string }
	candidates := []candidate{}
	for _, item := range interfaces {
		if item.Flags&net.FlagUp == 0 || item.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, addressErr := item.Addrs()
		if addressErr != nil {
			continue
		}
		for _, address := range addresses {
			ip, _, parseErr := net.ParseCIDR(address.String())
			if parseErr != nil || ip == nil || ip.IsLoopback() || ip.To4() == nil || ip.IsLinkLocalUnicast() {
				continue
			}
			candidates = append(candidates, candidate{item.Name, ip.String()})
		}
	}
	for _, preferred := range []string{"en0", "en1", "eth0"} {
		for _, item := range candidates {
			if item.name == preferred {
				return item.ip
			}
		}
	}
	if len(candidates) > 0 {
		return candidates[0].ip
	}
	return "127.0.0.1"
}

type apiServer struct {
	proxy     *proxy
	dashboard fs.FS
}

func newAPIServer(proxy *proxy) (*apiServer, error) {
	files, err := fs.Sub(dashboardFiles, "dashboard/dist")
	if err != nil {
		return nil, err
	}
	return &apiServer{proxy: proxy, dashboard: files}, nil
}

func (s *apiServer) baseURL() string {
	host := s.proxy.cfg.Host
	if host == "0.0.0.0" || host == "::" {
		host = localIPv4Address()
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(s.proxy.cfg.Port)) + "/v1"
}

func (s *apiServer) serveAsset(w http.ResponseWriter, name string, index bool) {
	data, err := fs.ReadFile(s.dashboard, name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]string{"code": "not_found", "message": "Asset not found."}})
		return
	}
	contentType := mime.TypeByExtension(path.Ext(name))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if index {
		contentType = "text/html; charset=utf-8"
		w.Header().Set("Cache-Control", "no-store")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *apiServer) requireLocal(w http.ResponseWriter, r *http.Request) bool {
	if isLoopbackRequest(r) {
		return true
	}
	writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]string{"code": "local_only", "message": "Management is only available on this machine."}})
	return false
}
func (s *apiServer) requireAuth(w http.ResponseWriter, r *http.Request) bool {
	if s.proxy.authorized(r.Header.Get("Authorization")) {
		return true
	}
	writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]string{"code": "invalid_api_key", "message": "Invalid local proxy API key."}})
	return false
}
func (s *apiServer) requireDashboardControl(w http.ResponseWriter, r *http.Request) bool {
	if !s.requireLocal(w, r) {
		return false
	}
	if r.Header.Get("X-Proxy-Dashboard") != "1" {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]string{"code": "dashboard_control_denied", "message": "Dashboard control header is required."}})
		return false
	}
	origin := strings.TrimRight(r.Header.Get("Origin"), "/")
	allowed := map[string]bool{fmt.Sprintf("http://127.0.0.1:%d", s.proxy.cfg.Port): true, fmt.Sprintf("http://localhost:%d", s.proxy.cfg.Port): true}
	fetchSite := r.Header.Get("Sec-Fetch-Site")
	if (origin != "" && !allowed[origin]) || (fetchSite != "" && fetchSite != "none" && fetchSite != "same-origin") {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]string{"code": "dashboard_control_denied", "message": "Dashboard control is same-origin only."}})
		return false
	}
	return true
}

func decodeBody(w http.ResponseWriter, r *http.Request, limit int64, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("multiple JSON values")
	}
	return nil
}

func (s *apiServer) handleDashboard(w http.ResponseWriter, r *http.Request, requestPath string) bool {
	isPage := requestPath == "/" || requestPath == "/v1" || requestPath == "/dashboard" || requestPath == "/v1/proxy/dashboard"
	isAsset := strings.HasPrefix(requestPath, "/dashboard-assets/")
	isData := requestPath == "/v1/proxy/dashboard-data"
	isAdmin := strings.HasPrefix(requestPath, "/admin/")
	if !isPage && !isAsset && !isData && !isAdmin {
		return false
	}
	if !s.proxy.cfg.DashboardEnabled {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]string{"code": "not_found", "message": "Endpoint not found."}})
		return true
	}
	if !s.requireLocal(w, r) {
		return true
	}
	switch {
	case r.Method == http.MethodGet && isPage:
		s.serveAsset(w, "index.html", true)
	case r.Method == http.MethodGet && isAsset:
		relative, err := url.PathUnescape(strings.TrimPrefix(requestPath, "/dashboard-assets/"))
		if err != nil || relative == "" || strings.Contains(relative, "..") || strings.HasPrefix(relative, "/") {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]string{"code": "not_found", "message": "Asset not found."}})
		} else {
			s.serveAsset(w, relative, false)
		}
	case r.Method == http.MethodGet && isData:
		writeJSON(w, http.StatusOK, s.proxy.status(s.baseURL()))
	case r.Method == http.MethodGet && requestPath == "/admin/status":
		payload := s.proxy.status(s.baseURL())
		s.proxy.upstreamMu.RLock()
		payload["configured"] = s.proxy.upstreamKey != ""
		s.proxy.upstreamMu.RUnlock()
		payload["client_key"] = s.proxy.clientKey
		writeJSON(w, http.StatusOK, payload)
	case r.Method == http.MethodPost && requestPath == "/admin/upstream-key":
		if r.Header.Get("X-Aliyun-Proxy-Admin") != "1" {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]string{"code": "admin_header_required", "message": "管理请求缺少本机控制标记。"}})
			break
		}
		var body struct {
			APIKey string `json:"api_key"`
		}
		if err := decodeBody(w, r, 5120, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "invalid_json", "message": "请求格式不正确。"}})
			break
		}
		if err := s.proxy.updateUpstreamKey(body.APIKey); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "invalid_api_key", "message": err.Error()}})
			break
		}
		payload := s.proxy.status(s.baseURL())
		payload["configured"] = true
		payload["client_key"] = s.proxy.clientKey
		writeJSON(w, http.StatusOK, payload)
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]string{"code": "not_found", "message": "Endpoint not found."}})
	}
	return true
}

func copyResponseHeaders(w http.ResponseWriter, source http.Header) {
	for _, key := range []string{"Content-Type", "Cache-Control", "Content-Language", "Expires"} {
		if value := source.Get(key); value != "" {
			w.Header().Set(key, value)
		}
	}
}

func (s *apiServer) handleModelControl(w http.ResponseWriter, r *http.Request) {
	if !s.proxy.cfg.DashboardEnabled {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]string{"code": "not_found", "message": "Endpoint not found."}})
		return
	}
	if !s.requireDashboardControl(w, r) {
		return
	}
	var body struct {
		Model   string `json:"model"`
		Enabled *bool  `json:"enabled"`
	}
	if err := decodeBody(w, r, 64<<10, &body); err != nil || strings.TrimSpace(body.Model) == "" || body.Enabled == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "invalid_request", "message": "model and boolean enabled are required."}})
		return
	}
	probed, err := s.proxy.updateModelEnabled(strings.TrimSpace(body.Model), *body.Enabled)
	if err != nil {
		status := http.StatusConflict
		code := "last_enabled_model"
		if errors.Is(err, osErrNotExist) {
			status = http.StatusNotFound
			code = "model_not_found"
		}
		var probeError modelProbeError
		if errors.As(err, &probeError) {
			code = "model_probe_failed"
			writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": err.Error(), "upstream_status": probeError.Status, "upstream_code": probeError.Code}, "dashboard": s.proxy.status(s.baseURL())})
			return
		}
		writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": err.Error()}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"model": body.Model, "enabled": *body.Enabled, "probed": probed, "dashboard": s.proxy.status(s.baseURL())})
}

func (s *apiServer) handleChat(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := decodeBody(w, r, maxRequestSize, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "invalid_json", "message": "Request body must be JSON."}})
		return
	}
	if _, ok := body["messages"].([]any); !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "invalid_request", "message": "messages must be an array."}})
		return
	}
	s.proxy.upstreamMu.RLock()
	configured := s.proxy.upstreamKey != ""
	s.proxy.upstreamMu.RUnlock()
	if !configured {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]string{"code": "upstream_not_configured", "message": "Configure the Aliyun DashScope API Key first."}})
		return
	}
	started := time.Now()
	stream, _ := body["stream"].(bool)
	response, state, attempts := s.proxy.route(body, stream)
	w.Header().Set("X-Proxy-Attempts", strings.Join(attempts, ","))
	if state != nil {
		w.Header().Set("X-Proxy-Model", state.Config.ID)
	}
	addCORS(w)
	if stream && state != nil && response.Stream != nil {
		copyResponseHeaders(w, response.Headers)
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(response.Status)
		failed, disconnected := false, false
		if len(response.Prefetched) > 0 {
			if _, err := w.Write(response.Prefetched); err != nil {
				disconnected = true
			}
		}
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		if !disconnected {
			buffer := make([]byte, 32*1024)
			for {
				count, readErr := response.Stream.Read(buffer)
				if count > 0 {
					if _, writeErr := w.Write(buffer[:count]); writeErr != nil {
						disconnected = true
						break
					}
					if flusher, ok := w.(http.Flusher); ok {
						flusher.Flush()
					}
				}
				if readErr != nil {
					if !errors.Is(readErr, io.EOF) && !errors.Is(readErr, net.ErrClosed) {
						failed = true
					}
					break
				}
			}
		}
		response.Stream.Close()
		status := response.Status
		if failed {
			status = 502
		} else if disconnected {
			status = 499
		}
		s.proxy.recordClientResponse(status, float64(time.Since(started).Microseconds())/1000)
		return
	}
	copyResponseHeaders(w, response.Headers)
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(response.Body)))
	w.WriteHeader(response.Status)
	_, _ = w.Write(response.Body)
	s.proxy.recordClientResponse(response.Status, float64(time.Since(started).Microseconds())/1000)
}

func (s *apiServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestPath := strings.TrimRight(r.URL.Path, "/")
	if requestPath == "" {
		requestPath = "/"
	}
	if s.handleDashboard(w, r, requestPath) {
		return
	}
	if r.Method == http.MethodOptions {
		addCORS(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method == http.MethodGet && requestPath == "/health" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if r.Method == http.MethodPost && requestPath == "/v1/proxy/models/enabled" {
		s.handleModelControl(w, r)
		return
	}
	addCORS(w)
	if !s.requireAuth(w, r) {
		return
	}
	switch {
	case r.Method == http.MethodGet && requestPath == "/v1/models":
		aliases := []string{s.proxy.cfg.ModelAlias}
		if s.proxy.cfg.ModelAlias != "translategemma-4b-it" {
			aliases = append(aliases, "translategemma-4b-it")
		}
		data := make([]map[string]string, 0, len(aliases))
		for _, alias := range aliases {
			data = append(data, map[string]string{"id": alias, "object": "model", "owned_by": "local-aliyun-proxy"})
		}
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
	case r.Method == http.MethodGet && (requestPath == "/v1/proxy/status" || requestPath == "/status"):
		writeJSON(w, http.StatusOK, s.proxy.status(s.baseURL()))
	case r.Method == http.MethodPost && requestPath == "/v1/chat/completions":
		s.handleChat(w, r)
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]string{"code": "not_found", "message": "Endpoint not found."}})
	}
}

func serveHTTP(cfg config, clientKey, upstreamKey string) (*http.Server, *proxy, error) {
	unavailable := newUnavailableStore(statePath("unavailable_models.json"))
	metrics, err := newMetricsStore(statePath("metrics.sqlite3"))
	if err != nil {
		return nil, nil, err
	}
	proxy, err := newProxy(cfg, clientKey, upstreamKey, unavailable, metrics)
	if err != nil {
		metrics.close()
		return nil, nil, err
	}
	handler, err := newAPIServer(proxy)
	if err != nil {
		proxy.close()
		return nil, nil, err
	}
	server := &http.Server{Addr: net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)), Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, ErrorLog: log.Default()}
	return server, proxy, nil
}

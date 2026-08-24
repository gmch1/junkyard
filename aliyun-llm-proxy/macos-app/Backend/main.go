package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	modelAlias     = "aliyun-translate-auto"
	defaultPort    = 39281
	maxRequestSize = 5 << 20
	maxKeySize     = 4096
)

var defaultModels = []string{
	"deepseek-v4-pro",
	"deepseek-v3.2",
	"deepseek-v3.1",
	"deepseek-v3",
	"kimi-k3",
	"Moonshot-Kimi-K2-Instruct",
	"MiniMax-M2.5",
	"qwen3.8-max",
	"qwen3.7-max",
	"qwen3-max",
	"qwen3.6-plus",
	"qwen3.5-plus",
	"qwen-plus",
	"qwen-turbo",
	"qwen3.6-flash-2026-04-16",
	"qwen3.5-flash-2026-02-23",
	"qwen3.7-plus-2026-05-26",
	"qwen3.6-plus-2026-04-02",
	"qwen3-30b-a3b-instruct-2507",
}

type modelConfig struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
}

type config struct {
	Version               int           `json:"version"`
	Host                  string        `json:"host"`
	Port                  int           `json:"port"`
	UpstreamBaseURL       string        `json:"upstream_base_url"`
	ModelAlias            string        `json:"model_alias"`
	RequestTimeoutSeconds int           `json:"request_timeout_seconds"`
	Models                []modelConfig `json:"models"`
}

type modelRuntime struct {
	ID            string
	Enabled       bool
	Unavailable   bool
	CooldownUntil time.Time
	Requests      uint64
	Successes     uint64
	Failures      uint64
	Throttles     uint64
}

type service struct {
	cfg       config
	client    *http.Client
	startedAt time.Time

	mu          sync.Mutex
	upstreamKey string
	models      []*modelRuntime
	nextModel   uint64
	requests    uint64
	successes   uint64
	failures    uint64
}

func defaultConfig() config {
	models := make([]modelConfig, 0, len(defaultModels))
	for _, id := range defaultModels {
		models = append(models, modelConfig{ID: id, Enabled: true})
	}
	return config{
		Version:               1,
		Host:                  "0.0.0.0",
		Port:                  defaultPort,
		UpstreamBaseURL:       "https://dashscope.aliyuncs.com/compatible-mode/v1",
		ModelAlias:            modelAlias,
		RequestTimeoutSeconds: 120,
		Models:                models,
	}
}

func stateDirectory() string {
	if value := strings.TrimSpace(os.Getenv("ALIYUN_PROXY_STATE_DIR")); value != "" {
		return value
	}
	userHome, err := os.UserHomeDir()
	if err == nil && userHome != "" {
		return filepath.Join(userHome, "Library", "Application Support", "AliyunLLMProxy")
	}
	return filepath.Join(os.TempDir(), "AliyunLLMProxy")
}

func statePath(name string) string { return filepath.Join(stateDirectory(), name) }

func ensureStateDirectory() error {
	if err := os.MkdirAll(stateDirectory(), 0o700); err != nil {
		return err
	}
	return os.Chmod(stateDirectory(), 0o700)
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := ensureStateDirectory(); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

func readSecret(name string) string {
	data, err := os.ReadFile(statePath(name))
	if err != nil {
		return ""
	}
	_ = os.Chmod(statePath(name), 0o600)
	return strings.TrimSpace(string(data))
}

func writeSecret(name, value string) error {
	value = strings.TrimSpace(value)
	if len(value) < 8 || len(value) > maxKeySize || strings.ContainsAny(value, "\r\n") {
		return errors.New("API Key 长度或格式不正确")
	}
	return atomicWrite(statePath(name), []byte(value+"\n"), 0o600)
}

func ensureClientKey() (string, error) {
	if value := readSecret("client.key"); value != "" {
		return value, nil
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	value := "ap-" + base64.RawURLEncoding.EncodeToString(random)
	if err := writeSecret("client.key", value); err != nil {
		return "", err
	}
	return value, nil
}

func loadConfig() (config, error) {
	resolved := defaultConfig()
	path := statePath("proxy.json")
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &resolved); err != nil {
			return config{}, fmt.Errorf("invalid proxy.json: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return config{}, err
	} else {
		encoded, marshalErr := json.MarshalIndent(resolved, "", "  ")
		if marshalErr != nil {
			return config{}, marshalErr
		}
		if err := atomicWrite(path, append(encoded, '\n'), 0o600); err != nil {
			return config{}, err
		}
	}
	if value := strings.TrimSpace(os.Getenv("ALIYUN_PROXY_HOST")); value != "" {
		resolved.Host = value
	}
	if value := strings.TrimSpace(os.Getenv("ALIYUN_PROXY_PORT")); value != "" {
		port, parseErr := strconv.Atoi(value)
		if parseErr != nil {
			return config{}, errors.New("ALIYUN_PROXY_PORT must be an integer")
		}
		resolved.Port = port
	}
	if resolved.Host == "" || resolved.Port < 1 || resolved.Port > 65535 {
		return config{}, errors.New("invalid listen address")
	}
	if resolved.UpstreamBaseURL == "" || resolved.RequestTimeoutSeconds < 1 {
		return config{}, errors.New("invalid upstream configuration")
	}
	if resolved.ModelAlias == "" {
		resolved.ModelAlias = modelAlias
	}
	enabled := 0
	for _, model := range resolved.Models {
		if model.Enabled && strings.TrimSpace(model.ID) != "" {
			enabled++
		}
	}
	if enabled == 0 {
		return config{}, errors.New("at least one model must be enabled")
	}
	return resolved, nil
}

func newService(cfg config, upstreamKey string) *service {
	models := make([]*modelRuntime, 0, len(cfg.Models))
	for _, model := range cfg.Models {
		if strings.TrimSpace(model.ID) != "" {
			models = append(models, &modelRuntime{ID: model.ID, Enabled: model.Enabled})
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 32
	transport.IdleConnTimeout = 90 * time.Second
	return &service{
		cfg:         cfg,
		client:      &http.Client{Transport: transport, Timeout: time.Duration(cfg.RequestTimeoutSeconds) * time.Second},
		startedAt:   time.Now(),
		upstreamKey: upstreamKey,
		models:      models,
	}
}

func (s *service) reloadUpstreamKey() error {
	value := readSecret("dashscope.key")
	if value == "" {
		return errors.New("DashScope API Key is empty")
	}
	s.mu.Lock()
	s.upstreamKey = value
	s.mu.Unlock()
	return nil
}

func (s *service) authorized(header, clientKey string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	provided := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	if len(provided) != len(clientKey) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(clientKey)) == 1
}

func (s *service) orderedModels() []*modelRuntime {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.models) == 0 {
		return nil
	}
	start := int(s.nextModel % uint64(len(s.models)))
	s.nextModel++
	now := time.Now()
	ordered := make([]*modelRuntime, 0, len(s.models))
	for offset := 0; offset < len(s.models); offset++ {
		model := s.models[(start+offset)%len(s.models)]
		if model.Enabled && !model.Unavailable && !now.Before(model.CooldownUntil) {
			ordered = append(ordered, model)
		}
	}
	return ordered
}

func (s *service) recordAttempt(model *modelRuntime, status int, retryable, unavailable bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	model.Requests++
	if status >= 200 && status < 300 {
		model.Successes++
		return
	}
	model.Failures++
	if status == http.StatusTooManyRequests {
		model.Throttles++
	}
	if unavailable {
		model.Unavailable = true
	} else if retryable {
		model.CooldownUntil = time.Now().Add(60 * time.Second)
	}
}

func parseUpstreamError(body []byte) (string, string) {
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return "", ""
	}
	if nested, ok := payload["error"].(map[string]any); ok {
		payload = nested
	}
	code, _ := payload["code"].(string)
	message, _ := payload["message"].(string)
	return code, message
}

func retryDecision(status int, body []byte) (retryable, unavailable bool) {
	if status == 429 || status == 500 || status == 502 || status == 503 || status == 504 {
		return true, false
	}
	if status != 403 && status != 404 {
		return false, false
	}
	code, _ := parseUpstreamError(body)
	switch strings.ToLower(code) {
	case "modelnotfound", "model.accessdenied", "model_not_found", "allocationquota.freetieronly":
		return true, true
	default:
		return false, false
	}
}

func copyRequestHeaders(destination, source http.Header) {
	for key, values := range source {
		switch strings.ToLower(key) {
		case "authorization", "host", "content-length", "connection", "transfer-encoding":
			continue
		}
		for _, value := range values {
			destination.Add(key, value)
		}
	}
}

func copyResponseHeaders(destination, source http.Header) {
	for key, values := range source {
		switch strings.ToLower(key) {
		case "content-length", "connection", "transfer-encoding", "content-encoding":
			continue
		}
		for _, value := range values {
			destination.Add(key, value)
		}
	}
}

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

func (s *service) status() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	models := make([]map[string]any, 0, len(s.models))
	available := 0
	for _, model := range s.models {
		cooldown := max(0, int(time.Until(model.CooldownUntil).Seconds()))
		if model.Enabled && !model.Unavailable && cooldown == 0 {
			available++
		}
		models = append(models, map[string]any{
			"id": model.ID, "enabled": model.Enabled, "unavailable": model.Unavailable,
			"cooldown_seconds": cooldown, "requests": model.Requests, "successes": model.Successes,
			"failures": model.Failures, "throttles": model.Throttles,
		})
	}
	return map[string]any{
		"model_alias":      s.cfg.ModelAlias,
		"uptime_seconds":   int(time.Since(s.startedAt).Seconds()),
		"client":           map[string]any{"requests": s.requests, "successes": s.successes, "failures": s.failures},
		"models":           models,
		"available_models": available,
	}
}

func (s *service) handleChat(w http.ResponseWriter, r *http.Request) {
	bodyBytes, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestSize))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "invalid_request", "message": "Invalid request size."}})
		return
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(bodyBytes, &body) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "invalid_json", "message": "Request body must be JSON."}})
		return
	}
	messages, ok := body["messages"]
	if !ok || len(messages) == 0 || messages[0] != '[' {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "invalid_request", "message": "messages must be an array."}})
		return
	}

	s.mu.Lock()
	s.requests++
	s.mu.Unlock()
	models := s.orderedModels()
	if len(models) == 0 {
		s.mu.Lock()
		s.failures++
		s.mu.Unlock()
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]string{"code": "no_available_models", "message": "No Aliyun model is currently available."}})
		return
	}

	attempts := make([]string, 0, len(models))
	var lastStatus int
	var lastBody []byte
	var lastHeader http.Header
	for _, model := range models {
		attempts = append(attempts, model.ID)
		body["model"], _ = json.Marshal(model.ID)
		encoded, _ := json.Marshal(body)
		upstreamURL := strings.TrimRight(s.cfg.UpstreamBaseURL, "/") + "/chat/completions"
		request, requestErr := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(encoded))
		if requestErr != nil {
			continue
		}
		copyRequestHeaders(request.Header, r.Header)
		request.Header.Set("Content-Type", "application/json")
		s.mu.Lock()
		upstreamKey := s.upstreamKey
		s.mu.Unlock()
		request.Header.Set("Authorization", "Bearer "+upstreamKey)
		response, requestErr := s.client.Do(request)
		if requestErr != nil {
			s.recordAttempt(model, 0, true, false)
			lastStatus = http.StatusBadGateway
			lastBody, _ = json.Marshal(map[string]any{"error": map[string]string{"code": "upstream_connection_error", "message": requestErr.Error()}})
			continue
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			s.recordAttempt(model, response.StatusCode, false, false)
			s.mu.Lock()
			s.successes++
			s.mu.Unlock()
			addCORS(w)
			copyResponseHeaders(w.Header(), response.Header)
			w.Header().Set("X-Proxy-Attempts", strings.Join(attempts, ","))
			w.Header().Set("X-Proxy-Model", model.ID)
			w.WriteHeader(response.StatusCode)
			buffer := make([]byte, 32*1024)
			for {
				count, readErr := response.Body.Read(buffer)
				if count > 0 {
					if _, err := w.Write(buffer[:count]); err != nil {
						break
					}
					if flusher, ok := w.(http.Flusher); ok {
						flusher.Flush()
					}
				}
				if readErr != nil {
					break
				}
			}
			response.Body.Close()
			return
		}
		responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, maxRequestSize))
		response.Body.Close()
		if readErr != nil {
			responseBody = []byte(`{"error":{"code":"upstream_read_error","message":"Could not read upstream response."}}`)
		}
		retryable, unavailable := retryDecision(response.StatusCode, responseBody)
		s.recordAttempt(model, response.StatusCode, retryable, unavailable)
		lastStatus, lastBody, lastHeader = response.StatusCode, responseBody, response.Header.Clone()
		if !retryable {
			break
		}
	}

	s.mu.Lock()
	s.failures++
	s.mu.Unlock()
	if lastStatus == 0 {
		lastStatus = http.StatusBadGateway
	}
	addCORS(w)
	copyResponseHeaders(w.Header(), lastHeader)
	w.Header().Set("X-Proxy-Attempts", strings.Join(attempts, ","))
	if len(lastBody) == 0 {
		lastBody = []byte(`{"error":{"code":"upstream_failed","message":"All Aliyun model attempts failed."}}`)
	}
	w.WriteHeader(lastStatus)
	_, _ = w.Write(lastBody)
}

func (s *service) handler(clientKey string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		addCORS(w)
		path := strings.TrimRight(r.URL.Path, "/")
		if path == "" {
			path = "/"
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method == http.MethodGet && path == "/health" {
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
			return
		}
		if !s.authorized(r.Header.Get("Authorization"), clientKey) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]string{"code": "invalid_api_key", "message": "Invalid local proxy API key."}})
			return
		}
		switch {
		case r.Method == http.MethodGet && path == "/v1/models":
			writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": []map[string]string{{"id": s.cfg.ModelAlias, "object": "model", "owned_by": "local-aliyun-proxy"}, {"id": "translategemma-4b-it", "object": "model", "owned_by": "local-aliyun-proxy"}}})
		case r.Method == http.MethodGet && (path == "/v1/proxy/status" || path == "/status"):
			writeJSON(w, http.StatusOK, s.status())
		case r.Method == http.MethodPost && path == "/v1/chat/completions":
			s.handleChat(w, r)
		default:
			writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]string{"code": "not_found", "message": "Endpoint not found."}})
		}
	})
}

func processPID() int {
	data, err := os.ReadFile(statePath("proxy.pid"))
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return pid
}

func sameExecutable(pid int) bool {
	if pid < 1 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	current, err := os.Executable()
	if err != nil {
		return false
	}
	output, err := exec.Command("/bin/ps", "-ww", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return false
	}
	command := strings.TrimSpace(string(output))
	return strings.HasPrefix(command, current+" ") || command == current
}

func signalServer(signal syscall.Signal) bool {
	pid := processPID()
	if !sameExecutable(pid) {
		return false
	}
	return syscall.Kill(pid, signal) == nil
}

func localHealth(cfg config, clientKey string) bool {
	host := cfg.Host
	if host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	request, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://%s:%d/v1/proxy/status", host, cfg.Port), nil)
	request.Header.Set("Authorization", "Bearer "+clientKey)
	client := &http.Client{Timeout: time.Second}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	response.Body.Close()
	return response.StatusCode == http.StatusOK
}

func serve() error {
	if err := ensureStateDirectory(); err != nil {
		return err
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	clientKey, err := ensureClientKey()
	if err != nil {
		return err
	}
	upstreamKey := readSecret("dashscope.key")
	if upstreamKey == "" {
		return errors.New("Aliyun DashScope API Key is not configured")
	}
	svc := newService(cfg, upstreamKey)
	server := &http.Server{
		Addr:              net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		Handler:           svc.handler(clientKey),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return err
	}
	if err := atomicWrite(statePath("proxy.pid"), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		listener.Close()
		return err
	}
	defer func() {
		if processPID() == os.Getpid() {
			_ = os.Remove(statePath("proxy.pid"))
		}
	}()
	log.Printf("started host=%s port=%d models=%d", cfg.Host, cfg.Port, len(svc.models))
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		for received := range signals {
			if received == syscall.SIGHUP {
				if err := svc.reloadUpstreamKey(); err != nil {
					log.Printf("API Key reload failed: %v", err)
				} else {
					log.Printf("API Key reloaded")
				}
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = server.Shutdown(ctx)
			cancel()
			return
		}
	}()
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func start() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	clientKey, err := ensureClientKey()
	if err != nil {
		return err
	}
	if readSecret("dashscope.key") == "" {
		return errors.New("Aliyun DashScope API Key is not configured")
	}
	if localHealth(cfg, clientKey) {
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(statePath("proxy.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	command := exec.Command(executable, "serve")
	command.Env = os.Environ()
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		logFile.Close()
		return err
	}
	logFile.Close()
	for attempt := 0; attempt < 100; attempt++ {
		if localHealth(cfg, clientKey) {
			fmt.Printf("Aliyun proxy started (PID %d).\n", command.Process.Pid)
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = command.Process.Kill()
	return errors.New("timed out waiting for proxy startup")
}

func stop() error {
	pid := processPID()
	if !sameExecutable(pid) {
		_ = os.Remove(statePath("proxy.pid"))
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return err
	}
	for attempt := 0; attempt < 100; attempt++ {
		if !sameExecutable(pid) {
			_ = os.Remove(statePath("proxy.pid"))
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("proxy did not stop within 10 seconds")
}

func setUpstreamKey(fromEnvironment bool) error {
	value := ""
	if fromEnvironment {
		value = os.Getenv("DASHSCOPE_API_KEY")
	} else {
		data, err := io.ReadAll(io.LimitReader(os.Stdin, maxKeySize+1))
		if err != nil {
			return err
		}
		value = string(data)
	}
	if err := writeSecret("dashscope.key", value); err != nil {
		return err
	}
	_ = signalServer(syscall.SIGHUP)
	return nil
}

func usage() {
	fmt.Fprintf(os.Stderr, "Usage: %s serve|start|stop|status|key|set-upstream-key\n", filepath.Base(os.Args[0]))
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve()
	case "start":
		err = start()
	case "stop":
		err = stop()
	case "status":
		cfg, loadErr := loadConfig()
		if loadErr != nil {
			err = loadErr
			break
		}
		key, keyErr := ensureClientKey()
		if keyErr != nil {
			err = keyErr
			break
		}
		if !localHealth(cfg, key) {
			err = errors.New("proxy is not running")
		}
	case "key":
		var key string
		key, err = ensureClientKey()
		if err == nil {
			fmt.Println(key)
		}
	case "set-upstream-key":
		err = setUpstreamKey(len(os.Args) > 2 && os.Args[2] == "--from-env")
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

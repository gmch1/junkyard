package main

import (
	"bytes"
	"context"
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
	"syscall"
	"time"

	"golang.org/x/term"
)

func processPID() int {
	data, err := os.ReadFile(statePath("proxy.pid"))
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return pid
}

func processCommand(pid int) string {
	if pid < 1 {
		return ""
	}
	if err := syscall.Kill(pid, 0); err != nil {
		return ""
	}
	output, err := exec.Command("/bin/ps", "-ww", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func sameProxyProcess(pid int) bool {
	command := processCommand(pid)
	if command == "" || !strings.Contains(command, "serve") {
		return false
	}
	executable, err := os.Executable()
	if err != nil {
		return false
	}
	resolved, _ := filepath.EvalSymlinks(executable)
	return strings.Contains(command, executable) || (resolved != "" && strings.Contains(command, resolved))
}
func legacyPythonProcess(pid int) bool {
	command := processCommand(pid)
	return strings.Contains(command, "aliyun_proxy.py") && strings.Contains(command, "serve")
}

func signalServer(value syscall.Signal) bool {
	pid := processPID()
	if !sameProxyProcess(pid) {
		return false
	}
	return syscall.Kill(pid, value) == nil
}

func localConnectHost(cfg config) string {
	if cfg.Host == "0.0.0.0" || cfg.Host == "::" {
		return "127.0.0.1"
	}
	return cfg.Host
}
func localURL(cfg config, path string) string {
	return "http://" + net.JoinHostPort(localConnectHost(cfg), strconv.Itoa(cfg.Port)) + path
}

func localHealth(cfg config) bool {
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get(localURL(cfg, "/health"))
	if err != nil {
		return false
	}
	response.Body.Close()
	return response.StatusCode == http.StatusOK
}
func localDashboardHealth(cfg config) bool {
	request, _ := http.NewRequest(http.MethodGet, localURL(cfg, "/v1/proxy/dashboard-data"), nil)
	client := &http.Client{Timeout: time.Second}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	response.Body.Close()
	return response.StatusCode == http.StatusOK
}

func localServiceReady(cfg config) bool {
	return localHealth(cfg) && (!cfg.DashboardEnabled || localDashboardHealth(cfg))
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
	server, proxy, err := serveHTTP(cfg, clientKey, readSecret("dashscope.key"))
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		proxy.close()
		return err
	}
	if err := atomicWrite(statePath("proxy.pid"), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		listener.Close()
		proxy.close()
		return err
	}
	defer func() {
		if processPID() == os.Getpid() {
			_ = os.Remove(statePath("proxy.pid"))
		}
		proxy.close()
	}()
	log.Printf("started host=%s port=%d models=%d", cfg.Host, cfg.Port, len(cfg.Models))
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for received := range signals {
			if received == syscall.SIGHUP {
				if err := proxy.reloadUpstreamKey(); err != nil {
					log.Printf("upstream_key_reload=false error=%v", err)
				} else {
					log.Printf("upstream_key_reload=true")
				}
				continue
			}
			log.Printf("received_signal=%s stopping=true", received)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = server.Shutdown(ctx)
			cancel()
			return
		}
	}()
	err = server.Serve(listener)
	signal.Stop(signals)
	close(signals)
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	if err == nil {
		select {
		case <-done:
		default:
		}
	}
	log.Printf("stopped")
	return err
}

func stopPID(pid int) bool {
	if !sameProxyProcess(pid) && !legacyPythonProcess(pid) {
		return true
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return processCommand(pid) == ""
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if processCommand(pid) == "" {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if processCommand(pid) == "" {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return processCommand(pid) == ""
}

func start() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if _, err := ensureClientKey(); err != nil {
		return err
	}
	pid := processPID()
	if sameProxyProcess(pid) && localServiceReady(cfg) {
		fmt.Printf("Aliyun proxy is already running (PID %d).\n", pid)
		return nil
	}
	if (sameProxyProcess(pid) || legacyPythonProcess(pid)) && !stopPID(pid) {
		return fmt.Errorf("existing proxy PID %d could not be stopped", pid)
	}
	_ = os.Remove(statePath("proxy.pid"))
	if localHealth(cfg) {
		return fmt.Errorf("port %d is already used by another service", cfg.Port)
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
		if localServiceReady(cfg) {
			fmt.Printf("Aliyun proxy started (PID %d).\n", command.Process.Pid)
			fmt.Printf("Dashboard: %s\n", localURL(cfg, "/"))
			fmt.Printf("Base URL: %s\n", localURL(cfg, "/v1"))
			fmt.Printf("Model: %s\n", cfg.ModelAlias)
			return nil
		}
		if command.ProcessState != nil && command.ProcessState.Exited() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = command.Process.Kill()
	return fmt.Errorf("proxy exited or timed out during startup\n%s", tailLog(30))
}

func stop() error {
	pid := processPID()
	if !sameProxyProcess(pid) && !legacyPythonProcess(pid) {
		_ = os.Remove(statePath("proxy.pid"))
		fmt.Println("Aliyun proxy is not running.")
		return nil
	}
	if !stopPID(pid) {
		return fmt.Errorf("proxy PID %d could not be terminated", pid)
	}
	_ = os.Remove(statePath("proxy.pid"))
	fmt.Println("Aliyun proxy stopped.")
	return nil
}

func fetchStatus(cfg config, clientKey string) (map[string]any, error) {
	request, _ := http.NewRequest(http.MethodGet, localURL(cfg, "/v1/proxy/status"), nil)
	request.Header.Set("Authorization", "Bearer "+clientKey)
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", response.StatusCode)
	}
	var payload map[string]any
	err = json.NewDecoder(response.Body).Decode(&payload)
	return payload, err
}

func showStatus() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	key, err := ensureClientKey()
	if err != nil {
		return err
	}
	pid := processPID()
	payload, fetchErr := fetchStatus(cfg, key)
	running := sameProxyProcess(pid)
	label := "stopped"
	if running {
		label = "starting/unhealthy"
	}
	if fetchErr == nil {
		label = "running"
	}
	fmt.Printf("Status: %s\n", label)
	if running {
		fmt.Printf("PID: %d\n", pid)
	}
	fmt.Printf("Dashboard: %s\nBase URL: %s\nModel: %s\nConfig: %s\nLog: %s\n", localURL(cfg, "/"), localURL(cfg, "/v1"), cfg.ModelAlias, statePath("proxy.json"), statePath("proxy.log"))
	if fetchErr != nil {
		return fetchErr
	}
	encoded, _ := json.MarshalIndent(payload, "", "  ")
	fmt.Println(string(encoded))
	return nil
}

func setUpstreamKey(fromEnvironment bool) error {
	value := ""
	if fromEnvironment {
		value = strings.TrimSpace(os.Getenv("DASHSCOPE_API_KEY"))
		if value == "" {
			return errors.New("DASHSCOPE_API_KEY is empty")
		}
	} else {
		fmt.Fprint(os.Stderr, "Aliyun DashScope API Key: ")
		if term.IsTerminal(int(syscall.Stdin)) {
			data, err := term.ReadPassword(int(syscall.Stdin))
			fmt.Fprintln(os.Stderr)
			if err != nil {
				return err
			}
			value = string(data)
		} else {
			data, err := io.ReadAll(io.LimitReader(os.Stdin, 4097))
			if err != nil {
				return err
			}
			value = string(data)
		}
	}
	if err := writeSecret("dashscope.key", value); err != nil {
		return err
	}
	_ = signalServer(syscall.SIGHUP)
	fmt.Printf("Aliyun API key saved to %s (mode 0600).\n", statePath("dashscope.key"))
	return nil
}

func showUnavailable() error {
	store := newUnavailableStore(statePath("unavailable_models.json"))
	models := store.snapshot()
	if len(models) == 0 {
		fmt.Println("No models are persistently unavailable.")
		return nil
	}
	for id, details := range models {
		fmt.Printf("%s: status=%d code=%s disabled_at=%s\n", id, details.HTTPStatus, details.Code, details.DisabledAt)
	}
	return nil
}
func resetUnavailable(model string) error {
	count, err := newUnavailableStore(statePath("unavailable_models.json")).clear(model)
	if err != nil {
		return err
	}
	fmt.Printf("Cleared %d unavailable model record(s).\n", count)
	if sameProxyProcess(processPID()) {
		fmt.Println("Restart the proxy for this change to take effect.")
	}
	return nil
}

func runProbe() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	key, err := ensureClientKey()
	if err != nil {
		return err
	}
	payload := map[string]any{"model": cfg.ModelAlias, "messages": []map[string]string{{"role": "system", "content": "You are a professional Simplified Mandarin Chinese translator. Output only the translation."}, {"role": "user", "content": "Translate to Simplified Mandarin Chinese:\n\nHello."}}, "temperature": 0, "max_tokens": 32, "stream": false}
	encoded, _ := json.Marshal(payload)
	request, _ := http.NewRequest(http.MethodPost, localURL(cfg, "/v1/chat/completions"), bytes.NewReader(encoded))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 130 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	fmt.Printf("HTTP: %d\nAttempts: %s\nSelected model: %s\n%s\n", response.StatusCode, response.Header.Get("X-Proxy-Attempts"), response.Header.Get("X-Proxy-Model"), data)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("probe failed")
	}
	return nil
}

func usage() {
	fmt.Fprintf(os.Stderr, "Usage: %s start|stop|restart|status|key|logs|config|serve|set-upstream-key [--from-env]|unavailable|reset-unavailable [model]|reload-key|probe\n", filepath.Base(os.Args[0]))
}

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.LUTC)
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	command := os.Args[1]
	var err error
	switch command {
	case "serve":
		err = serve()
	case "start":
		err = start()
	case "stop":
		err = stop()
	case "restart":
		if err = stop(); err == nil {
			err = start()
		}
	case "status":
		err = showStatus()
	case "key":
		var key string
		key, err = ensureClientKey()
		if err == nil {
			fmt.Println(key)
		}
	case "logs":
		fmt.Print(tailLog(100))
	case "config":
		_, err = loadConfig()
		if err == nil {
			fmt.Println(statePath("proxy.json"))
		}
	case "set-upstream-key":
		err = setUpstreamKey(len(os.Args) > 2 && os.Args[2] == "--from-env")
	case "unavailable":
		err = showUnavailable()
	case "reset-unavailable":
		model := ""
		if len(os.Args) > 2 {
			model = os.Args[2]
		}
		err = resetUnavailable(model)
	case "reload-key":
		if !signalServer(syscall.SIGHUP) {
			err = errors.New("proxy is not running")
		} else {
			fmt.Println("Aliyun API key reload requested.")
		}
	case "probe":
		err = runProbe()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

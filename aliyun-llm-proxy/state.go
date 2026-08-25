package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

func stateDirectory() string {
	if value := strings.TrimSpace(os.Getenv("ALIYUN_PROXY_STATE_DIR")); value != "" {
		if absolute, err := filepath.Abs(value); err == nil {
			return absolute
		}
		return value
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		return ".aliyun-proxy"
	}
	return filepath.Join(workingDirectory, ".aliyun-proxy")
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
	path := statePath(name)
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	_ = os.Chmod(path, 0o600)
	return strings.TrimSpace(string(data))
}

func writeSecret(name, value string) error {
	value = strings.TrimSpace(value)
	if len(value) < 8 || len(value) > 4096 || strings.ContainsAny(value, "\r\n") {
		return errors.New("API Key length or format is invalid")
	}
	return atomicWrite(statePath(name), []byte(value+"\n"), 0o600)
}

func ensureClientKey() (string, error) {
	if current := readSecret("client.key"); current != "" {
		return current, nil
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	value := "ap-" + base64.RawURLEncoding.EncodeToString(random)
	if err := atomicWrite(statePath("client.key"), []byte(value+"\n"), 0o600); err != nil {
		return "", err
	}
	return value, nil
}

type unavailableRecord struct {
	DisabledAt string `json:"disabled_at"`
	HTTPStatus int    `json:"http_status"`
	Code       string `json:"code"`
	Message    string `json:"message"`
}

type unavailableStore struct {
	path string
	mu   sync.Mutex
	data map[string]unavailableRecord
}

func newUnavailableStore(path string) *unavailableStore {
	store := &unavailableStore{path: path, data: map[string]unavailableRecord{}}
	data, err := os.ReadFile(path)
	if err == nil {
		var payload struct {
			Models map[string]unavailableRecord `json:"models"`
		}
		if json.Unmarshal(data, &payload) == nil && payload.Models != nil {
			store.data = payload.Models
		}
	}
	return store
}

func (s *unavailableStore) snapshot() map[string]unavailableRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make(map[string]unavailableRecord, len(s.data))
	for key, value := range s.data {
		result[key] = value
	}
	return result
}

func (s *unavailableStore) saveLocked() error {
	data, err := json.MarshalIndent(struct {
		Version int                          `json:"version"`
		Models  map[string]unavailableRecord `json:"models"`
	}{Version: 1, Models: s.data}, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(s.path, append(data, '\n'), 0o600)
}

func (s *unavailableStore) mark(model string, status int, code, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(message) > 500 {
		message = message[:500]
	}
	s.data[model] = unavailableRecord{DisabledAt: time.Now().Format(time.RFC3339), HTTPStatus: status, Code: code, Message: message}
	return s.saveLocked()
}

func (s *unavailableStore) clear(model string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	if model == "" {
		count = len(s.data)
		s.data = map[string]unavailableRecord{}
	} else if _, exists := s.data[model]; exists {
		delete(s.data, model)
		count = 1
	}
	return count, s.saveLocked()
}

type clientPersistentMetrics struct {
	Requests       uint64
	Successes      uint64
	Failures       uint64
	TotalLatencyMS float64
	LatenciesMS    []float64
	HedgedRequests uint64
}

type modelPersistentMetrics struct {
	ID                  string
	Successes           uint64
	Failures            uint64
	Throttles           uint64
	TotalLatencyMS      float64
	InputTokens         uint64
	OutputTokens        uint64
	Adoptions           uint64
	HedgeParticipations uint64
	HedgeWins           uint64
	DiscardedResponses  uint64
	LatenciesMS         []float64
}

type metricsStore struct {
	db            *sql.DB
	mu            sync.Mutex
	lastFlushedAt float64
}

func newMetricsStore(path string) (*metricsStore, error) {
	if err := ensureStateDirectory(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &metricsStore{db: db}
	if _, err := db.Exec(`
		PRAGMA journal_mode=WAL;
		PRAGMA synchronous=NORMAL;
		CREATE TABLE IF NOT EXISTS proxy_metrics (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			client_requests INTEGER NOT NULL DEFAULT 0,
			client_successes INTEGER NOT NULL DEFAULT 0,
			client_failures INTEGER NOT NULL DEFAULT 0,
			client_total_latency_ms REAL NOT NULL DEFAULT 0,
			client_latencies_json TEXT NOT NULL DEFAULT '[]',
			hedged_requests INTEGER NOT NULL DEFAULT 0,
			updated_at REAL NOT NULL DEFAULT 0
		);
		CREATE TABLE IF NOT EXISTS model_metrics (
			model_id TEXT PRIMARY KEY,
			successes INTEGER NOT NULL DEFAULT 0,
			failures INTEGER NOT NULL DEFAULT 0,
			throttles INTEGER NOT NULL DEFAULT 0,
			total_latency_ms REAL NOT NULL DEFAULT 0,
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			adoptions INTEGER NOT NULL DEFAULT 0,
			hedge_participations INTEGER NOT NULL DEFAULT 0,
			hedge_wins INTEGER NOT NULL DEFAULT 0,
			discarded_responses INTEGER NOT NULL DEFAULT 0,
			latencies_json TEXT NOT NULL DEFAULT '[]',
			updated_at REAL NOT NULL DEFAULT 0
		);`); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func decodeLatencies(value string, limit int) []float64 {
	var values []float64
	if json.Unmarshal([]byte(value), &values) != nil {
		return nil
	}
	if len(values) > limit {
		values = values[len(values)-limit:]
	}
	return values
}

func (s *metricsStore) load() (clientPersistentMetrics, map[string]modelPersistentMetrics, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	client := clientPersistentMetrics{}
	var latencyJSON string
	var updated float64
	err := s.db.QueryRow(`SELECT client_requests, client_successes, client_failures, client_total_latency_ms, client_latencies_json, hedged_requests, updated_at FROM proxy_metrics WHERE id=1`).Scan(
		&client.Requests, &client.Successes, &client.Failures, &client.TotalLatencyMS, &latencyJSON, &client.HedgedRequests, &updated,
	)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return client, nil, err
	}
	if err == nil {
		client.LatenciesMS = decodeLatencies(latencyJSON, 500)
		s.lastFlushedAt = updated
	}
	rows, err := s.db.Query(`SELECT model_id, successes, failures, throttles, total_latency_ms, input_tokens, output_tokens, adoptions, hedge_participations, hedge_wins, discarded_responses, latencies_json FROM model_metrics`)
	if err != nil {
		return client, nil, err
	}
	defer rows.Close()
	models := map[string]modelPersistentMetrics{}
	for rows.Next() {
		var item modelPersistentMetrics
		var jsonValue string
		if err := rows.Scan(&item.ID, &item.Successes, &item.Failures, &item.Throttles, &item.TotalLatencyMS, &item.InputTokens, &item.OutputTokens, &item.Adoptions, &item.HedgeParticipations, &item.HedgeWins, &item.DiscardedResponses, &jsonValue); err != nil {
			return client, nil, err
		}
		item.LatenciesMS = decodeLatencies(jsonValue, 200)
		models[item.ID] = item
	}
	return client, models, rows.Err()
}

func encodeLatencies(values []float64) string { data, _ := json.Marshal(values); return string(data) }

func (s *metricsStore) flush(client clientPersistentMetrics, models []modelPersistentMetrics) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := float64(time.Now().UnixNano()) / 1e9
	if _, err := tx.Exec(`INSERT INTO proxy_metrics (id,client_requests,client_successes,client_failures,client_total_latency_ms,client_latencies_json,hedged_requests,updated_at) VALUES (1,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET client_requests=excluded.client_requests,client_successes=excluded.client_successes,client_failures=excluded.client_failures,client_total_latency_ms=excluded.client_total_latency_ms,client_latencies_json=excluded.client_latencies_json,hedged_requests=excluded.hedged_requests,updated_at=excluded.updated_at`, client.Requests, client.Successes, client.Failures, client.TotalLatencyMS, encodeLatencies(client.LatenciesMS), client.HedgedRequests, now); err != nil {
		return err
	}
	statement, err := tx.Prepare(`INSERT INTO model_metrics (model_id,successes,failures,throttles,total_latency_ms,input_tokens,output_tokens,adoptions,hedge_participations,hedge_wins,discarded_responses,latencies_json,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(model_id) DO UPDATE SET successes=excluded.successes,failures=excluded.failures,throttles=excluded.throttles,total_latency_ms=excluded.total_latency_ms,input_tokens=excluded.input_tokens,output_tokens=excluded.output_tokens,adoptions=excluded.adoptions,hedge_participations=excluded.hedge_participations,hedge_wins=excluded.hedge_wins,discarded_responses=excluded.discarded_responses,latencies_json=excluded.latencies_json,updated_at=excluded.updated_at`)
	if err != nil {
		return err
	}
	defer statement.Close()
	for _, item := range models {
		if _, err := statement.Exec(item.ID, item.Successes, item.Failures, item.Throttles, item.TotalLatencyMS, item.InputTokens, item.OutputTokens, item.Adoptions, item.HedgeParticipations, item.HedgeWins, item.DiscardedResponses, encodeLatencies(item.LatenciesMS), now); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.lastFlushedAt = now
	return nil
}

func (s *metricsStore) lastFlush() float64 { s.mu.Lock(); defer s.mu.Unlock(); return s.lastFlushedAt }
func (s *metricsStore) close() error       { return s.db.Close() }

func tailLog(lines int) string {
	data, err := os.ReadFile(statePath("proxy.log"))
	if err != nil {
		return ""
	}
	parts := strings.Split(string(data), "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return strings.Join(parts, "\n")
}

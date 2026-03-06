// tracker-time — Daemon de monitoramento de produtividade para Linux (X11).
// Roda em segundo plano, registra janela ativa e tempo, e sincroniza com API REST.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// --- Constantes de configuração ---

const (
	monitorInterval      = 2 * time.Second  // Loop rápido: a cada 2s
	syncInterval         = 10 * time.Minute // Loop lento: a cada 10min
	connectRetryInterval = 30 * time.Second // Intervalo entre tentativas de conexão ao X11
	defaultIdleThreshold = 2 * time.Second  // Tempo sem mouse/teclado para considerar inativo (configurável por env)
	defaultIngestURL     = "https://api.dashboard.com/v1/ingest"
	defaultTTL           = 168 * time.Hour  // 7 dias: registros mais antigos são apagados (TTL em app, SQLite não tem TTL nativo)
)

// defaultDBPath retorna o caminho padrão do SQLite (diretório do usuário).
func defaultDBPath() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		return "./tracker.db"
	}
	return home + "/.local/share/tracker-time/tracker.db"
}

// getTTL retorna a duração do TTL a partir de TRACKER_TTL (ex: "72h", "168h") ou TRACKER_TTL_HOURS (ex: "168").
// Zero = TTL desativado. Default: 7 dias.
func getTTL() time.Duration {
	if s := os.Getenv("TRACKER_TTL"); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d >= 0 {
			return d
		}
	}
	if s := os.Getenv("TRACKER_TTL_HOURS"); s != "" {
		if h, err := strconv.Atoi(s); err == nil && h >= 0 {
			return time.Duration(h) * time.Hour
		}
	}
	return defaultTTL
}

// getIngestURL retorna a URL da API de ingestão. Variável de ambiente: TRACKER_INGEST_URL ou TRACKER_API_URL.
func getIngestURL() string {
	if s := os.Getenv("TRACKER_INGEST_URL"); s != "" {
		return strings.TrimRight(s, "/")
	}
	if s := os.Getenv("TRACKER_API_URL"); s != "" {
		return strings.TrimRight(s, "/")
	}
	return defaultIngestURL
}

// getIdleThreshold retorna o tempo sem mouse/teclado para considerar usuário inativo.
// Variável de ambiente: TRACKER_IDLE_THRESHOLD (ex: "2s", "60s", "1m"). Default: 2s.
func getIdleThreshold() time.Duration {
	if s := os.Getenv("TRACKER_IDLE_THRESHOLD"); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d >= 0 {
			return d
		}
	}
	return defaultIdleThreshold
}

// --- Structs para banco de dados e payload JSON ---

// MachineIdentity guarda dados da máquina/usuário para consolidar no painel do gestor.
type MachineIdentity struct {
	UserName  string // nome do usuário do SO
	Hostname  string // nome da máquina local (hostname)
	LocalIP   string // IP local (loopback, ex: 127.0.0.1)
	NetworkIP string // IP na rede (primeira IPv4 não loopback)
}

// Record representa uma linha da tabela de atividades (mapeamento SQLite).
type Record struct {
	ID          int64     `json:"id"`
	ProcessName string    `json:"process_name"`
	WindowTitle string    `json:"window_title"`
	StartTime   time.Time `json:"start_time"`
	EndTime     time.Time `json:"end_time"`
	UserName    string    `json:"user_name"`
	Hostname    string    `json:"hostname"`
	LocalIP     string    `json:"local_ip"`
	NetworkIP   string    `json:"network_ip"`
}

// IngestPayload é o corpo do POST enviado à API (pode ser um único registro ou lote).
type IngestPayload struct {
	Events []IngestEvent `json:"events"`
}

// IngestEvent representa um evento no payload de ingestão.
type IngestEvent struct {
	ProcessName string `json:"process_name"`
	WindowTitle string `json:"window_title"`
	StartTime   string `json:"start_time"` // ISO8601
	EndTime     string `json:"end_time"`
	UserName    string `json:"user_name"`
	Hostname    string `json:"hostname"`
	LocalIP     string `json:"local_ip"`
	NetworkIP   string `json:"network_ip"`
}

// --- DDL SQLite (também aplicado em initDB) ---

const createTableSQL = `
CREATE TABLE IF NOT EXISTS activity (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	process_name TEXT NOT NULL,
	window_title TEXT NOT NULL,
	start_time DATETIME NOT NULL,
	end_time DATETIME NOT NULL,
	user_name TEXT NOT NULL DEFAULT '',
	hostname TEXT NOT NULL DEFAULT '',
	local_ip TEXT NOT NULL DEFAULT '',
	network_ip TEXT NOT NULL DEFAULT ''
);
`

// --- Identidade da máquina (usuário e IPs) ---

// getMachineIdentity obtém nome do usuário, hostname, IP local e IP da rede para o gestor consolidar uso por máquina/usuário.
func getMachineIdentity() MachineIdentity {
	id := MachineIdentity{LocalIP: "127.0.0.1"}
	if u, err := user.Current(); err == nil {
		id.UserName = u.Username
	}
	if h, err := os.Hostname(); err == nil {
		id.Hostname = h
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return id
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok || ipNet.IP.IsLoopback() {
				continue
			}
			ip := ipNet.IP.To4()
			if ip != nil {
				id.NetworkIP = ip.String()
				return id
			}
		}
	}
	return id
}

// --- Banco de dados ---

func initDB(dbPath string) (*sql.DB, error) {
	if err := os.MkdirAll(
		strings.TrimSuffix(dbPath, "/tracker.db"),
		0755,
	); err != nil {
		return nil, fmt.Errorf("criar diretório do DB: %w", err)
	}
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("abrir SQLite: %w", err)
	}
	if _, err := db.Exec(createTableSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("criar tabela: %w", err)
	}
	// Migração: adicionar colunas de identidade em bancos já existentes (ignoramos erro se já existirem).
	_, _ = db.Exec("ALTER TABLE activity ADD COLUMN user_name TEXT NOT NULL DEFAULT ''")
	_, _ = db.Exec("ALTER TABLE activity ADD COLUMN hostname TEXT NOT NULL DEFAULT ''")
	_, _ = db.Exec("ALTER TABLE activity ADD COLUMN local_ip TEXT NOT NULL DEFAULT ''")
	_, _ = db.Exec("ALTER TABLE activity ADD COLUMN network_ip TEXT NOT NULL DEFAULT ''")
	// Migração: remover is_synced em bancos antigos (registros passam a ser apagados após envio).
	_, _ = db.Exec("ALTER TABLE activity DROP COLUMN is_synced")
	return db, nil
}

// --- Goroutine 1: Monitoramento (loop rápido, com auto-conexão/reconexão X11) ---

func runMonitor(ctx context.Context, db *sql.DB) {
	identity := getMachineIdentity()
	idleThreshold := getIdleThreshold()
	idleProv := X11IdleProvider{}

	var (
		windowProv      WindowProvider
		cleanup         func()
		mu              sync.Mutex
		currentProcess  string
		currentTitle    string
		currentID       int64
		lastConnAttempt time.Time
		loggedWaiting   bool
	)

	ticker := time.NewTicker(monitorInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			if cleanup != nil {
				cleanup()
			}
			return
		case <-ticker.C:
			if windowProv == nil {
				if !lastConnAttempt.IsZero() && time.Since(lastConnAttempt) < connectRetryInterval {
					continue
				}
				lastConnAttempt = time.Now()
				os.Unsetenv("DISPLAY")
				wp, cl, err := NewX11WindowProvider()
				if err != nil {
					if !loggedWaiting {
						log.Printf("[monitor] aguardando sessão X11 (%v), retentando a cada %s", err, connectRetryInterval)
						loggedWaiting = true
					}
					continue
				}
				windowProv = wp
				cleanup = cl
				loggedWaiting = false
				log.Printf("[monitor] conectado ao X11 (DISPLAY=%s)", os.Getenv("DISPLAY"))
			}

			idleDur, err := idleProv.IdleDuration()
			if err != nil {
				continue
			}
			if idleDur > idleThreshold {
				continue
			}

			processName, windowTitle, err := windowProv.ActiveWindow()
			if err != nil {
				log.Printf("[monitor] conexão X11 perdida: %v — reconectando...", err)
				if cleanup != nil {
					cleanup()
				}
				windowProv = nil
				cleanup = nil
				mu.Lock()
				currentID = 0
				currentProcess = ""
				currentTitle = ""
				mu.Unlock()
				continue
			}
			if processName == "" && windowTitle == "" {
				continue
			}
			if processName == "" {
				processName = "unknown"
			}

			now := time.Now()

			mu.Lock()
			sameWindow := currentProcess == processName && currentTitle == windowTitle
			if sameWindow && currentID != 0 {
				_, _ = db.Exec(
					`UPDATE activity SET end_time = ? WHERE id = ?`,
					now.Format(time.RFC3339), currentID,
				)
			} else {
				if currentID != 0 {
					_, _ = db.Exec(
						`UPDATE activity SET end_time = ? WHERE id = ?`,
						now.Format(time.RFC3339), currentID,
					)
				}
				res, err := db.Exec(
					`INSERT INTO activity (process_name, window_title, start_time, end_time, user_name, hostname, local_ip, network_ip) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
					processName, windowTitle, now.Format(time.RFC3339), now.Format(time.RFC3339),
					identity.UserName, identity.Hostname, identity.LocalIP, identity.NetworkIP,
				)
				if err != nil {
					mu.Unlock()
					continue
				}
				currentID, _ = res.LastInsertId()
				currentProcess = processName
				currentTitle = windowTitle
			}
			mu.Unlock()
		}
	}
}

// --- Goroutine 2: Sincronização (loop lento com Ticker) ---

func runSync(ctx context.Context, db *sql.DB) {
	ticker := time.NewTicker(syncInterval)
	defer ticker.Stop()
	ttl := getTTL()
	ingestURL := getIngestURL()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// TTL na aplicação: SQLite não tem TTL nativo; removemos registros mais antigos que o limite
			// (ex.: API fora do ar por muito tempo — evita tabela crescer indefinidamente).
			if ttl > 0 {
				limit := time.Now().Add(-ttl).Format(time.RFC3339)
				if res, err := db.Exec(`DELETE FROM activity WHERE start_time < ?`, limit); err == nil {
					if n, _ := res.RowsAffected(); n > 0 {
						log.Printf("[sync] TTL: removidos %d registro(s) mais antigos que %s", n, limit)
					}
				}
			}

			rows, err := db.Query(
				`SELECT id, process_name, window_title, start_time, end_time, user_name, hostname, local_ip, network_ip FROM activity ORDER BY id`,
			)
			if err != nil {
				log.Printf("[sync] query: %v", err)
				continue
			}
			var records []Record
			for rows.Next() {
				var r Record
				var startStr, endStr string
				if err := rows.Scan(&r.ID, &r.ProcessName, &r.WindowTitle, &startStr, &endStr, &r.UserName, &r.Hostname, &r.LocalIP, &r.NetworkIP); err != nil {
					continue
				}
				r.StartTime, _ = time.Parse(time.RFC3339, startStr)
				r.EndTime, _ = time.Parse(time.RFC3339, endStr)
				records = append(records, r)
			}
			rows.Close()

			if len(records) == 0 {
				continue
			}

			events := make([]IngestEvent, len(records))
			ids := make([]int64, len(records))
			for i := range records {
				events[i] = IngestEvent{
					ProcessName: records[i].ProcessName,
					WindowTitle: records[i].WindowTitle,
					StartTime:   records[i].StartTime.Format(time.RFC3339),
					EndTime:     records[i].EndTime.Format(time.RFC3339),
					UserName:    records[i].UserName,
					Hostname:    records[i].Hostname,
					LocalIP:     records[i].LocalIP,
					NetworkIP:   records[i].NetworkIP,
				}
				ids[i] = records[i].ID
			}
			payload := IngestPayload{Events: events}
			body, _ := json.Marshal(payload)

			req, err := http.NewRequestWithContext(ctx, http.MethodPost, ingestURL, bytes.NewReader(body))
			if err != nil {
				log.Printf("[sync] new request: %v", err)
				continue
			}
			req.Header.Set("Content-Type", "application/json")

			client := &http.Client{Timeout: 30 * time.Second}
			resp, err := client.Do(req)
			if err != nil {
				log.Printf("[sync] POST falhou (rede): %v", err)
				continue
			}
			resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				log.Printf("[sync] API retornou %d", resp.StatusCode)
				continue
			}

			// Após envio bem-sucedido, remove os registros locais (não há motivo para mantê-los).
			placeholders := make([]string, len(ids))
			args := make([]interface{}, len(ids))
			for i, id := range ids {
				placeholders[i] = "?"
				args[i] = id
			}
			_, _ = db.Exec("DELETE FROM activity WHERE id IN ("+strings.Join(placeholders, ",")+")", args...)
		}
	}
}

// --- Main: inicia goroutines e espera sinal ---

func main() {
	dbPath := os.Getenv("TRACKER_DB_PATH")
	if dbPath == "" {
		dbPath = defaultDBPath()
	}

	db, err := initDB(dbPath)
	if err != nil {
		log.Fatalf("init DB: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runMonitor(ctx, db)
	go runSync(ctx, db)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	cancel()
}

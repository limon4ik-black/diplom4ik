package db

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"logflow/internal/config"
)

// CHClient — клиент к ClickHouse через HTTP интерфейс (порт 8123)
// Не требует внешних зависимостей — только стандартная библиотека Go
// ClickHouse полностью поддерживает SQL через HTTP: POST /  с телом = SQL запрос
type CHClient struct {
	baseURL  string
	user     string
	password string
	db       string
	client   *http.Client
}

func Connect(cfg config.ClickHouseConfig) (*CHClient, error) {
	c := &CHClient{
		baseURL:  "http://" + cfg.Addr,  // HTTP порт 8123
		user:     cfg.User,
		password: cfg.Password,
		db:       cfg.DB,
		client:   &http.Client{Timeout: 30 * time.Second},
	}

	// Retry пока ClickHouse стартует
	for i := range 10 {
		if err := c.Ping(context.Background()); err == nil {
			log.Println("clickhouse: connected")
			return c, nil
		} else {
			log.Printf("clickhouse not ready (%d/10): %v", i+1, err)
			time.Sleep(3 * time.Second)
		}
	}
	return nil, fmt.Errorf("clickhouse: failed to connect after retries")
}

// Exec — выполняет DDL или INSERT (не возвращает строки)
func (c *CHClient) Exec(ctx context.Context, query string, args ...any) error {
	q := fmt.Sprintf(query, args...)
	_, err := c.do(ctx, q)
	return err
}

// Query — выполняет SELECT, возвращает строки как [][]string
// ClickHouse HTTP отдаёт TSV по умолчанию — парсим вручную
func (c *CHClient) Query(ctx context.Context, query string) ([][]string, error) {
	body, err := c.do(ctx, query+" FORMAT TSV")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(body) == "" {
		return nil, nil
	}
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	rows := make([][]string, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		rows = append(rows, strings.Split(line, "\t"))
	}
	return rows, nil
}

// QueryRow — выполняет SELECT и возвращает первую строку
func (c *CHClient) QueryRow(ctx context.Context, query string) ([]string, error) {
	rows, err := c.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0], nil
}

func (c *CHClient) Ping(ctx context.Context) error {
	resp, err := c.client.Get(c.baseURL + "/ping")
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (c *CHClient) Close() {}

func (c *CHClient) do(ctx context.Context, query string) (string, error) {
	params := url.Values{}
	params.Set("database", c.db)
	params.Set("user", c.user)
	if c.password != "" {
		params.Set("password", c.password)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/?"+params.Encode(),
		strings.NewReader(query))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "text/plain")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("clickhouse request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("clickhouse error %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return string(body), nil
}

// RunMigrations — читает SQL файл и применяет каждый statement
func RunMigrations(conn *CHClient, migrationFile string) error {
	sql, err := os.ReadFile(migrationFile)
	if err != nil {
		return fmt.Errorf("read migration: %w", err)
	}

	statements := splitStatements(string(sql))
	ctx := context.Background()
	for _, stmt := range statements {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if err := conn.Exec(ctx, "%s", stmt); err != nil {
			return fmt.Errorf("migration failed:\n%s\nerror: %w", stmt[:min(200, len(stmt))], err)
		}
	}
	log.Println("migrations: applied successfully")
	return nil
}

func splitStatements(sql string) []string {
	var stmts []string
	var cur strings.Builder
	for _, line := range strings.Split(sql, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		cur.WriteString(line)
		cur.WriteByte('\n')
		if strings.HasSuffix(trimmed, ";") {
			s := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(cur.String()), ";"))
			if s != "" {
				stmts = append(stmts, s)
			}
			cur.Reset()
		}
	}
	return stmts
}

func min(a, b int) int {
	if a < b { return a }
	return b
}

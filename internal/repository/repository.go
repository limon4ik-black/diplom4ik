package repository

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"logflow/internal/db"
	"logflow/internal/model"
)

type Repository struct {
	conn *db.CHClient
}

func New(conn *db.CHClient) *Repository {
	return &Repository{conn: conn}
}

func (r *Repository) UpsertSession(ctx context.Context, id uint64, status string) error {
	now := "now()"
	return r.conn.Exec(ctx,
		"INSERT INTO sessions (id, started_at, status) VALUES (%d, %s, '%s')",
		id, now, status,
	)
}

func (r *Repository) UpdateSessionStatus(ctx context.Context, id uint64, status string) error {
	return r.conn.Exec(ctx,
		"ALTER TABLE sessions UPDATE status = '%s' WHERE id = %d",
		status, id,
	)
}

func (r *Repository) GetSession(ctx context.Context, id uint64) (*model.Session, error) {
	row, err := r.conn.QueryRow(ctx,
		fmt.Sprintf("SELECT id, status, started_at, ended_at FROM sessions FINAL WHERE id = %d", id))
	if err != nil || row == nil {
		return nil, err
	}
	s := &model.Session{
		Status:    row[1],
		StartedAt: row[2],
	}
	s.ID, _ = strconv.ParseUint(row[0], 10, 64)
	if row[3] != "" && row[3] != "\\N" {
		s.EndedAt = &row[3]
	}
	return s, nil
}

func (r *Repository) InsertSignalLogs(ctx context.Context, logs []model.SignalLog) error {
	if len(logs) == 0 {
		return nil
	}
	var sb strings.Builder
	sb.WriteString("INSERT INTO signal_logs (session_id, restart_id, element, parameter, model_time, value) VALUES ")
	for i, l := range logs {
		if i > 0 {
			sb.WriteString(", ")
		}
		fmt.Fprintf(&sb, "(%d, %d, %s, %s, %f, %f)",
			l.SessionID, l.RestartID,
			chStr(l.Element), chStr(l.Parameter),
			l.ModelTime, l.Value,
		)
	}
	return r.conn.Exec(ctx, "%s", sb.String())
}

func (r *Repository) InsertEventLogs(ctx context.Context, logs []model.EventLog) error {
	if len(logs) == 0 {
		return nil
	}
	var sb strings.Builder
	sb.WriteString("INSERT INTO event_logs (session_id, restart_id, username, event_type, model_time, data) VALUES ")
	for i, l := range logs {
		if i > 0 {
			sb.WriteString(", ")
		}
		data := l.Data
		if data == "" {
			data = "{}"
		}
		fmt.Fprintf(&sb, "(%d, %d, %s, %s, %f, %s)",
			l.SessionID, l.RestartID,
			chStr(l.Username), chStr(l.EventType),
			l.ModelTime, chStr(data),
		)
	}
	return r.conn.Exec(ctx, "%s", sb.String())
}

func (r *Repository) GetSignalLogs(ctx context.Context, p model.GetLogsParams) ([]model.SignalLog, uint64, error) {
	if p.Limit <= 0 || p.Limit > 10000 {
		p.Limit = 1000
	}
	where := fmt.Sprintf("session_id = %d", p.SessionID)
	if p.RestartID != nil {
		where += fmt.Sprintf(" AND restart_id = %d", *p.RestartID)
	}
	row, err := r.conn.QueryRow(ctx, fmt.Sprintf("SELECT count() FROM signal_logs WHERE %s", where))
	if err != nil {
		return nil, 0, err
	}
	var total uint64
	if row != nil {
		total, _ = strconv.ParseUint(row[0], 10, 64)
	}
	rows, err := r.conn.Query(ctx, fmt.Sprintf(
		"SELECT session_id, restart_id, element, parameter, model_time, value FROM signal_logs WHERE %s ORDER BY restart_id, model_time LIMIT %d OFFSET %d",
		where, p.Limit, p.Offset,
	))
	if err != nil {
		return nil, 0, err
	}
	var logs []model.SignalLog
	for _, row := range rows {
		if len(row) < 6 {
			continue
		}
		sid, _ := strconv.ParseUint(row[0], 10, 64)
		rid, _ := strconv.ParseUint(row[1], 10, 64)
		mt, _ := strconv.ParseFloat(row[4], 64)
		val, _ := strconv.ParseFloat(row[5], 64)
		logs = append(logs, model.SignalLog{
			SessionID: sid, RestartID: rid,
			Element: row[2], Parameter: row[3],
			ModelTime: mt, Value: val,
		})
	}
	return logs, total, nil
}

func (r *Repository) GetEventLogs(ctx context.Context, p model.GetLogsParams) ([]model.EventLog, uint64, error) {
	if p.Limit <= 0 || p.Limit > 10000 {
		p.Limit = 1000
	}
	where := fmt.Sprintf("session_id = %d", p.SessionID)
	if p.RestartID != nil {
		where += fmt.Sprintf(" AND restart_id = %d", *p.RestartID)
	}
	row, err := r.conn.QueryRow(ctx, fmt.Sprintf("SELECT count() FROM event_logs WHERE %s", where))
	if err != nil {
		return nil, 0, err
	}
	var total uint64
	if row != nil {
		total, _ = strconv.ParseUint(row[0], 10, 64)
	}
	rows, err := r.conn.Query(ctx, fmt.Sprintf(
		"SELECT id, session_id, restart_id, username, event_type, model_time, data FROM event_logs WHERE %s ORDER BY restart_id, model_time LIMIT %d OFFSET %d",
		where, p.Limit, p.Offset,
	))
	if err != nil {
		return nil, 0, err
	}
	var logs []model.EventLog
	for _, row := range rows {
		if len(row) < 7 {
			continue
		}
		sid, _ := strconv.ParseUint(row[1], 10, 64)
		rid, _ := strconv.ParseUint(row[2], 10, 64)
		mt, _ := strconv.ParseFloat(row[5], 64)
		logs = append(logs, model.EventLog{
			ID: row[0], SessionID: sid, RestartID: rid,
			Username: row[3], EventType: row[4],
			ModelTime: mt, Data: row[6],
		})
	}
	return logs, total, nil
}

// DeleteSessionLogs — удаляет логи из ClickHouse но оставляет метаданные сессии
// (нужно чтобы знать статус archived при последующих запросах)
func (r *Repository) DeleteSessionLogs(ctx context.Context, sessionID uint64) error {
	sid := fmt.Sprintf("%d", sessionID)
	if err := r.conn.Exec(ctx, "ALTER TABLE signal_logs DROP PARTITION '%s'", sid); err != nil {
		return fmt.Errorf("drop signal_logs: %w", err)
	}
	if err := r.conn.Exec(ctx, "ALTER TABLE event_logs DROP PARTITION '%s'", sid); err != nil {
		return fmt.Errorf("drop event_logs: %w", err)
	}
	return nil
}

// DeleteSession — полное удаление: логи + метаданные + архивные файлы (вызывается из сервиса)
func (r *Repository) DeleteSession(ctx context.Context, sessionID uint64) error {
	if err := r.DeleteSessionLogs(ctx, sessionID); err != nil {
		return err
	}
	return r.conn.Exec(ctx, "ALTER TABLE sessions DELETE WHERE id = %d", sessionID)
}

func chStr(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "\\'") + "'"
}

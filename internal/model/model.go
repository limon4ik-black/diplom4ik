package model

import "encoding/json"

type WSMessage struct {
	Type string          `json:"type"` // "signal" | "event" | "session_end"
	Data json.RawMessage `json:"data"`
}

// WSSessionEnd — сообщение об окончании сессии
type WSSessionEnd struct {
	SessionID uint64 `json:"session_id"`
}

// SignalLog — значение параметра модели в момент времени
type SignalLog struct {
	SessionID uint64  `json:"session_id"`
	RestartID uint64  `json:"restart_id"`
	Element   string  `json:"element"`
	Parameter string  `json:"parameter"`
	ModelTime float64 `json:"model_time"`
	Value     float64 `json:"value"`
}

// EventLog — действие оператора или системное событие
type EventLog struct {
	ID        string  `json:"id,omitempty"`
	SessionID uint64  `json:"session_id"`
	RestartID uint64  `json:"restart_id"`
	Username  string  `json:"username,omitempty"`
	EventType string  `json:"event_type"`
	ModelTime float64 `json:"model_time"`
	Data      string  `json:"data"` // JSON строка
}

// Session — метаданные сессии
type Session struct {
	ID        uint64  `json:"id"`
	Status    string  `json:"status"`
	StartedAt string  `json:"started_at"`
	EndedAt   *string `json:"ended_at,omitempty"`
}

// -------------------------------------------------------
// Параметры запросов
// -------------------------------------------------------

type GetLogsParams struct {
	SessionID uint64
	RestartID *uint64
	Limit     int
	Offset    int
}

// -------------------------------------------------------
// Ответы API
// -------------------------------------------------------

type SignalLogsResponse struct {
	SessionID uint64      `json:"session_id"`
	Logs      []SignalLog `json:"logs"`
	Total     uint64      `json:"total"`
}

type EventLogsResponse struct {
	SessionID uint64     `json:"session_id"`
	Logs      []EventLog `json:"logs"`
	Total     uint64     `json:"total"`
}

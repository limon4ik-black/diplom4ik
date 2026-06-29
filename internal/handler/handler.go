package handler

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/gorilla/websocket"
	"logflow/internal/model"
	"logflow/internal/service"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// В продакшне нужно проверять Origin
	CheckOrigin: func(r *http.Request) bool { return true },
}

type Handler struct {
	svc    *service.Service
	apiKey string
}

func New(svc *service.Service, apiKey string) *Handler {
	return &Handler{svc: svc, apiKey: apiKey}
}

func (h *Handler) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.With(middleware.Timeout(60*time.Second)).Get("/health", h.health)

	r.Group(func(r chi.Router) {
		r.Use(h.authMiddleware)

		// WebSocket — тренажёр подключается сюда и шлёт логи
		r.Get("/sessions/{session_id}/stream", h.wsStream)

		r.Group(func(r chi.Router) {
			r.Use(middleware.Timeout(60 * time.Second))

			// REST — управление сессиями
			r.Post("/sessions/{session_id}/archive", h.archiveSession)
			r.Delete("/sessions/{session_id}", h.deleteSession)

			// REST — получение логов
			r.Get("/sessions/{session_id}/signal-logs", h.getSignalLogs)
			r.Get("/sessions/{session_id}/event-logs", h.getEventLogs)
		})
	})

	return r
}

// -------------------------------------------------------
// WebSocket — главный эндпоинт
// -------------------------------------------------------

// wsStream обрабатывает WS соединение от тренажёра.
// Тренажёр подключается, шлёт JSON сообщения с логами,
// при завершении сессии шлёт {"type":"session_end",...} и закрывает соединение.
func (h *Handler) wsStream(w http.ResponseWriter, r *http.Request) {
	sessionID, err := parseSessionID(r)
	if err != nil {
		http.Error(w, "invalid session_id", http.StatusBadRequest)
		return
	}

	// Апгрейд HTTP → WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade error session=%d: %v", sessionID, err)
		return
	}
	defer conn.Close()

	log.Printf("ws: session=%d connected from %s", sessionID, r.RemoteAddr)

	ctx := r.Context()

	// Регистрируем сессию в БД
	if err := h.svc.StartSession(ctx, sessionID); err != nil {
		log.Printf("ws: start session=%d error: %v", sessionID, err)
		return
	}

	// Пинг каждые 30с чтобы соединение не упало
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	go pingLoop(conn)

	// Основной цикл чтения сообщений
	for {
		_, msgBytes, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("ws: session=%d unexpected close: %v", sessionID, err)
			}
			break
		}

		var msg model.WSMessage
		if err := json.Unmarshal(msgBytes, &msg); err != nil {
			log.Printf("ws: session=%d bad message: %v", sessionID, err)
			continue
		}

		switch msg.Type {
		case "signal":
			var l model.SignalLog
			if err := json.Unmarshal(msg.Data, &l); err != nil {
				log.Printf("ws: bad signal: %v", err)
				continue
			}
			l.SessionID = sessionID // перезаписываем из URL для безопасности
			h.svc.AddSignalLog(ctx, l)

		case "event":
			var l model.EventLog
			if err := json.Unmarshal(msg.Data, &l); err != nil {
				log.Printf("ws: bad event: %v", err)
				continue
			}
			l.SessionID = sessionID
			h.svc.AddEventLog(ctx, l)

		case "session_end":
			log.Printf("ws: session=%d finished", sessionID)
			if err := h.svc.EndSession(ctx, sessionID); err != nil {
				log.Printf("ws: end session=%d error: %v", sessionID, err)
			}
			return

		default:
			log.Printf("ws: session=%d unknown type: %s", sessionID, msg.Type)
		}
	}

	// Соединение закрылось без session_end — всё равно сбрасываем буфер
	_ = h.svc.EndSession(context.Background(), sessionID)
	log.Printf("ws: session=%d disconnected", sessionID)
}

func pingLoop(conn *websocket.Conn) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
			return
		}
	}
}

// -------------------------------------------------------
// REST handlers
// -------------------------------------------------------

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// POST /sessions/{session_id}/archive
func (h *Handler) archiveSession(w http.ResponseWriter, r *http.Request) {
	sessionID, err := parseSessionID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid session_id")
		return
	}

	if err := h.svc.ArchiveSession(r.Context(), sessionID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": sessionID,
		"status":     "archived",
	})
}

// DELETE /sessions/{session_id}
func (h *Handler) deleteSession(w http.ResponseWriter, r *http.Request) {
	sessionID, err := parseSessionID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid session_id")
		return
	}

	if err := h.svc.DeleteSession(r.Context(), sessionID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": sessionID,
		"deleted":    true,
	})
}

// GET /sessions/{session_id}/signal-logs?limit=1000&offset=0&restart_id=3
func (h *Handler) getSignalLogs(w http.ResponseWriter, r *http.Request) {
	sessionID, err := parseSessionID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid session_id")
		return
	}

	p := model.GetLogsParams{
		SessionID: sessionID,
		Limit:     parseIntDefault(r.URL.Query().Get("limit"), 1000),
		Offset:    parseIntDefault(r.URL.Query().Get("offset"), 0),
	}
	if rid := r.URL.Query().Get("restart_id"); rid != "" {
		if v, err := strconv.ParseUint(rid, 10, 64); err == nil {
			p.RestartID = &v
		}
	}

	resp, err := h.svc.GetSignalLogs(r.Context(), p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// GET /sessions/{session_id}/event-logs?limit=1000&offset=0&restart_id=3
func (h *Handler) getEventLogs(w http.ResponseWriter, r *http.Request) {
	sessionID, err := parseSessionID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid session_id")
		return
	}

	p := model.GetLogsParams{
		SessionID: sessionID,
		Limit:     parseIntDefault(r.URL.Query().Get("limit"), 1000),
		Offset:    parseIntDefault(r.URL.Query().Get("offset"), 0),
	}
	if rid := r.URL.Query().Get("restart_id"); rid != "" {
		if v, err := strconv.ParseUint(rid, 10, 64); err == nil {
			p.RestartID = &v
		}
	}

	resp, err := h.svc.GetEventLogs(r.Context(), p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// -------------------------------------------------------
// Middleware + helpers
// -------------------------------------------------------

func (h *Handler) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// WS запросы передают ключ через query param (браузер не может слать заголовки)
		key := r.URL.Query().Get("api_key")
		if key == "" {
			key = r.Header.Get("X-API-Key")
		}
		if key == "" {
			auth := r.Header.Get("Authorization")
			if len(auth) > 7 && auth[:7] == "Bearer " {
				key = auth[7:]
			}
		}
		if key != h.apiKey {
			writeError(w, http.StatusUnauthorized, "invalid or missing API key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func parseSessionID(r *http.Request) (uint64, error) {
	return strconv.ParseUint(chi.URLParam(r, "session_id"), 10, 64)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func parseIntDefault(s string, fallback int) int {
	if s == "" {
		return fallback
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < 0 {
		return fallback
	}
	return v
}

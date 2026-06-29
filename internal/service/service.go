package service

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"logflow/internal/config"
	"logflow/internal/model"
	"logflow/internal/repository"
)

const (
	flushSize     = 500
	flushInterval = time.Second
)

// sessionStatus — статус сессии в памяти (не ждём ClickHouse мутации)
type sessionStatus struct {
	archived bool
}

type Service struct {
	repo       *repository.Repository
	archiveDir string

	// Статусы сессий в памяти — быстро, без задержек ClickHouse
	statusMu sync.RWMutex
	statuses map[uint64]*sessionStatus

	// Буферы логов по сессиям
	signalMu  sync.Mutex
	signalBuf map[uint64][]model.SignalLog

	eventMu  sync.Mutex
	eventBuf map[uint64][]model.EventLog
}

func New(repo *repository.Repository, cfg config.ArchiveConfig) *Service {
	s := &Service{
		repo:       repo,
		archiveDir: cfg.Dir,
		statuses:   make(map[uint64]*sessionStatus),
		signalBuf:  make(map[uint64][]model.SignalLog),
		eventBuf:   make(map[uint64][]model.EventLog),
	}
	go s.flushLoop()
	return s
}

// -------------------------------------------------------
// WebSocket — приём логов
// -------------------------------------------------------

func (s *Service) StartSession(ctx context.Context, sessionID uint64) error {
	s.statusMu.Lock()
	s.statuses[sessionID] = &sessionStatus{archived: false}
	s.statusMu.Unlock()
	return s.repo.UpsertSession(ctx, sessionID, "active")
}

func (s *Service) AddSignalLog(ctx context.Context, l model.SignalLog) {
	s.signalMu.Lock()
	s.signalBuf[l.SessionID] = append(s.signalBuf[l.SessionID], l)
	size := len(s.signalBuf[l.SessionID])
	s.signalMu.Unlock()

	if size >= flushSize {
		s.flushSignals(ctx, l.SessionID)
	}
}

func (s *Service) AddEventLog(ctx context.Context, l model.EventLog) {
	s.eventMu.Lock()
	s.eventBuf[l.SessionID] = append(s.eventBuf[l.SessionID], l)
	size := len(s.eventBuf[l.SessionID])
	s.eventMu.Unlock()

	if size >= flushSize {
		s.flushEvents(ctx, l.SessionID)
	}
}

func (s *Service) EndSession(ctx context.Context, sessionID uint64) error {
	s.flushSignals(ctx, sessionID)
	s.flushEvents(ctx, sessionID)
	return s.repo.UpdateSessionStatus(ctx, sessionID, "active")
}

// -------------------------------------------------------
// Получение логов
// -------------------------------------------------------

func (s *Service) GetSignalLogs(ctx context.Context, p model.GetLogsParams) (*model.SignalLogsResponse, error) {
	s.flushSignals(ctx, p.SessionID)

	if s.isArchived(p.SessionID) {
		return s.readSignalsFromArchive(p.SessionID)
	}

	logs, total, err := s.repo.GetSignalLogs(ctx, p)
	if err != nil {
		return nil, err
	}
	return &model.SignalLogsResponse{SessionID: p.SessionID, Logs: logs, Total: total}, nil
}

func (s *Service) GetEventLogs(ctx context.Context, p model.GetLogsParams) (*model.EventLogsResponse, error) {
	s.flushEvents(ctx, p.SessionID)

	if s.isArchived(p.SessionID) {
		return s.readEventsFromArchive(p.SessionID)
	}

	logs, total, err := s.repo.GetEventLogs(ctx, p)
	if err != nil {
		return nil, err
	}
	return &model.EventLogsResponse{SessionID: p.SessionID, Logs: logs, Total: total}, nil
}

// isArchived — проверяет статус сначала в памяти, потом в БД
func (s *Service) isArchived(sessionID uint64) bool {
	s.statusMu.RLock()
	st, ok := s.statuses[sessionID]
	s.statusMu.RUnlock()

	if ok {
		return st.archived
	}

	// Сервис перезапустился — читаем из БД
	sess, err := s.repo.GetSession(context.Background(), sessionID)
	if err != nil || sess == nil {
		return false
	}

	archived := sess.Status == "archived"
	s.statusMu.Lock()
	s.statuses[sessionID] = &sessionStatus{archived: archived}
	s.statusMu.Unlock()

	return archived
}

// -------------------------------------------------------
// Архивирование
// -------------------------------------------------------

func (s *Service) ArchiveSession(ctx context.Context, sessionID uint64) error {
	s.flushSignals(ctx, sessionID)
	s.flushEvents(ctx, sessionID)

	if err := os.MkdirAll(s.archiveDir, 0755); err != nil {
		return fmt.Errorf("create archive dir: %w", err)
	}

	if err := s.archiveSignals(ctx, sessionID); err != nil {
		return fmt.Errorf("archive signals: %w", err)
	}
	if err := s.archiveEvents(ctx, sessionID); err != nil {
		return fmt.Errorf("archive events: %w", err)
	}

	// Помечаем в памяти — мгновенно, без задержки ClickHouse мутации
	s.statusMu.Lock()
	s.statuses[sessionID] = &sessionStatus{archived: true}
	s.statusMu.Unlock()

	// Асинхронно обновляем статус в БД и удаляем логи
	go func() {
		bgCtx := context.Background()
		if err := s.repo.UpdateSessionStatus(bgCtx, sessionID, "archived"); err != nil {
			log.Printf("WARN: update session status: %v", err)
		}
		if err := s.repo.DeleteSessionLogs(bgCtx, sessionID); err != nil {
			log.Printf("WARN: delete session logs: %v", err)
		}
	}()

	return nil
}

func (s *Service) archiveSignals(ctx context.Context, sessionID uint64) error {
	path := filepath.Join(s.archiveDir, fmt.Sprintf("signal_logs_%d.jsonl.gz", sessionID))
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	enc := json.NewEncoder(gz)

	offset := 0
	for {
		logs, _, err := s.repo.GetSignalLogs(ctx, model.GetLogsParams{
			SessionID: sessionID, Limit: 5000, Offset: offset,
		})
		if err != nil {
			return err
		}
		if len(logs) == 0 {
			break
		}
		for _, l := range logs {
			if err := enc.Encode(l); err != nil {
				return err
			}
		}
		offset += len(logs)
	}
	log.Printf("archived signal_logs session=%d → %s (%d records)", sessionID, path, offset)
	return nil
}

func (s *Service) archiveEvents(ctx context.Context, sessionID uint64) error {
	path := filepath.Join(s.archiveDir, fmt.Sprintf("event_logs_%d.jsonl.gz", sessionID))
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	enc := json.NewEncoder(gz)

	offset := 0
	for {
		logs, _, err := s.repo.GetEventLogs(ctx, model.GetLogsParams{
			SessionID: sessionID, Limit: 5000, Offset: offset,
		})
		if err != nil {
			return err
		}
		if len(logs) == 0 {
			break
		}
		for _, l := range logs {
			if err := enc.Encode(l); err != nil {
				return err
			}
		}
		offset += len(logs)
	}
	log.Printf("archived event_logs session=%d → %s (%d records)", sessionID, path, offset)
	return nil
}

// -------------------------------------------------------
// Чтение из архива
// -------------------------------------------------------

func (s *Service) readSignalsFromArchive(sessionID uint64) (*model.SignalLogsResponse, error) {
	path := filepath.Join(s.archiveDir, fmt.Sprintf("signal_logs_%d.jsonl.gz", sessionID))
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("archive file not found for session %d", sessionID)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()

	var logs []model.SignalLog
	dec := json.NewDecoder(gz)
	for dec.More() {
		var l model.SignalLog
		if err := dec.Decode(&l); err != nil {
			return nil, err
		}
		logs = append(logs, l)
	}
	return &model.SignalLogsResponse{
		SessionID: sessionID,
		Logs:      logs,
		Total:     uint64(len(logs)),
	}, nil
}

func (s *Service) readEventsFromArchive(sessionID uint64) (*model.EventLogsResponse, error) {
	path := filepath.Join(s.archiveDir, fmt.Sprintf("event_logs_%d.jsonl.gz", sessionID))
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("archive file not found for session %d", sessionID)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()

	var logs []model.EventLog
	dec := json.NewDecoder(gz)
	for dec.More() {
		var l model.EventLog
		if err := dec.Decode(&l); err != nil {
			return nil, err
		}
		logs = append(logs, l)
	}
	return &model.EventLogsResponse{
		SessionID: sessionID,
		Logs:      logs,
		Total:     uint64(len(logs)),
	}, nil
}

// -------------------------------------------------------
// Удаление
// -------------------------------------------------------

func (s *Service) DeleteSession(ctx context.Context, sessionID uint64) error {
	if err := s.repo.DeleteSession(ctx, sessionID); err != nil {
		return err
	}

	// Удаляем архивные файлы если есть
	os.Remove(filepath.Join(s.archiveDir, fmt.Sprintf("signal_logs_%d.jsonl.gz", sessionID)))
	os.Remove(filepath.Join(s.archiveDir, fmt.Sprintf("event_logs_%d.jsonl.gz", sessionID)))

	// Чистим из памяти
	s.signalMu.Lock()
	delete(s.signalBuf, sessionID)
	s.signalMu.Unlock()

	s.eventMu.Lock()
	delete(s.eventBuf, sessionID)
	s.eventMu.Unlock()

	s.statusMu.Lock()
	delete(s.statuses, sessionID)
	s.statusMu.Unlock()

	return nil
}

// -------------------------------------------------------
// Буферизация
// -------------------------------------------------------

func (s *Service) flushLoop() {
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	for range ticker.C {
		ctx := context.Background()
		s.signalMu.Lock()
		ids := make([]uint64, 0, len(s.signalBuf))
		for id := range s.signalBuf {
			ids = append(ids, id)
		}
		s.signalMu.Unlock()
		for _, id := range ids {
			s.flushSignals(ctx, id)
			s.flushEvents(ctx, id)
		}
	}
}

func (s *Service) flushSignals(ctx context.Context, sessionID uint64) {
	s.signalMu.Lock()
	if len(s.signalBuf[sessionID]) == 0 {
		s.signalMu.Unlock()
		return
	}
	cp := make([]model.SignalLog, len(s.signalBuf[sessionID]))
	copy(cp, s.signalBuf[sessionID])
	s.signalBuf[sessionID] = s.signalBuf[sessionID][:0]
	s.signalMu.Unlock()

	if err := s.repo.InsertSignalLogs(ctx, cp); err != nil {
		log.Printf("ERROR flush signal_logs session=%d: %v", sessionID, err)
	}
}

func (s *Service) flushEvents(ctx context.Context, sessionID uint64) {
	s.eventMu.Lock()
	if len(s.eventBuf[sessionID]) == 0 {
		s.eventMu.Unlock()
		return
	}
	cp := make([]model.EventLog, len(s.eventBuf[sessionID]))
	copy(cp, s.eventBuf[sessionID])
	s.eventBuf[sessionID] = s.eventBuf[sessionID][:0]
	s.eventMu.Unlock()

	if err := s.repo.InsertEventLogs(ctx, cp); err != nil {
		log.Printf("ERROR flush event_logs session=%d: %v", sessionID, err)
	}
}

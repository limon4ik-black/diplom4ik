// cmd/loadtest/main.go
// Симулятор нагрузки — имитирует несколько тренажёров одновременно.
// Каждый тренажёр открывает WS соединение и шлёт логи с заданной скоростью.
//
// Запуск:
//
//	go run ./cmd/loadtest --sessions=5 --signals-per-sec=200 --duration=30s
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// --- флаги ---
var (
	serverURL     = flag.String("server", "ws://localhost:8080", "адрес сервера")
	apiKey        = flag.String("key", "dev-secret-key", "API ключ")
	numSessions   = flag.Int("sessions", 3, "количество одновременных сессий")
	signalsPerSec = flag.Int("signals-per-sec", 100, "сигналов в секунду на сессию")
	eventsPerSec  = flag.Int("events-per-sec", 5, "событий в секунду на сессию")
	duration      = flag.Duration("duration", 20*time.Second, "длительность теста")
	batchSize     = flag.Int("batch", 50, "сколько сигналов за одну отправку")
)

// --- счётчики ---
var (
	totalSignalsSent atomic.Int64
	totalEventsSent  atomic.Int64
	totalErrors      atomic.Int64
)

// Примеры элементов и параметров как в реальном тренажёре
var elements = []string{"H1S3S321", "H1S3S322", "H2S1S101", "T1AEB01", "A1AEB06", "P2SD023K"}
var parameters = []string{"U", "I", "P", "Q", "F", "T", "XH01", "XH02", "XH03"}
var eventTypes = []string{"Alarm", "Notification", "Operation", "Manual", "System"}
var areas = []string{"Котел", "ЭЦ", "ОРУ 110 кВ", "Модель", "Турбина"}
var usernames = []string{"ivanovii", "petrovaa", "sidorovvv", "instructor"}

func main() {
	flag.Parse()

	log.Printf("=== Нагрузочный тест ===")
	log.Printf("Сессий:           %d", *numSessions)
	log.Printf("Сигналов/сек:     %d (на сессию)", *signalsPerSec)
	log.Printf("Событий/сек:      %d (на сессию)", *eventsPerSec)
	log.Printf("Длительность:     %s", *duration)
	log.Printf("Ожидаемый поток:  ~%d сигналов/сек суммарно",
		*numSessions**signalsPerSec)
	log.Printf("========================")

	var wg sync.WaitGroup
	startTime := time.Now()

	for i := 0; i < *numSessions; i++ {
		wg.Add(1)
		sessionID := uint64(1000 + i)
		go func(sid uint64) {
			defer wg.Done()
			runSession(sid, startTime)
		}(sessionID)

		// Небольшой сдвиг чтобы не все подключались одновременно
		time.Sleep(100 * time.Millisecond)
	}

	// Прогресс каждую секунду
	go printProgress(startTime)

	wg.Wait()

	elapsed := time.Since(startTime).Seconds()
	totalSig := totalSignalsSent.Load()
	totalEv := totalEventsSent.Load()
	totalErr := totalErrors.Load()

	log.Printf("\n=== Результаты ===")
	log.Printf("Время:              %.1f сек", elapsed)
	log.Printf("Сигналов отправлено: %d (%.0f/сек)", totalSig, float64(totalSig)/elapsed)
	log.Printf("Событий отправлено:  %d (%.0f/сек)", totalEv, float64(totalEv)/elapsed)
	log.Printf("Ошибок:             %d", totalErr)
	log.Printf("==================")
}

func runSession(sessionID uint64, startTime time.Time) {
	url := fmt.Sprintf("%s/sessions/%d/stream?api_key=%s",
		*serverURL, sessionID, *apiKey)

	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		log.Printf("session=%d: connect error: %v", sessionID, err)
		totalErrors.Add(1)
		return
	}
	defer conn.Close()

	log.Printf("session=%d: connected", sessionID)

	// Читаем служебные WebSocket-кадры, чтобы библиотека отвечала pong
	// на ping сервера и долгие соединения не закрывались по таймауту.
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	deadline := startTime.Add(*duration)
	restartID := uint64(1)
	modelTime := 0.0

	// Интервал между батчами сигналов
	signalInterval := time.Duration(float64(time.Second) *
		float64(*batchSize) / float64(*signalsPerSec))
	signalTicker := time.NewTicker(signalInterval)
	defer signalTicker.Stop()

	eventInterval := time.Second / time.Duration(*eventsPerSec)
	eventTicker := time.NewTicker(eventInterval)
	defer eventTicker.Stop()

	for time.Now().Before(deadline) {
		select {
		case <-signalTicker.C:
			// Отправляем батч сигналов
			for i := 0; i < *batchSize; i++ {
				msg := map[string]any{
					"type": "signal",
					"data": map[string]any{
						"session_id": sessionID,
						"restart_id": restartID,
						"element":    elements[rand.Intn(len(elements))],
						"parameter":  parameters[rand.Intn(len(parameters))],
						"model_time": modelTime,
						"value":      rand.Float64() * 1000,
					},
				}
				modelTime += 0.1

				if err := sendJSON(conn, msg); err != nil {
					totalErrors.Add(1)
					return
				}
				totalSignalsSent.Add(1)
			}

		case <-eventTicker.C:
			// Отправляем событие
			evType := eventTypes[rand.Intn(len(eventTypes))]
			dataJSON, _ := json.Marshal(map[string]any{
				"area":            areas[rand.Intn(len(areas))],
				"tag":             elements[rand.Intn(len(elements))] + ".XH01",
				"tag_description": "Тестовый параметр",
				"message":         fmt.Sprintf("Тестовое событие %s", evType),
				"value":           rand.Float64() * 100,
			})

			msg := map[string]any{
				"type": "event",
				"data": map[string]any{
					"session_id": sessionID,
					"restart_id": restartID,
					"username":   usernames[rand.Intn(len(usernames))],
					"event_type": evType,
					"model_time": modelTime,
					"data":       string(dataJSON),
				},
			}

			if err := sendJSON(conn, msg); err != nil {
				totalErrors.Add(1)
				return
			}
			totalEventsSent.Add(1)
		}
	}

	// Завершаем сессию
	end := map[string]any{
		"type": "session_end",
		"data": map[string]any{"session_id": sessionID},
	}
	sendJSON(conn, end)
	log.Printf("session=%d: done, restart=%d, model_time=%.1fs",
		sessionID, restartID, modelTime)
}

func sendJSON(conn *websocket.Conn, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, data)
}

func printProgress(startTime time.Time) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		elapsed := time.Since(startTime).Seconds()
		sig := totalSignalsSent.Load()
		ev := totalEventsSent.Load()
		errs := totalErrors.Load()
		log.Printf("[%.0fs] сигналов: %d (%.0f/сек) | событий: %d | ошибок: %d",
			elapsed, sig, float64(sig)/elapsed, ev, errs)
	}
}

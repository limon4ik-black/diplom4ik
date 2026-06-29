package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"logflow/internal/config"
	"logflow/internal/db"
	"logflow/internal/handler"
	"logflow/internal/repository"
	"logflow/internal/service"
)

func main() {
	cfg := config.Load()

	conn, err := db.Connect(cfg.ClickHouse)
	if err != nil {
		log.Fatalf("FATAL: clickhouse connect: %v", err)
	}
	defer conn.Close()

	if err := db.RunMigrations(conn, "migrations/001_init.sql"); err != nil {
		log.Fatalf("FATAL: migrations: %v", err)
	}

	repo := repository.New(conn)
	svc := service.New(repo, cfg.Archive)
	h := handler.New(svc, cfg.Server.APIKey)

	srv := &http.Server{
		Addr:         ":" + cfg.Server.Port,
		Handler:      h.Router(),
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
	}

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Println("shutting down...")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()

	log.Printf("logflow started on :%s", cfg.Server.Port)
	log.Printf("ws endpoint: ws://localhost:%s/sessions/{id}/stream?api_key=...", cfg.Server.Port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("FATAL: %v", err)
	}
}

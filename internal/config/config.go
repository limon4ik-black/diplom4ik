package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	Server     ServerConfig
	ClickHouse ClickHouseConfig
	Archive    ArchiveConfig
}

type ServerConfig struct {
	Port         string
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	APIKey       string
}

type ClickHouseConfig struct {
	Addr     string
	DB       string
	User     string
	Password string
}

type ArchiveConfig struct {
	Dir string // куда писать .gz файлы
}

func Load() *Config {
	return &Config{
		Server: ServerConfig{
			Port:         getEnv("SERVER_PORT", "8080"),
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 30 * time.Second,
			APIKey:       getEnv("API_KEY", "dev-secret-key"),
		},
		ClickHouse: ClickHouseConfig{
			Addr:     getEnv("CH_ADDR", "localhost:8123"),
			DB:       getEnv("CH_DB", "logs"),
			User:     getEnv("CH_USER", "default"),
			Password: getEnv("CH_PASSWORD", ""),
		},
		Archive: ArchiveConfig{
			Dir: getEnv("ARCHIVE_DIR", "./archives"),
		},
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}

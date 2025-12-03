package internal

import (
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	Port      string
	HTTPProxy string
}

var Cfg *Config

func LoadConfig() {
	godotenv.Load()

	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}

	httpProxy := os.Getenv("HTTP_PROXY")

	Cfg = &Config{
		Port:      port,
		HTTPProxy: httpProxy,
	}
}

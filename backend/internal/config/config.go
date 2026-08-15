package config

import (
	"fmt"
	"os"
	"strconv"
)

const defaultPort = 8080

type Config struct {
	Port int
}

func Load() (Config, error) {
	port := defaultPort

	if v, ok := os.LookupEnv("PORT"); ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			return Config{}, fmt.Errorf("PORT %q は数値ではない: %w", v, err)
		}
		if n < 1 || n > 65535 {
			return Config{}, fmt.Errorf("PORT %d は 1~65535 の範囲外", n)
		}
		port = n
	}

	return Config{Port: port}, nil
}

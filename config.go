package main

// Настройки программы: settings.json в папке данных. Настройки звука и видео
// интерфейс хранит целиком в ключе "media" - Go их не разбирает.

import (
	"encoding/json"
	"os"
	"sync"
)

var configDefaults = map[string]any{
	"port":          0.0,  // порт окна; постоянный, чтобы браузер помнил устройства и разрешения
	"close_to_tray": true, // крестик прячет окно в трей, звонок продолжается
	"check_updates": true, //
	"last_login":    "",   // какой профиль подставить на экране входа
	"brokers":       "",   // MQTT-брокеры для коротких кодов, по одному в строке; пусто - встроенные
	"media":         M{},  // микрофон, шумоподавление, экран - решает интерфейс
}

type M = map[string]any

type Config struct {
	path string
	mu   sync.Mutex
	v    M
}

func loadConfig(path string) *Config {
	c := &Config{path: path, v: M{}}
	for k, v := range configDefaults {
		c.v[k] = v
	}
	if b, err := os.ReadFile(path); err == nil {
		var saved M
		if json.Unmarshal(b, &saved) == nil {
			for k, v := range saved {
				if _, known := configDefaults[k]; known {
					c.v[k] = v
				}
			}
		}
	}
	return c
}

func (c *Config) All() M {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := M{}
	for k, v := range c.v {
		out[k] = v
	}
	return out
}

func (c *Config) Bool(k string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, _ := c.v[k].(bool)
	return v
}

func (c *Config) Num(k string) float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, _ := c.v[k].(float64)
	return v
}

func (c *Config) Str(k string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, _ := c.v[k].(string)
	return v
}

// Set меняет известные ключи с тем же типом, что у значения по умолчанию.
func (c *Config) Set(changes M) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range changes {
		def, known := configDefaults[k]
		if !known {
			continue
		}
		ok := false
		switch def.(type) {
		case bool:
			_, ok = v.(bool)
		case float64:
			_, ok = v.(float64)
		case string:
			_, ok = v.(string)
		case M:
			_, ok = v.(M)
		}
		if ok {
			c.v[k] = v
		}
	}
	b, err := json.MarshalIndent(c.v, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(c.path, b)
}

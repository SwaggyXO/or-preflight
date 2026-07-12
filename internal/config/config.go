package config

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ModelSpecs struct {
	ID                string
	FriendlyName      string
	InputCostPerM     float64
	OutputCostPerM    float64
	CacheReadCostPerM float64
	MaxContextWindow  int
}

type AppConfig struct {
	ProxyPort        string
	SimulationMode   bool
	OllamaEndpoint   string
	TargetEvalModel  string
	WatchDirectories []string
}

type LiveModelRegistry struct {
	sync.RWMutex
	Models map[string]ModelSpecs
}

var Registry = &LiveModelRegistry{
	Models: make(map[string]ModelSpecs),
}

func SynchronizeUpstreamRates() {
	fmt.Println("[Config] Synchronizing live model catalog from OpenRouter edge...")
	
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get("https://openrouter.ai/api/v1/models")
	if err != nil {
		fmt.Printf("[CRITICAL] Failed to fetch live pricing metrics from OpenRouter: %v. Server running with zero authorized models.\n", err)
		return
	}
	defer resp.Body.Close()

	type APIModelPricing struct {
		Prompt         string `json:"prompt"`
		Completion     string `json:"completion"`
		InputCacheRead string `json:"input_cache_read,omitempty"`
	}
	type APIModelData struct {
		ID            string          `json:"id"`
		Name          string          `json:"name"`
		ContextLength int             `json:"context_length"`
		Pricing       APIModelPricing `json:"pricing"`
	}
	type APIResponse struct {
		Data []APIModelData `json:"data"`
	}

	var payload APIResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		fmt.Printf("[CRITICAL] Corrupted catalog schema returned from OpenRouter edge: %v. Registration aborted.\n", err)
		return
	}

	Registry.Lock()
	defer Registry.Unlock()

	for _, m := range payload.Data {
		inPrice, _ := strconv.ParseFloat(m.Pricing.Prompt, 64)
		outPrice, _ := strconv.ParseFloat(m.Pricing.Completion, 64)
		
		cacheReadPrice := inPrice
		if m.Pricing.InputCacheRead != "" {
			cacheReadPrice, _ = strconv.ParseFloat(m.Pricing.InputCacheRead, 64)
		}

		Registry.Models[m.ID] = ModelSpecs{
			ID:                m.ID,
			FriendlyName:      m.Name,
			InputCostPerM:     inPrice * 1000000.0,
			OutputCostPerM:    outPrice * 1000000.0,
			CacheReadCostPerM: cacheReadPrice * 1000000.0,
			MaxContextWindow:  m.ContextLength,
		}
	}
	fmt.Printf("[Config] Ingested %d active model specs. Ready for deployment execution cycles.\n", len(Registry.Models))
}

func LoadConfig() *AppConfig {
	proxyPort := getEnv("PREFLIGHT_PORT", "8080")
	simModeStr := getEnv("PREFLIGHT_SIMULATION", "true")
	isSimulation := strings.ToLower(simModeStr) == "true"

	dirsStr := os.Getenv("PREFLIGHT_WATCH_DIRS")
	if dirsStr == "" {
		dirsStr = os.Getenv("PREFLIGHT_WATCH_DIR")
	}
	if dirsStr == "" {
		dirsStr = "."
	}

	var directories []string
	for _, d := range strings.Split(dirsStr, ",") {
		trimmed := strings.TrimSpace(d)
		if trimmed != "" {
			directories = append(directories, expandHomeDir(trimmed))
		}
	}

	return &AppConfig{
		ProxyPort:        ":" + proxyPort,
		SimulationMode:   isSimulation,
		OllamaEndpoint:   getEnv("OLLAMA_HOST", "http://localhost:11434"),
		TargetEvalModel:  getEnv("PREFLIGHT_EVAL_MODEL", "anthropic/claude-3.5-sonnet"),
		WatchDirectories: directories,
	}
}

func expandHomeDir(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

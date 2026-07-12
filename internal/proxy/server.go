package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"or-preflight/internal/config"
	"or-preflight/internal/indexer"
	"or-preflight/internal/tty"
)

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type InboundRequest struct {
	Model     string        `json:"model"`
	Messages  []ChatMessage `json:"messages"`
	MaxTokens int           `json:"max_tokens,omitempty"`
}

func StartProxyServer(cfg *config.AppConfig) {
	http.HandleFunc("/api/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		handleChatCompletion(w, r, cfg)
	})
	http.HandleFunc("/api/v1/preflight/status", func(w http.ResponseWriter, r *http.Request) {
		handleStatusCheck(w)
	})
}

func handleChatCompletion(w http.ResponseWriter, r *http.Request, cfg *config.AppConfig) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Unreadable request payload", http.StatusInternalServerError)
		return
	}

	var req InboundRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		http.Error(w, "Malformed JSON request schema", http.StatusBadRequest)
		return
	}

	var combinedText strings.Builder
	for _, msg := range req.Messages {
		combinedText.WriteString(msg.Content)
	}
	promptTokens := len(combinedText.String()) / 4

	targetModelID := req.Model
	if cfg.SimulationMode {
		targetModelID = cfg.TargetEvalModel
	}

	config.Registry.RLock()
	specs, exists := config.Registry.Models[targetModelID]
	config.Registry.RUnlock()

	if !exists {
		errMsg := fmt.Sprintf("[PRE-FLIGHT REJECTION] The requested model token identity '%s' is completely absent from the local synchronized catalog registry. Aborting transaction to shield budget thresholds.", targetModelID)
		fmt.Printf("\n🛑 %s\n", errMsg)
		http.Error(w, errMsg, http.StatusUnprocessableEntity)
		return
	}

	if specs.MaxContextWindow <= 0 {
		errMsg := fmt.Sprintf("[PRE-FLIGHT REJECTION] Model structure '%s' exhibits an uninitialized or broken context ceiling parameter (<= 0 tokens) from upstream metadata. Request dropped to protect transaction safety.", targetModelID)
		fmt.Printf("\n🛑 %s\n", errMsg)
		http.Error(w, errMsg, http.StatusInternalServerError)
		return
	}

	indexer.CurrentState.RLock()
	var registeredCodebaseTokens int
	for _, metrics := range indexer.CurrentState.Topology {
		registeredCodebaseTokens += metrics.TokenCount
	}
	indexer.CurrentState.RUnlock()

	totalEstimatedInput := promptTokens + registeredCodebaseTokens

	if totalEstimatedInput > specs.MaxContextWindow {
		errMsg := fmt.Sprintf("[PRE-FLIGHT OVERFLOW REJECTION] Total structural input [%d tokens] (Prompt: %d, Codebase: %d) violates the strict architectural context limit window [%d tokens] declared for %s.", 
			totalEstimatedInput, promptTokens, registeredCodebaseTokens, specs.MaxContextWindow, specs.FriendlyName)
		fmt.Printf("\n⚠️  %s\n", errMsg)
		http.Error(w, errMsg, http.StatusUnprocessableEntity)
		return
	}

	outputBudget := req.MaxTokens
	if outputBudget == 0 {
		outputBudget = 4000
	}

	isCacheHit := EvaluatePromptCache(req.Messages)
	
	var calculatedInputCost float64
	displayName := specs.FriendlyName

	if isCacheHit {
		displayName += " ⚡ [PROMPT CACHE HIT]"
		calculatedInputCost = float64(totalEstimatedInput) * (specs.CacheReadCostPerM / 1000000.0)
	} else {
		displayName += " ❄️ [CACHE MISS]"
		calculatedInputCost = float64(totalEstimatedInput) * (specs.InputCostPerM / 1000000.0)
	}

	estimatedOutputCost := float64(outputBudget) * (specs.OutputCostPerM / 1000000.0)
	totalProjectedCost := calculatedInputCost + estimatedOutputCost

	// 6. Interactive verification checkpoint via controlling TTY
	authorized := tty.PromptUserInteractively(displayName, totalEstimatedInput, outputBudget, totalProjectedCost)
	if !authorized {
		http.Error(w, "Query rejected by user at pre-flight terminal check.", http.StatusForbidden)
		return
	}

	// 7. Route connection traffic onward
	var targetURL string
	if cfg.SimulationMode {
		targetURL = cfg.OllamaEndpoint + "/v1/chat/completions"
		var parsedMap map[string]interface{}
		_ = json.Unmarshal(bodyBytes, &parsedMap)
		parsedMap["model"] = "llama3" 
		bodyBytes, _ = json.Marshal(parsedMap)
	} else {
		targetURL = "https://openrouter.ai/api/v1/chat/completions"
	}

	parsedDestination, _ := url.Parse(targetURL)
	upstreamReq, err := http.NewRequest(http.MethodPost, parsedDestination.String(), bytes.NewReader(bodyBytes))
	if err != nil {
		http.Error(w, "Failed to instantiate connection context", http.StatusInternalServerError)
		return
	}

	for k, vv := range r.Header {
		for _, v := range vv {
			upstreamReq.Header.Add(k, v)
		}
	}
	upstreamReq.Header.Set("Host", parsedDestination.Host)
	if cfg.SimulationMode {
		upstreamReq.Header.Del("Authorization") 
	}

	client := &http.Client{}
	resp, err := client.Do(upstreamReq)
	if err != nil {
		http.Error(w, "Downstream connection failure", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func handleStatusCheck(w http.ResponseWriter) {
	indexer.CurrentState.RLock()
	defer indexer.CurrentState.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(indexer.CurrentState)
}

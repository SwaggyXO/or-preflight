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

type AdvisoryCalcRequest struct {
	TouchedFiles []string `json:"touched_files"` 
	PromptText   string   `json:"prompt_text"`
	ExpectedOut  int      `json:"expected_out_tokens"`
}

type ModelCostLine struct {
	ModelID       string  `json:"model_id"`
	FriendlyName  string  `json:"friendly_name"`
	InputTokens   int     `json:"input_tokens"`
	OutputTokens  int     `json:"output_tokens"`
	ProjectedCost float64 `json:"projected_cost"`
}

type MultiAgentTeamResponse struct {
	PlannerLine   ModelCostLine `json:"planner_step"`
	ExecutorLine  ModelCostLine `json:"executor_step"`
	CombinedTotal float64       `json:"combined_total_task_cost"`
}

func StartProxyServer(cfg *config.AppConfig) {
	http.HandleFunc("/api/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		handleChatCompletion(w, r, cfg)
	})
	http.HandleFunc("/api/v1/preflight/status", func(w http.ResponseWriter, r *http.Request) {
		handleStatusCheck(w)
	})
	http.HandleFunc("/api/v1/preflight/calc", func(w http.ResponseWriter, r *http.Request) {
		handleAdvisoryCalculation(w, r, cfg)
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

	config.Registry.RLock()
	specs, exists := config.Registry.Models[req.Model]
	config.Registry.RUnlock()

	if !exists {
		errMsg := fmt.Sprintf("[CRITICAL PRE-FLIGHT REJECTION] Model '%s' is absent from the live synchronized pricing registry. Request blocked to prevent uncalculated costs.", req.Model)
		fmt.Printf("\n🛑 %s\n", errMsg)
		http.Error(w, errMsg, http.StatusUnprocessableEntity)
		return
	}

	if specs.MaxContextWindow <= 0 {
		errMsg := fmt.Sprintf("[CRITICAL PRE-FLIGHT REJECTION] Model '%s' contains an invalid or missing context ceiling (<= 0). Request blocked.", req.Model)
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
		errMsg := fmt.Sprintf("[PRE-FLIGHT OVERFLOW] Input [%d tokens] violates strict context limit [%d tokens] for %s.", 
			totalEstimatedInput, specs.MaxContextWindow, specs.FriendlyName)
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

	authorized := tty.PromptUserInteractively(displayName, totalEstimatedInput, outputBudget, totalProjectedCost)
	if !authorized {
		http.Error(w, "Query rejected by user at pre-flight terminal check.", http.StatusForbidden)
		return
	}

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

func handleAdvisoryCalculation(w http.ResponseWriter, r *http.Request, cfg *config.AppConfig) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req AdvisoryCalcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Malformed JSON request payload", http.StatusBadRequest)
		return
	}

	config.Registry.RLock()
	plannerSpecs, plannerExists := config.Registry.Models[cfg.PlannerModel]
	executorSpecs, executorExists := config.Registry.Models[cfg.ExecutorModel]
	config.Registry.RUnlock()

	if !plannerExists || !executorExists {
		http.Error(w, fmt.Sprintf("[Advisor Error] One or both configured team models are missing from the dynamic registry. (Planner: '%s', Executor: '%s')", cfg.PlannerModel, cfg.ExecutorModel), http.StatusUnprocessableEntity)
		return
	}

	var targetedCodebaseTokens int
	indexer.CurrentState.RLock()
	for _, fileKey := range req.TouchedFiles {
		normalizedKey := strings.ToLower(fileKey)
		for topKey, metrics := range indexer.CurrentState.Topology {
			if strings.Contains(topKey, normalizedKey) {
				targetedCodebaseTokens += metrics.TokenCount
				break
			}
		}
	}
	indexer.CurrentState.RUnlock()

	promptTokens := len(req.PromptText) / 4
	outputBudget := req.ExpectedOut
	if outputBudget == 0 {
		outputBudget = 4000 
	}

	plannerInputTokens := promptTokens + targetedCodebaseTokens
	plannerCost := float64(plannerInputTokens)*(plannerSpecs.InputCostPerM/1000000.0) + float64(outputBudget)*(plannerSpecs.OutputCostPerM/1000000.0)

	executorInputTokens := promptTokens + targetedCodebaseTokens + outputBudget
	executorCost := float64(executorInputTokens)*(executorSpecs.InputCostPerM/1000000.0) + float64(outputBudget)*(executorSpecs.OutputCostPerM/1000000.0)

	responsePayload := MultiAgentTeamResponse{
		PlannerLine: ModelCostLine{
			ModelID:       plannerSpecs.ID,
			FriendlyName:  plannerSpecs.FriendlyName,
			InputTokens:   plannerInputTokens,
			OutputTokens:  outputBudget,
			ProjectedCost: plannerCost,
		},
		ExecutorLine: ModelCostLine{
			ModelID:       executorSpecs.ID,
			FriendlyName:  executorSpecs.FriendlyName,
			InputTokens:   executorInputTokens,
			OutputTokens:  outputBudget,
			ProjectedCost: executorCost,
		},
		CombinedTotal: plannerCost + executorCost,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(responsePayload)
}

func handleStatusCheck(w http.ResponseWriter) {
	indexer.CurrentState.RLock()
	defer indexer.CurrentState.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(indexer.CurrentState)
}

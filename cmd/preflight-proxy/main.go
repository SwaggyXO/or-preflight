package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"or-preflight/internal/config"
	"or-preflight/internal/indexer"
	"or-preflight/internal/proxy"
)

func main() {
	fmt.Println("[Debug] Raw OS Lookup:", os.Getenv("PREFLIGHT_WATCH_DIRS"))
	cfg := config.LoadConfig()

	fmt.Println("🚀 INITIALIZING OPENROUTER PRE-FLIGHT COST ENFORCER DAEMON...")
	fmt.Println("==================================================================")
	fmt.Printf(" Target Execution Port : %s\n", cfg.ProxyPort)
	fmt.Printf(" Simulation Sandbox Mode: %t\n", cfg.SimulationMode)
	
	if cfg.SimulationMode {
		fmt.Printf(" Offline Eval Routing  : Forwarding payloads to local Ollama (%s)\n", cfg.OllamaEndpoint)
		fmt.Printf(" Cost Prediction Target: Evaluated against rates for %s\n", cfg.TargetEvalModel)
	} else {
		fmt.Println(" Upstream API Target   : Production Mode -> Routing to live OpenRouter Gateways")
	}
	
	fmt.Printf(" Tracked Codebase Roots: \n   • %s\n", strings.Join(cfg.WatchDirectories, "\n   • "))
	fmt.Println("==================================================================")

	fmt.Println("\n[Indexer] Launching parallel workspace scanning threads...")
	indexer.StartBackgroundIndex(cfg.WatchDirectories)

	indexer.CurrentState.RLock()
	fmt.Println("[Indexer] Initial codebase sweep complete. Current Memory Cache State:")
	var totalTokens int
	var totalLOC int
	for _, metrics := range indexer.CurrentState.Topology {
		fmt.Printf("   ├─ Directory [%-10s] Type [%-5s]: %d files | %5d LOC | ~%d tokens\n", 
			metrics.Folder, metrics.Extension, metrics.FileCount, metrics.TotalLOC, metrics.TokenCount)
		totalTokens += metrics.TokenCount
		totalLOC += metrics.TotalLOC
	}
	fmt.Printf("   └─ Total Footprint: %d source lines mapping to ~%d context tokens\n", totalLOC, totalTokens)
	indexer.CurrentState.RUnlock()

	proxy.StartProxyServer(cfg)

	fmt.Printf("\n⚡ Preflight Proxy Engine activated on http://localhost%s\n", cfg.ProxyPort)
	if err := http.ListenAndServe(cfg.ProxyPort, nil); err != nil {
		log.Fatalf("Fatal daemon execution crash: %v", err)
	}
}

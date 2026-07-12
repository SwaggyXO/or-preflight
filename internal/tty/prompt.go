package tty

import (
	"fmt"
	"os"
	"strings"
)

func PromptUserInteractively(modelName string, inputTokens, outputTokens int, estimatedCost float64) bool {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[Preflight Warning] No TTY access available. Rejecting raw query.\n")
		return false
	}
	defer tty.Close()

	fmt.Fprintln(tty, "\n─── PRE-FLIGHT TOKENS & COST LEDGER ──────────────────")
	fmt.Fprintf(tty, " Target Upstream Model : %s\n", modelName)
	fmt.Fprintf(tty, " Ingested Context Size : %d tokens\n", inputTokens)
	fmt.Fprintf(tty, " Expected Output Cap   : %d tokens\n", outputTokens)
	fmt.Fprintln(tty, "──────────────────────────────────────────────────────")
	fmt.Fprintf(tty, " Projected Cost/Query  : $%0.4f\n", estimatedCost)
	
	fmt.Fprintln(tty, "─── [Caveman Optimization Projections] ───────────────")
	fmt.Fprintf(tty, " Optimized Cost (Est)  : $%0.4f (-50%% via linting)\n", estimatedCost*0.50)
	fmt.Fprintln(tty, "──────────────────────────────────────────────────────")
	fmt.Fprintf(tty, "👉 Authorize upstream execution? (y/N): ")

	var inputBuffer [1]byte
	_, err = tty.Read(inputBuffer[:])
	if err != nil {
		return false
	}

	confirmation := strings.ToLower(string(inputBuffer[0]))
	if confirmation == "y" {
		fmt.Fprintln(tty, "✅ Query authorized. Forwarding payload downstream...")
		return true
	}

	fmt.Fprintln(tty, "❌ Query rejected by developer. Dropping connection context.")
	return false
}

// Command gateway is the AI failover gateway entry point.
package main

import (
	"fmt"
	"os"

	"github.com/kwa0x2/ai-failover-gateway/internal/app"
)

func main() {
	if err := app.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

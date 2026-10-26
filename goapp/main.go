package main

import (
	"fmt"
	"os"

	"tuissh-go/internal/ui"
)

func main() {
	if err := ui.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "tuissh:", err)
		os.Exit(1)
	}
}

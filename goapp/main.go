package main

import (
	"fmt"
	"os"

	"gossh-go/internal/ui"
)

func main() {
	if err := ui.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "gossh:", err)
		os.Exit(1)
	}
}

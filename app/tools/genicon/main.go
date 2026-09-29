// Command genicon writes the app icon as a 256×256 PNG (used by go-winres for the exe icon).
package main

import (
	"os"

	"netmon/internal/icon"
)

func main() {
	if err := os.WriteFile(os.Args[1], icon.PNG(icon.Green, 256), 0o644); err != nil {
		panic(err)
	}
}

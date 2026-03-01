package main

import (
	"os"
	"shelldeck/internal/bridge"
	"shelldeck/internal/server"
)

func main() {
	// Se avviato con flag -connect (modalità worker background del bridge),
	// avvia solo il bridge e non il server.
	if len(os.Args) > 2 && os.Args[1] == "-connect" {
		bridge.Start()
		return
	}

	// Avvia il server in una goroutine
	go server.Start()

	// Avvia il bridge (UI) nel thread principale (Fyne lo richiede)
	bridge.Start()
}

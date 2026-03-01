package bridge

import (
	"io"
	"log"
	"os"
	"strings"
)

func Start() {
	// Setup logging su file e stdout
	logFile, err := os.OpenFile("bridge.log", os.O_CREATE|os.O_APPEND|os.O_RDWR, 0666)
	if err != nil {
		log.Fatal(err)
	}
	defer logFile.Close()
	log.SetOutput(io.MultiWriter(os.Stdout, logFile))

	log.Println("Bridge Starting...")
	initConfigDB()

	// Start UI (Fyne handles the main loop)
	initUI()
}

func loadRuntimeConfig() bool {
	url := getConfig("server_url")
	key := getConfig("encryption_key")
	user := getConfig("username")

	if url != "" && key != "" {
		if !strings.Contains(url, "://") {
			url = "http://" + url
		}
		url = strings.TrimRight(url, "/")
		CurrentServerURL = url
		CurrentMasterKey = []byte(key)
		CurrentUsername = user
		return true
	}
	return false
}

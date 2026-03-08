package bridge

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"
)

var (
	flagMode     = flag.String("mode", "local", "Mode: local (default) or proxy")
	flagHeadless = flag.Bool("headless", false, "Run in headless mode (no UI)")
	flagConnect  = flag.Int("connect", 0, "Connect to server ID (background mode)")
	flagServer   = flag.String("server", "", "Server URL (e.g. wss://server.com)")
	flagUser     = flag.String("user", "", "Username")
	flagPass     = flag.String("pass", "", "Password")
)

func Start() {
	flag.Parse()

	// Setup logging su file e stdout
	logFile, err := os.OpenFile("bridge.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0666)
	if err != nil {
		log.Fatal(err)
	}
	defer logFile.Close()
	log.SetOutput(io.MultiWriter(os.Stdout, logFile))

	defer func() {
		if r := recover(); r != nil {
			log.Printf("CRITICAL BRIDGE PANIC: %v", r)
		}
	}()

	log.Println("Bridge Starting...")
	initConfigDB()

	// Handle background worker mode (Multiple connections)
	if *flagConnect > 0 {
		StartBridge(*flagConnect)
		select {} // Block forever
	}

	if Headless || *flagHeadless || *flagMode == "proxy" {
		startProxyMode()
		return
	}

	// Start UI (Fyne handles the main loop)
	initUI()
	log.Println("Bridge UI exited.")
}

func startProxyMode() {
	log.Println("Bridge Starting (Proxy Mode)...")

	server := *flagServer
	user := *flagUser
	pass := *flagPass

	// Load from DB if flags are missing
	if server == "" {
		server = getConfig("server_url")
	}
	if user == "" {
		user = getConfig("username")
	}
	if pass == "" {
		pass = getConfig("encryption_key")
	}

	if server == "" || user == "" || pass == "" {
		reader := bufio.NewReader(os.Stdin)
		if server == "" {
			fmt.Print("Server URL: ")
			server, _ = reader.ReadString('\n')
			server = strings.TrimSpace(server)
		}
		if user == "" {
			fmt.Print("Username: ")
			user, _ = reader.ReadString('\n')
			user = strings.TrimSpace(user)
		}
		if pass == "" {
			fmt.Print("Password: ")
			pass, _ = reader.ReadString('\n')
			pass = strings.TrimSpace(pass)
		}
	}

	// Save to DB for persistence
	setConfig("server_url", server)
	setConfig("username", user)
	setConfig("encryption_key", pass)

	// Infinite reconnection loop
	for {
		log.Printf("Connecting to %s as %s...", server, user)
		connectProxy(server, user, pass)
		log.Println("Connection lost. Retrying in 10 seconds...")
		time.Sleep(10 * time.Second)
	}
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

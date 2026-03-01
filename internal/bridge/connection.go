package bridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fasthttp/websocket"
)

var (
	bridgeRunning   bool
	stopBridge      chan struct{}
	onStatusChange  func(connected bool)
	onWelcome       func(string)
	MagicLoginToken string
	activeServer    *ServerConfig
	activeServerMu  sync.Mutex
)

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = (pongWait * 9) / 10
)

func SetStatusCallback(cb func(bool)) {
	onStatusChange = cb
}

func SetWelcomeCallback(cb func(string)) {
	onWelcome = cb
}

func StopBridge() {
	if bridgeRunning {
		close(stopBridge)
		bridgeRunning = false
	}
	wsMu.Lock()
	if wsConn != nil {
		wsConn.Close()
	}
	wsMu.Unlock()
}

func StartBridge(serverID int) {
	if bridgeRunning {
		return
	}
	stopBridge = make(chan struct{})
	bridgeRunning = true
	go connectionLoop(serverID)
}

func connectionLoop(serverID int) {
	targetID := serverID
	if targetID == 0 {
		targetID = getDefaultServerID()
	}
	if targetID == 0 {
		log.Println("Nessun server configurato o di default da avviare.")
		bridgeRunning = false
		return
	}

	for {
		select {
		case <-stopBridge:
			return
		default:
			connectToHQ(targetID)
			// Wait before retry if stopped or failed
			select {
			case <-stopBridge:
				return
			case <-time.After(10 * time.Second):
			}
		}
	}
}

func connectToHQ(serverID int) {
	config, err := getServer(serverID)
	if err != nil {
		log.Printf("Impossibile ottenere la configurazione per il server ID %d: %v", serverID, err)
		return
	}
	rawURL := config.URL
	if !strings.Contains(rawURL, "://") {
		rawURL = "http://" + rawURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		log.Fatal("Invalid Server URL:", err)
	}

	// Aggiorna l'URL nella configurazione attiva con lo schema corretto
	config.URL = rawURL

	activeServerMu.Lock()
	activeServer = config
	CurrentServerURL = rawURL
	activeServerMu.Unlock()

	scheme := "ws"
	if parsed.Scheme == "https" {
		scheme = "wss"
	}

	u := url.URL{Scheme: scheme, Host: parsed.Host, Path: "/ws/bridge"}
	log.Printf("Connessione al Quartier Generale: %s...", u.String())
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		log.Println("Errore connessione HQ:", err)
		return
	}
	wsMu.Lock()
	wsConn = conn
	wsMu.Unlock()

	// --- KEEPALIVE SETUP ---
	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error { conn.SetReadDeadline(time.Now().Add(pongWait)); return nil })

	go func() {
		ticker := time.NewTicker(pingPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				wsMu.Lock()
				if wsConn != conn {
					wsMu.Unlock()
					return
				}
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					wsMu.Unlock()
					return
				}
				wsMu.Unlock()
			}
		}
	}()

	if onStatusChange != nil {
		onStatusChange(true)
	}
	log.Println("✅ BRIDGE PRONTO.")

	// --- HANDSHAKE: Invia preferenza workspace ---
	payload := map[string]string{
		"username": config.Username,
		"key":      config.EncryptionKey,
	}
	prefID := getConfig("preferred_workspace")
	if prefID != "" {
		payload["preferred_workspace"] = prefID
	}
	log.Printf("Inviando Handshake Bridge per utente: %s", config.Username)
	conn.WriteJSON(JMessage{Type: "bridge_hello", Payload: payload})

	for {
		var msg JMessage
		err := conn.ReadJSON(&msg)
		if err != nil {
			if onStatusChange != nil {
				onStatusChange(false)
			}
			log.Println("Disconnesso dal server:", err)
			wsMu.Lock()
			if wsConn == conn {
				wsConn = nil
			}
			setConfig(fmt.Sprintf("token_%d", config.ID), "")
			conn.Close()

			// Clean up sessions (protected by wsMu)
			for key := range sshSessions {
				delete(sshSessions, key)
			}
			wsMu.Unlock()

			// Clean up all SSH clients and sessions on disconnect
			sshClientsMu.Lock()
			for hostID, client := range sshClients {
				client.Close()
				delete(sshClients, hostID)
			}
			sshClientsMu.Unlock()
			return
		}
		switch msg.Type {
		case "bridge_welcome":
			log.Println("Ricevuto messaggio bridge_welcome")
			if payloadMap, ok := msg.Payload.(map[string]interface{}); ok {
				if token, ok := payloadMap["token"].(string); ok {
					MagicLoginToken = token
					log.Println("Ricevuto token di auto-login.")
					setConfig(fmt.Sprintf("token_%d", config.ID), token)
					if onWelcome != nil {
						onWelcome(token)
					}
				} else {
					log.Println("Errore: token non trovato nel payload")
				}
			} else {
				log.Printf("Errore: payload non è una mappa: %T", msg.Payload)
			}
		case "ssh_input":
			log.Printf("[BRIDGE-RX-SSH_INPUT] HostID: %d, TermID: %s", msg.HostID, msg.TermID)
			if payloadStr, ok := msg.Payload.(string); ok {
				go handleSSHInput(msg.HostID, msg.TermID, payloadStr)
			}
		case "ssh_resize":
			log.Printf("[BRIDGE-RX-SSH_RESIZE] HostID: %d, TermID: %s", msg.HostID, msg.TermID)
			if payloadMap, ok := msg.Payload.(map[string]interface{}); ok {
				go handleSSHResize(msg.HostID, msg.TermID, payloadMap)
			}
		case "ssh_close":
			log.Printf("[BRIDGE-RX-SSH_CLOSE] HostID: %d, TermID: %s", msg.HostID, msg.TermID)
			go closeSession(msg.HostID, msg.TermID)
		case "fs_command":
			log.Printf("[BRIDGE-RX-FS_COMMAND] HostID: %d, TermID: %s", msg.HostID, msg.TermID)
			if payloadMap, ok := msg.Payload.(map[string]interface{}); ok {
				go handleFSCommand(msg.HostID, msg.TermID, payloadMap)
			}
		case "service_command":
			log.Printf("[BRIDGE-RX-SERVICE_COMMAND] HostID: %d, TermID: %s", msg.HostID, msg.TermID)
			if payloadMap, ok := msg.Payload.(map[string]interface{}); ok {
				go handleServiceCommand(msg.HostID, msg.TermID, payloadMap)
			}
		case "network_command":
			log.Printf("[BRIDGE-RX-NETWORK_COMMAND] HostID: %d, TermID: %s", msg.HostID, msg.TermID)
			if payloadMap, ok := msg.Payload.(map[string]interface{}); ok {
				go handleNetworkCommand(msg.HostID, msg.TermID, payloadMap)
			}
		case "disk_command":
			log.Printf("[BRIDGE-RX-DISK_COMMAND] HostID: %d, TermID: %s", msg.HostID, msg.TermID)
			if payloadMap, ok := msg.Payload.(map[string]interface{}); ok {
				go handleDiskCommand(msg.HostID, msg.TermID, payloadMap)
			}
		case "log_command":
			log.Printf("[BRIDGE-RX-LOG_COMMAND] HostID: %d, TermID: %s", msg.HostID, msg.TermID)
			if payloadMap, ok := msg.Payload.(map[string]interface{}); ok {
				go handleLogCommand(msg.HostID, msg.TermID, payloadMap)
			}
		case "ssh_command":
			if payloadMap, ok := msg.Payload.(map[string]interface{}); ok {
				go handleSSHCommand(msg.HostID, msg.TermID, payloadMap)
			}
		case "nginx_command":
			if payloadMap, ok := msg.Payload.(map[string]interface{}); ok {
				go handleNginxCommand(msg.HostID, msg.TermID, payloadMap)
			}
		case "apache_command":
			if payloadMap, ok := msg.Payload.(map[string]interface{}); ok {
				go handleApacheCommand(msg.HostID, msg.TermID, payloadMap)
			}
		case "pkg_command":
			if payloadMap, ok := msg.Payload.(map[string]interface{}); ok {
				go handlePkgCommand(msg.HostID, msg.TermID, payloadMap)
			}
		case "admin_command":
			if payloadMap, ok := msg.Payload.(map[string]interface{}); ok {
				go handleAdminCommand(msg.HostID, msg.TermID, payloadMap)
			}
		case "taskmgr_command":
			if payloadMap, ok := msg.Payload.(map[string]interface{}); ok {
				go handleTaskMgrCommand(msg.HostID, msg.TermID, payloadMap)
			}
		case "setup_ssh_key":
			wsID := 0
			if payloadMap, ok := msg.Payload.(map[string]interface{}); ok {
				if w, ok := payloadMap["workspace_id"].(float64); ok {
					wsID = int(w)
				} else if w, ok := payloadMap["workspace_id"].(int); ok {
					wsID = w
				}
			}
			go handleSSHKeySetup(msg.HostID, wsID)
		case "search_command":
			log.Printf("[BRIDGE-RX-SEARCH_COMMAND] HostID: %d, TermID: %s", msg.HostID, msg.TermID)
			if payloadMap, ok := msg.Payload.(map[string]interface{}); ok {
				go handleSearchCommand(msg.HostID, msg.TermID, payloadMap)
			}
		case "system_command":
			if payloadMap, ok := msg.Payload.(map[string]interface{}); ok {
				go handleSystemCommand(msg.HostID, msg.TermID, payloadMap)
			}
		case "dashboard_start_monitoring":
			log.Printf("[BRIDGE-RX-DASH_MONITOR] HostID: %d, TermID: %s", msg.HostID, msg.TermID)
			wsID := GetWorkspaceID(msg.TermID)
			go handleDashboardStartMonitoring(msg.HostID, wsID, msg.TermID)
		case "dashboard_start_logs":
			log.Printf("[BRIDGE-RX-DASH_LOGS] HostID: %d, TermID: %s", msg.HostID, msg.TermID)
			wsID := GetWorkspaceID(msg.TermID)
			go handleDashboardStartLogs(msg.HostID, wsID, msg.TermID)
		case "mariadb_command":
			if payloadMap, ok := msg.Payload.(map[string]interface{}); ok {
				go handleMariaDBCommand(msg.HostID, msg.TermID, payloadMap)
			}
		case "docker_command":
			if payloadMap, ok := msg.Payload.(map[string]interface{}); ok {
				go handleDockerCommand(msg.HostID, msg.TermID, payloadMap)
			}
		case "sftp_init":
			log.Printf("[BRIDGE-RX-SFTP_INIT] HostID: %d", msg.HostID)
			go func() {
				wsID := 0
				if strings.Contains(msg.TermID, ":") {
					parts := strings.SplitN(msg.TermID, ":", 2)
					if len(parts) == 2 {
						if id, err := strconv.Atoi(parts[0]); err == nil {
							wsID = id
						}
					}
				}
				client, err := ensureClient(msg.HostID, wsID)
				if err != nil {
					sendToHQ("sftp_init_res", msg.HostID, msg.TermID, map[string]string{"status": "error", "msg": err.Error()})
					return
				}
				_, _, err = client.SendRequest("keepalive@shelldeck", true, nil)
				if err != nil {
					sendToHQ("sftp_init_res", msg.HostID, msg.TermID, map[string]string{"status": "error", "msg": "Keepalive check failed: " + err.Error()})
				} else {
					sendToHQ("sftp_init_res", msg.HostID, msg.TermID, map[string]string{"status": "success"})
				}
			}()
		case "sftpc_connect":
			log.Printf("[BRIDGE-RX-SFTPC_CONNECT] HostID: %d", msg.HostID)
			if payloadMap, ok := msg.Payload.(map[string]interface{}); ok {
				go handleSftpcConnect(msg.HostID, msg.TermID, payloadMap)
			}
		case "sftpc_command":
			log.Printf("[BRIDGE-RX-SFTPC_COMMAND] HostID: %d", msg.HostID)
			if payloadMap, ok := msg.Payload.(map[string]interface{}); ok {
				go handleSftpcCommand(msg.HostID, msg.TermID, payloadMap)
			}
		case "tunnel_ctl":
			if payloadMap, ok := msg.Payload.(map[string]interface{}); ok {
				go handleTunnelControl(msg.HostID, payloadMap)
			}
		case "save_workspace":
			if payloadMap, ok := msg.Payload.(map[string]interface{}); ok {
				if id, ok := payloadMap["id"].(string); ok {
					setConfig("preferred_workspace", id)
					log.Printf("Workspace preferito salvato: %s", id)
				}
			}
		case "server_list":
			servers := getServers()
			sendToHQ("server_list_res", msg.HostID, msg.TermID, servers)
		case "server_add":
			if payloadMap, ok := msg.Payload.(map[string]interface{}); ok {
				urlStr, _ := payloadMap["url"].(string)
				user, _ := payloadMap["username"].(string)
				pass, _ := payloadMap["password"].(string)

				if !strings.Contains(urlStr, "://") {
					urlStr = "http://" + urlStr
				}
				urlStr = strings.TrimRight(urlStr, "/")

				// Esegui login per ottenere la chiave
				loginURL := fmt.Sprintf("%s/api/login", urlStr)

				reqBody, _ := json.Marshal(map[string]string{"username": user, "password": pass})
				resp, err := http.Post(loginURL, "application/json", bytes.NewBuffer(reqBody))

				if err != nil || resp.StatusCode != 200 {
					sendToHQ("server_add_res", msg.HostID, msg.TermID, map[string]string{"status": "error", "msg": "Login failed"})
				} else {
					var res map[string]interface{}
					json.NewDecoder(resp.Body).Decode(&res)
					resp.Body.Close()

					if key, ok := res["encryption_key"].(string); ok {
						err := addServer("", urlStr, user, key)
						if err != nil {
							sendToHQ("server_add_res", msg.HostID, msg.TermID, map[string]string{"status": "error", "msg": err.Error()})
						} else {
							sendToHQ("server_add_res", msg.HostID, msg.TermID, map[string]string{"status": "success"})
							// Invia lista aggiornata
							sendToHQ("server_list_res", msg.HostID, msg.TermID, getServers())
						}
					}
				}
			}
		case "server_switch":
			if payloadMap, ok := msg.Payload.(map[string]interface{}); ok {
				if idFloat, ok := payloadMap["id"].(float64); ok {
					id := int(idFloat)
					setDefaultServer(id)
					log.Printf("Switching to server ID %d...", id)

					// Chiudi connessione per forzare riconnessione nel main loop
					wsMu.Lock()
					if wsConn == conn {
						wsConn = nil
					}
					conn.Close()
					wsMu.Unlock()
					return
				}
			}
		case "server_list_res":
			// Ignore echoed response to prevent log spam
		case "sys_stats", "sys_logs", "ssh_output", "tunnel_status", "tunnel_list":
			// Ignore echoed messages from server to prevent log spam
		default:
			log.Printf("Unknown message type: %s", msg.Type)
		}
	}
}

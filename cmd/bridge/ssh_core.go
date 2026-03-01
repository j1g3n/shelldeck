package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

type ActiveTunnel struct {
	Client   *ssh.Client
	Listener net.Listener
}

var (
	sshClients      = make(map[string]*ssh.Client)
	sshClientsMu    sync.Mutex
	sudoPasswords   = make(map[string]string)
	sudoPassMu      sync.Mutex
	activeTunnels   = make(map[int]ActiveTunnel) // HostID -> Tunnel Info
	activeTunnelsMu sync.Mutex
)

func isSessionActive(hostID int, termID string) bool {
	sessionKey := fmt.Sprintf("%d:%s", hostID, termID)
	wsMu.Lock()
	defer wsMu.Unlock()
	_, exists := sshSessions[sessionKey]
	return exists
}

// GetSessionIDWithPrefix helper per preservare il prefisso workspace (es. "1:main")
func GetSessionIDWithPrefix(termID string, suffix string) string {
	if idx := strings.Index(termID, ":"); idx != -1 {
		return termID[:idx+1] + suffix
	}
	return suffix
}

func GetWorkspaceID(termID string) int {
	if strings.Contains(termID, ":") {
		parts := strings.SplitN(termID, ":", 2)
		if len(parts) == 2 {
			if id, err := strconv.Atoi(parts[0]); err == nil {
				return id
			}
		}
	}
	return 0
}

// ensureClient gestisce e mette in cache la connessione *ssh.Client* per un host.
func ensureClient(hostID int, workspaceID int) (*ssh.Client, error) {
	clientKey := fmt.Sprintf("%d:%d", workspaceID, hostID)
	sshClientsMu.Lock()

	// Check if a client exists
	if client, exists := sshClients[clientKey]; exists && client != nil {
		// If it exists, check if it's alive
		_, _, err := client.SendRequest("keepalive@jconman", true, nil)
		if err == nil {
			// It's alive, unlock and return
			sshClientsMu.Unlock()
			return client, nil
		}
		// It's dead, close and remove it before creating a new one
		client.Close()
		delete(sshClients, clientKey)
		sudoPassMu.Lock()
		delete(sudoPasswords, clientKey)
		sudoPassMu.Unlock()
	}

	// We hold the lock and are sure we need to create a new client.
	// We must unlock during dial to avoid holding the lock for a long time.
	sshClientsMu.Unlock()

	log.Printf("[Host %d] Creazione nuova connessione SSH...", hostID)
	newClient, sudoPass, creds, err := dialHost(hostID, workspaceID)
	if err != nil {
		return nil, err
	}

	// Now we need to store the new client. Lock again.
	sshClientsMu.Lock()
	defer sshClientsMu.Unlock()

	// It's possible another goroutine created a client while we were dialing.
	if existingClient, exists := sshClients[clientKey]; exists && existingClient != nil {
		// If so, close our new client and return the existing one.
		newClient.Close()
		return existingClient, nil
	}

	// Otherwise, store our new client.
	sshClients[clientKey] = newClient
	sudoPassMu.Lock()
	sudoPasswords[clientKey] = sudoPass
	sudoPassMu.Unlock()

	// Start tunnel if configured (best effort for standard sessions)
	if creds != nil {
		go startTunnel(newClient, creds)
	}

	return newClient, nil
}

// ensureSession gestisce una sessione/canale virtuale (PTY) sopra una connessione fisica.
func ensureSession(hostID int, termID string) (*SSHSessionWrapper, error) {
	if termID == "" {
		termID = "main" // Sessione di default per comandi non-terminale
	}
	sessionKey := fmt.Sprintf("%d:%s", hostID, termID)

	// Extract WorkspaceID from TermID (format: "wsID:realTermID")
	wsID := 0
	if strings.Contains(termID, ":") {
		parts := strings.SplitN(termID, ":", 2)
		if len(parts) == 2 {
			if id, err := strconv.Atoi(parts[0]); err == nil {
				wsID = id
			}
		}
	}

	wsMu.Lock()
	// Controlla se una sessione esiste già ed è valida
	if session, exists := sshSessions[sessionKey]; exists && session != nil && session.Session != nil {
		wsMu.Unlock()
		return session, nil
	}
	// Se non esiste, dobbiamo crearla. Rilasciamo il lock prima delle operazioni lunghe.
	wsMu.Unlock()

	log.Printf("[Host %d] Sessione per terminale '%s' mancante. Creazione...", hostID, termID)

	client, err := ensureClient(hostID, wsID)
	if err != nil {
		return nil, fmt.Errorf("ensureClient failed: %w", err)
	}

	newSess, err := startSshSessionForTerm(client, hostID, termID)
	if err != nil {
		return nil, fmt.Errorf("startSshSessionForTerm failed: %w", err)
	}

	// La password sudo è legata al client, che ora è garantito esistere.
	clientKey := fmt.Sprintf("%d:%d", wsID, hostID)
	sudoPassMu.Lock()
	sudoPass := sudoPasswords[clientKey]
	sudoPassMu.Unlock()
	newSess.Password = sudoPass // Imposta la password
	newSess.Client = client     // Aggiungi il client al wrapper

	// Blocca di nuovo per inserire la nuova sessione nella mappa
	wsMu.Lock()
	defer wsMu.Unlock()

	// Double-check: un'altra goroutine potrebbe aver creato la sessione nel frattempo
	if session, exists := sshSessions[sessionKey]; exists && session != nil && session.Session != nil {
		newSess.Session.Close() // Chiudi la sessione che abbiamo appena creato inutilmente
		return session, nil
	}

	sshSessions[sessionKey] = newSess

	return newSess, nil
}

func startSshSessionForTerm(client *ssh.Client, hostID int, termID string) (*SSHSessionWrapper, error) {
	// Optimization: "main" sessions are used for FS/Admin commands which use runSingleCommand (creating their own ephemeral sessions).
	// We don't need a persistent session for "main".
	// Also exclude dashboard services which manage their own sessions.
	if termID == "main" || strings.HasSuffix(termID, ":main") || strings.Contains(termID, "dashboard-stats") || strings.Contains(termID, "dashboard-logs") {
		return &SSHSessionWrapper{Client: client}, nil
	}

	session, err := client.NewSession()
	if err != nil {
		return nil, err
	}

	var stdin io.WriteCloser
	var stdout, stderr io.Reader

	// La PTY è richiesta solo per terminali interattivi, non per la sessione "main"
	{
		stdin, _ = session.StdinPipe()
		stdout, _ = session.StdoutPipe()
		stderr, _ = session.StderrPipe()

		modes := ssh.TerminalModes{
			ssh.ECHO:          1,
			ssh.TTY_OP_ISPEED: 14400,
			ssh.TTY_OP_OSPEED: 14400,
		}
		if err := session.RequestPty("xterm-256color", 40, 80, modes); err != nil {
			session.Close()
			return nil, err
		}
		if err := session.Shell(); err != nil {
			session.Close()
			return nil, err
		}
		go forwardReader(hostID, termID, stdout)
		go forwardReader(hostID, termID, stderr)
	}

	return &SSHSessionWrapper{Session: session, Stdin: stdin, Client: client}, nil
}

func forwardReader(hostID int, termID string, r io.Reader) {
	buf := make([]byte, 8192)
	sessionKey := fmt.Sprintf("%d:%s", hostID, termID)
	for {
		n, err := r.Read(buf)
		if err != nil {
			wsMu.Lock()
			if sess, ok := sshSessions[sessionKey]; ok {
				sess.Session.Close()
				delete(sshSessions, sessionKey)
			}
			wsMu.Unlock()
			log.Printf("Sessione I/O %s terminata.", sessionKey)
			return
		}
		sendToHQ("ssh_output", hostID, termID, string(buf[:n]))
	}
}

func closeSession(hostID int, termID string) {
	sessionKey := fmt.Sprintf("%d:%s", hostID, termID)
	wsMu.Lock()
	defer wsMu.Unlock()

	if sess, exists := sshSessions[sessionKey]; exists {
		if sess.Session != nil {
			sess.Session.Close()
		}
		delete(sshSessions, sessionKey)
		log.Printf("Sessione SSH chiusa manualmente: %s", sessionKey)
	}
}

func handleSSHInput(hostID int, termID string, payload string) {
	session, err := ensureSession(hostID, termID)
	if err != nil {
		sendToHQ("ssh_output", hostID, termID, fmt.Sprintf("\r\n[BRIDGE ERROR] %v\r\n", err))
		return
	}

	if session != nil && session.Stdin != nil {
		session.Stdin.Write([]byte(payload))
	}
}

func handleSSHResize(hostID int, termID string, payload map[string]interface{}) {
	// Optimization: Don't create session just for resize.
	// If it doesn't exist, it might be closed or not yet created.
	sessionKey := fmt.Sprintf("%d:%s", hostID, termID)
	wsMu.Lock()
	session, exists := sshSessions[sessionKey]
	wsMu.Unlock()

	if !exists || session == nil || session.Session == nil {
		return
	}
	var cols, rows int
	if c, ok := payload["cols"].(float64); ok {
		cols = int(c)
	}
	if r, ok := payload["rows"].(float64); ok {
		rows = int(r)
	}
	if cols > 0 && rows > 0 {
		// Enforce sane minimums to prevent shell crashes
		if cols < 10 {
			cols = 10
		}
		if rows < 5 {
			rows = 5
		}
		session.Session.WindowChange(rows, cols)
	}
}

// ... (Il resto delle funzioni come dialHost, getHostCredentials etc. restano qui)
// Scarica le credenziali complete
func getHostCredentials(hostID int, workspaceID int) (*HostCredentials, error) {
	url := fmt.Sprintf("%s/api/host-credentials/%d?workspace_id=%d", CurrentServerURL, hostID, workspaceID)
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API Error (%d): %s", resp.StatusCode, string(bodyBytes))
	}
	var creds HostCredentials
	if err := json.NewDecoder(resp.Body).Decode(&creds); err != nil {
		return nil, err
	}
	return &creds, nil
}

// Metodi auth (Key, Passphrase, Password)
func getAuthMethods(creds *HostCredentials) ([]ssh.AuthMethod, string, error) {
	var auths []ssh.AuthMethod
	var sudoPass string

	if creds.Key != "" {
		var signer ssh.Signer
		var parseErr error

		if creds.Passphrase != "" {
			signer, parseErr = ssh.ParsePrivateKeyWithPassphrase([]byte(creds.Key), []byte(creds.Passphrase))
		} else {
			signer, parseErr = ssh.ParsePrivateKey([]byte(creds.Key))
		}

		if parseErr == nil {
			auths = append(auths, ssh.PublicKeys(signer))
		} else {
			log.Printf("Errore key SSH host %s: %v", creds.IP, parseErr)
		}
	}

	if creds.Password != "" {
		auths = append(auths, ssh.Password(creds.Password))
		sudoPass = creds.Password
	}

	if len(auths) == 0 {
		return nil, "", fmt.Errorf("nessun metodo auth valido")
	}

	return auths, sudoPass, nil
}

// Connessione Base
func dialHost(hostID int, workspaceID int) (*ssh.Client, string, *HostCredentials, error) {
	creds, err := getHostCredentials(hostID, workspaceID)
	if err != nil {
		return nil, "", nil, err
	}

	auths, sudoPass, err := getAuthMethods(creds)
	if err != nil {
		return nil, "", nil, err
	}

	addr := creds.IP
	if !strings.Contains(addr, ":") {
		addr = addr + ":22"
	}

	config := &ssh.ClientConfig{
		User:            creds.User,
		Auth:            auths,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	if creds.JumpIP != "" {
		jumpAddr := creds.JumpIP
		if !strings.Contains(jumpAddr, ":") {
			if creds.JumpPort != "" {
				jumpAddr += ":" + creds.JumpPort
			} else {
				jumpAddr += ":22"
			}
		}
		jumpAuth := []ssh.AuthMethod{ssh.Password(creds.JumpPass)}
		jumpConfig := &ssh.ClientConfig{
			User:            creds.JumpUser,
			Auth:            jumpAuth,
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout:         10 * time.Second,
		}
		bastionClient, err := ssh.Dial("tcp", jumpAddr, jumpConfig)
		if err != nil {
			return nil, "", nil, fmt.Errorf("custom bastion dial err: %v", err)
		}
		conn, err := bastionClient.Dial("tcp", addr)
		if err != nil {
			bastionClient.Close()
			return nil, "", nil, fmt.Errorf("target dial via bastion err: %v", err)
		}
		ncc, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
		if err != nil {
			bastionClient.Close()
			return nil, "", nil, fmt.Errorf("ssh tunnel handshake err: %v", err)
		}
		client := ssh.NewClient(ncc, chans, reqs)
		return client, sudoPass, creds, nil
	}

	if creds.JumpHostID > 0 {
		log.Printf("Jump Host via DB non ancora pienamente supportato in questa refactorizzazione.")
	}

	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, "", nil, err
	}
	return client, sudoPass, creds, nil
}

// --- GESTIONE TUNNEL BACKGROUND ---

func handleTunnelControl(hostID int, payload map[string]interface{}) {
	action, _ := payload["action"].(string)

	if action == "start" {
		// Check if already active
		activeTunnelsMu.Lock()
		if _, ok := activeTunnels[hostID]; ok {
			activeTunnelsMu.Unlock()
			sendToHQ("tunnel_status", 0, "", map[string]interface{}{"active": true, "msg": "Already running", "host_id": hostID})
			return
		}
		activeTunnelsMu.Unlock()

		// Connect
		// Note: We use workspace 0 or need to pass it. Assuming default/current for now.
		// Ideally payload should contain workspace_id.
		// For now, let's try to find creds.
		// Since we don't have workspace_id in payload easily here without changing protocol,
		// we might rely on the fact that dialHost will try to find it.
		// Let's assume workspace 0 (default) or try to get it from context if possible.
		// FIX: For now using 0, but in multi-workspace env this might need fix.

		client, _, creds, err := dialHost(hostID, 0)
		if err != nil {
			sendToHQ("tunnel_status", 0, "", map[string]interface{}{"active": false, "error": err.Error(), "host_id": hostID})
			return
		}

		// Start Tunnel Listener based on config
		listener, err := startTunnel(client, creds)
		if err != nil {
			client.Close()
			sendToHQ("tunnel_status", 0, "", map[string]interface{}{"active": false, "error": err.Error(), "host_id": hostID})
			return
		}

		activeTunnelsMu.Lock()
		activeTunnels[hostID] = ActiveTunnel{Client: client, Listener: listener}
		activeTunnelsMu.Unlock()

		sendToHQ("tunnel_status", 0, "", map[string]interface{}{"active": true, "host_id": hostID})
		log.Printf("[Tunnel] Host %d tunnel started (Type: %s)", hostID, creds.TunnelType)

	} else if action == "stop" {
		activeTunnelsMu.Lock()
		tunnel, ok := activeTunnels[hostID]
		if ok {
			if tunnel.Listener != nil {
				tunnel.Listener.Close()
			}
			if tunnel.Client != nil {
				tunnel.Client.Close()
			}
			delete(activeTunnels, hostID)
		}
		activeTunnelsMu.Unlock()

		sendToHQ("tunnel_status", 0, "", map[string]interface{}{"active": false, "host_id": hostID})
		log.Printf("[Tunnel] Host %d tunnel stopped", hostID)
	}

	if action == "sync" {
		activeTunnelsMu.Lock()
		var activeIDs []int
		for id := range activeTunnels {
			activeIDs = append(activeIDs, id)
		}
		activeTunnelsMu.Unlock()
		sendToHQ("tunnel_list", 0, "", map[string]interface{}{"active_ids": activeIDs})
	}
}

// --- GESTIONE TUNNEL ---
func startTunnel(client *ssh.Client, creds *HostCredentials) (net.Listener, error) {
	if creds.TunnelType == "" {
		return nil, fmt.Errorf("tunnel type not configured")
	}

	// DYNAMIC (SOCKS5)
	if creds.TunnelType == "D" {
		localAddr := fmt.Sprintf("0.0.0.0:%s", creds.TunnelLPort)
		log.Printf("[Tunnel-D] Starting SOCKS5 on %s", localAddr)
		return startSocks5Proxy(client, localAddr)
	}

	// REMOTE (Reverse)
	if creds.TunnelType == "R" {
		// Remote port -> Local host:port
		// Note: In DB "TunnelRPort" is the port ON REMOTE to listen.
		// "TunnelLPort" is the LOCAL port to forward to (usually localhost:port).
		// "TunnelRHost" might be used as the target host (usually localhost).

		remoteListen := fmt.Sprintf("0.0.0.0:%s", creds.TunnelRPort)
		localTarget := fmt.Sprintf("%s:%s", creds.TunnelRHost, creds.TunnelLPort) // e.g. localhost:8080

		log.Printf("[Tunnel-R] Requesting remote forward: %s -> %s", remoteListen, localTarget)

		listener, err := client.Listen("tcp", remoteListen)
		if err != nil {
			log.Printf("[Tunnel-R] Error listening on remote %s: %v", remoteListen, err)
			return nil, err
		}

		go func() {
			defer listener.Close()
			for {
				remoteConn, err := listener.Accept()
				if err != nil {
					return
				}
				go func() {
					localConn, err := net.Dial("tcp", localTarget)
					if err != nil {
						remoteConn.Close()
						return
					}
					go io.Copy(remoteConn, localConn)
					go io.Copy(localConn, remoteConn)
				}()
			}
		}()
		return listener, nil
	}

	// LOCAL (Standard)
	localAddr := fmt.Sprintf("0.0.0.0:%s", creds.TunnelLPort)
	remoteAddr := fmt.Sprintf("%s:%s", creds.TunnelRHost, creds.TunnelRPort)

	listener, err := net.Listen("tcp", localAddr)
	if err != nil {
		log.Printf("[Tunnel] Errore bind locale %s: %v", localAddr, err)
		return nil, err
	}

	go func() {
		defer listener.Close()
		log.Printf("[Tunnel] Avviato Forward Locale: Bridge[%s] -> Target[%s]", localAddr, remoteAddr)

		for {
			localConn, err := listener.Accept()
			if err != nil {
				continue
			}
			go func() {
				remoteConn, err := client.Dial("tcp", remoteAddr)
				if err != nil {
					localConn.Close()
					return
				}
				go io.Copy(localConn, remoteConn)
				go io.Copy(remoteConn, localConn)
			}()
		}
	}()
	return listener, nil
}

// Minimal SOCKS5 Server implementation
func startSocks5Proxy(client *ssh.Client, localAddr string) (net.Listener, error) {
	listener, err := net.Listen("tcp", localAddr)
	if err != nil {
		log.Printf("[SOCKS5] Error listen: %v", err)
		return nil, err
	}

	go func() {
		defer listener.Close()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go handleSocks5(conn, client)
		}
	}()
	return listener, nil
}

func handleSocks5(conn net.Conn, sshClient *ssh.Client) {
	defer conn.Close()
	clientAddr := conn.RemoteAddr().String()
	log.Printf("[SOCKS5] Incoming connection from %s", clientAddr)

	// Handshake
	buf := make([]byte, 2)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return
	}
	if buf[0] != 0x05 {
		return
	} // Ver 5
	numMethods := int(buf[1])
	methods := make([]byte, numMethods)
	if _, err := io.ReadFull(conn, methods); err != nil {
		return
	}

	conn.Write([]byte{0x05, 0x00}) // No auth

	// Request
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return
	}

	var dest string
	switch header[3] {
	case 0x01: // IPv4
		ip := make([]byte, 4)
		io.ReadFull(conn, ip)
		dest = net.IP(ip).String()
	case 0x03: // Domain
		l := make([]byte, 1)
		io.ReadFull(conn, l)
		domain := make([]byte, int(l[0]))
		io.ReadFull(conn, domain)
		dest = string(domain)
	case 0x04: // IPv6
		ip := make([]byte, 16)
		io.ReadFull(conn, ip)
		dest = net.IP(ip).String()
	}
	portBuf := make([]byte, 2)
	io.ReadFull(conn, portBuf)
	port := int(portBuf[0])<<8 | int(portBuf[1])

	destAddr := fmt.Sprintf("%s:%d", dest, port)
	log.Printf("[SOCKS5] %s requesting connection to %s", clientAddr, destAddr)

	// Connect via SSH
	remote, err := sshClient.Dial("tcp", destAddr)
	if err != nil {
		log.Printf("[SOCKS5] Failed to connect to %s: %v", destAddr, err)
		conn.Write([]byte{0x05, 0x04, 0x00, 0x01, 0, 0, 0, 0, 0, 0}) // Host unreachable
		return
	}
	defer remote.Close()
	conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}) // Success

	log.Printf("[SOCKS5] Connection established to %s", destAddr)
	go io.Copy(remote, conn)
	io.Copy(conn, remote)
}

// --- AUTO-SETUP SSH KEY (ssh-copy-id) ---
func generateSSHKeyPair() (string, string, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return "", "", err
	}
	privateKeyPEM := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)}
	privateKeyStr := string(pem.EncodeToMemory(privateKeyPEM))
	pub, err := ssh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		return "", "", err
	}
	publicKeyStr := string(ssh.MarshalAuthorizedKey(pub))
	return privateKeyStr, publicKeyStr, nil
}

func handleSSHKeySetup(hostID int, workspaceID int) {
	creds, err := getHostCredentials(hostID, workspaceID)
	if err != nil {
		sendToHQ("sys_logs", hostID, "main", "Errore setup chiavi: impossibile recuperare credenziali.")
		return
	}
	if creds.Password == "" {
		sendToHQ("sys_logs", hostID, "main", "Errore setup chiavi: password mancante, impossibile eseguire ssh-copy-id.")
		return
	}
	privKey, pubKey, err := generateSSHKeyPair()
	if err != nil {
		sendToHQ("sys_logs", hostID, "main", "Errore generazione chiavi: "+err.Error())
		return
	}
	config := &ssh.ClientConfig{
		User:            creds.User,
		Auth:            []ssh.AuthMethod{ssh.Password(creds.Password)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}
	addr := creds.IP
	if !strings.Contains(addr, ":") {
		addr += ":22"
	}
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		sendToHQ("sys_logs", hostID, "main", "Errore connessione con password per auto-setup: "+err.Error())
		return
	}
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return
	}
	defer session.Close()
	cmd := fmt.Sprintf(`sh -c "mkdir -p ~/.ssh && chmod 700 ~/.ssh && echo '%s' >> ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys"`, pubKey)
	if err := session.Run(cmd); err != nil {
		sendToHQ("sys_logs", hostID, "main", "Errore iniezione chiave: "+err.Error())
		return
	}
	sendToHQ("save_generated_key", hostID, "main", map[string]string{"private_key": privKey})
	sendToHQ("sys_logs", hostID, "main", "Setup chiavi SSH completato con successo. Da ora la password non servirà più.")
}

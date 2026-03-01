package main

import (
	"fmt"
	"log"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// SftpcSession holds the necessary details for an SFTPC connection.
type SftpcSession struct {
	Host     string
	User     string
	Password string
	Mode     string // "direct" or "bridge"

	// For Bridge mode, we hold a client connection open.
	BridgeClient *sftp.Client
}

var (
	sftpcSessions = make(map[string]SftpcSession)
	sftpcSessMux  = &sync.Mutex{}
)

// Closes and cleans up any active SFTPC session for a given hostID.
func closeSftpcSession(hostID int, wsID int) {
	sftpcSessMux.Lock()
	defer sftpcSessMux.Unlock()

	key := fmt.Sprintf("%d:%d", wsID, hostID)
	if sess, ok := sftpcSessions[key]; ok {
		if sess.BridgeClient != nil {
			sess.BridgeClient.Close()
		}
		delete(sftpcSessions, key)
		log.Printf("SFTPC session for host %d closed.", hostID)
	}
}

func handleSftpcConnect(hostID int, termID string, payload map[string]interface{}) {
	wsID := GetWorkspaceID(termID)
	closeSftpcSession(hostID, wsID)

	ip, _ := payload["ip"].(string)
	user, _ := payload["user"].(string)
	pass, _ := payload["pass"].(string)
	privKey, _ := payload["private_key"].(string)
	connType, _ := payload["connection_type"].(string)

	if ip == "" || user == "" {
		sendToHQ("sftpc_op_res", hostID, termID, map[string]string{"status": "error", "msg": "IP, User are required."})
		return
	}

	sessionData := SftpcSession{
		Host:     ip,
		User:     user,
		Password: pass,
		Mode:     connType,
	}

	var sftpClient *sftp.Client
	var err error

	var authMethods []ssh.AuthMethod
	if privKey != "" {
		signer, err := ssh.ParsePrivateKey([]byte(privKey))
		if err == nil {
			authMethods = append(authMethods, ssh.PublicKeys(signer))
		} else {
			log.Printf("SFTPC: Failed to parse private key: %v", err)
		}
	}
	if pass != "" {
		authMethods = append(authMethods, ssh.Password(pass))
	}
	if len(authMethods) == 0 {
		sendToHQ("sftpc_op_res", hostID, termID, map[string]string{"status": "error", "msg": "No authentication method provided."})
		return
	}

	config := &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}
	addr := ip
	if !strings.Contains(addr, ":") {
		addr += ":22"
	}

	if connType == "direct" {
		hostASession, err := ensureSession(hostID, GetSessionIDWithPrefix(termID, "main"))
		if err != nil {
			sendToHQ("sftpc_op_res", hostID, termID, map[string]string{"status": "error", "msg": "No active session with Host A to tunnel through."})
			return
		}

		netConn, err := hostASession.Client.Dial("tcp", addr)
		if err != nil {
			sendToHQ("sftpc_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Direct connection (tunnel) from Host A failed: " + err.Error()})
			return
		}

		sshConn, chans, reqs, err := ssh.NewClientConn(netConn, addr, config)
		if err != nil {
			sendToHQ("sftpc_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Direct connection (SSH handshake) failed: " + err.Error()})
			return
		}
		sshClient := ssh.NewClient(sshConn, chans, reqs)
		sftpClient, err = sftp.NewClient(sshClient)

	} else { // Bridge mode
		sshClient, err := ssh.Dial("tcp", addr, config)
		if err != nil {
			sendToHQ("sftpc_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Bridge SSH connection failed: " + err.Error()})
			return
		}
		sftpClient, err = sftp.NewClient(sshClient)
	}

	if err != nil {
		sendToHQ("sftpc_op_res", hostID, termID, map[string]string{"status": "error", "msg": "SFTP client creation failed: " + err.Error()})
		return
	}
	sessionData.BridgeClient = sftpClient

	sftpcSessMux.Lock()
	key := fmt.Sprintf("%d:%d", wsID, hostID)
	sftpcSessions[key] = sessionData
	sftpcSessMux.Unlock()

	log.Printf("SFTPC session initialized for host %d in %s mode", hostID, connType)
	sendToHQ("sftpc_op_res", hostID, termID, map[string]string{"status": "success", "action": "connect"})
	handleSftpcCommand(hostID, termID, map[string]interface{}{"action": "list", "path": "/"})
}

func handleSftpcCommand(hostID int, termID string, payload map[string]interface{}) {
	wsID := GetWorkspaceID(termID)
	sftpcSessMux.Lock()
	key := fmt.Sprintf("%d:%d", wsID, hostID)
	sftpcSess, ok := sftpcSessions[key]
	sftpcSessMux.Unlock()

	if !ok {
		sendToHQ("sftpc_op_res", hostID, termID, map[string]string{"status": "error", "msg": "No active SFTPC session."})
		return
	}

	action, _ := payload["action"].(string)

	handleSftpcClientCommand(hostID, termID, sftpcSess, action, payload)
}

// Executes SFTP commands from the Bridge to Host B
func handleSftpcClientCommand(hostID int, termID string, sess SftpcSession, action string, payload map[string]interface{}) {
	client := sess.BridgeClient
	if client == nil {
		sendToHQ("sftpc_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Bridge SFTP client not connected."})
		return
	}

	p, _ := payload["path"].(string)
	if p == "" {
		p = "."
	}

	var err error

	switch action {
	case "list":
		// Already handled below
	case "mkdir":
		name, _ := payload["name"].(string)
		if name != "" {
			err = client.Mkdir(path.Join(p, name))
		}
	case "rename":
		oldName, _ := payload["oldName"].(string)
		newName, _ := payload["newName"].(string)
		if oldName != "" && newName != "" {
			err = client.Rename(path.Join(p, oldName), path.Join(p, newName))
		}
	case "delete":
		itemPath, _ := payload["path"].(string)
		if itemPath != "" {
			// Check if it's a directory to use Remove vs Rmdir
			info, statErr := client.Stat(itemPath)
			if statErr == nil {
				if info.IsDir() {
					err = client.RemoveDirectory(itemPath)
				} else {
					err = client.Remove(itemPath)
				}
			} else {
				err = statErr
			}
		}
	default:
		sendToHQ("sftpc_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Unknown bridge command: " + action})
		return
	}

	if err != nil {
		sendToHQ("sftpc_op_res", hostID, termID, map[string]string{"status": "error", "msg": fmt.Sprintf("Bridge %s failed: %s", action, err.Error())})
		return
	}

	// For non-list actions, send success and then refresh the list
	if action != "list" {
		sendToHQ("sftpc_op_res", hostID, termID, map[string]string{"status": "success"})
	}

	// Refresh list
	files, err := client.ReadDir(p)
	if err != nil {
		// This happens if we delete the directory we are in, try to go up.
		p = path.Dir(p)
		files, err = client.ReadDir(p)
		if err != nil {
			sendToHQ("sftpc_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Bridge list failed after op: " + err.Error()})
			return
		}
	}

	var items []map[string]interface{}
	for _, f := range files {
		isLink := f.Mode()&os.ModeSymlink != 0
		items = append(items, map[string]interface{}{
			"name":   f.Name(),
			"path":   path.Join(p, f.Name()),
			"isDir":  f.IsDir(),
			"isLink": isLink,
		})
	}

	currentPath, _ := client.RealPath(p)
	sendToHQ("sftpc_list", hostID, termID, map[string]interface{}{
		"path":  currentPath,
		"items": items,
	})
}

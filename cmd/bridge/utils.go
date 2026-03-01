package main

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// SFTP Helper Functions
func getSFTPClient(sshClient *ssh.Client) (*sftp.Client, error) {
	return sftp.NewClient(sshClient)
}

// uploadDirSFTP uploads a local directory to remote via SFTP (native, no tar)
func uploadDirSFTP(client *sftp.Client, localDir, remoteDir string) error {
	return filepath.Walk(localDir, func(file string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, _ := filepath.Rel(localDir, file)
		remotePath := path.Join(remoteDir, filepath.ToSlash(relPath))

		if fi.IsDir() {
			return client.MkdirAll(remotePath)
		}

		return uploadSingleFileSFTP(client, file, remotePath)
	})
}

// uploadSingleFileSFTP uploads a single file via SFTP
func uploadSingleFileSFTP(client *sftp.Client, localFile, remotePath string) error {
	srcFile, err := os.Open(localFile)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := client.Create(remotePath)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	return err
}

// downloadDirSFTP downloads a remote directory from remote to local via SFTP (native, no tar)
func downloadDirSFTP(client *sftp.Client, remoteDir, localDir string) error {
	if err := os.MkdirAll(localDir, 0755); err != nil {
		return err
	}

	walker := client.Walk(remoteDir)
	for walker.Step() {
		if err := walker.Err(); err != nil {
			return err
		}

		remotePath := walker.Path()
		relPath, _ := filepath.Rel(remoteDir, remotePath)
		localPath := filepath.Join(localDir, relPath)

		if walker.Stat().IsDir() {
			os.MkdirAll(localPath, 0755)
		} else {
			if err := downloadSingleFileSFTP(client, remotePath, localPath); err != nil {
				return err
			}
		}
	}
	return nil
}

// downloadSingleFileSFTP downloads a single file via SFTP
func downloadSingleFileSFTP(client *sftp.Client, remotePath, localPath string) error {
	remoteFile, err := client.Open(remotePath)
	if err != nil {
		return err
	}
	defer remoteFile.Close()

	localFile, err := os.Create(localPath)
	if err != nil {
		return err
	}
	defer localFile.Close()

	_, err = io.Copy(localFile, remoteFile)
	return err
}

func decrypt(ciphertext string) (string, error) {
	if ciphertext == "" {
		return "", nil
	}
	data, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}
	c, err := aes.NewCipher(CurrentMasterKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(c)
	if err != nil {
		return "", err
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertextBytes := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertextBytes, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func runSingleCommand(client *ssh.Client, cmd string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	out, err := session.CombinedOutput(cmd)
	return string(out), err
}

func calculatePerc(used, total string) string {
	var u, t float64
	fmt.Sscanf(used, "%f", &u)
	fmt.Sscanf(total, "%f", &t)
	if t == 0 {
		return "0"
	}
	return fmt.Sprintf("%.0f", (u/t)*100)
}

func sendToHQ(msgType string, hostID int, termID string, payload interface{}) {
	wsMu.Lock()
	defer wsMu.Unlock()
	if wsConn == nil {
		log.Printf("[SENDTOHQ-ERROR] No connection to HQ. Type: %s, HostID: %d, TermID: %s", msgType, hostID, termID)
		return
	}
	log.Printf("[SENDTOHQ] Type: %s, HostID: %d, TermID: %s", msgType, hostID, termID)
	if err := wsConn.WriteJSON(JMessage{Type: msgType, HostID: hostID, TermID: termID, Payload: payload}); err != nil {
		log.Printf("[SENDTOHQ-WRITE-ERROR] %v", err)
	}
}

func startStream(id int, wsID int, t, c, m string, cli *ssh.Client) {
	streamMu.Lock()
	k := fmt.Sprintf("%d:%d:%s", wsID, id, t)
	if _, x := streamSessions[k]; x {
		streamMu.Unlock()
		return
	}
	s, err := cli.NewSession()
	if err != nil {
		streamMu.Unlock()
		return
	}
	streamSessions[k] = s
	streamMu.Unlock()

	stdout, _ := s.StdoutPipe()
	if err := s.Start(c); err != nil {
		return
	}

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		sendToHQ(m, id, t, scanner.Text())
	}
	streamMu.Lock()
	delete(streamSessions, k)
	streamMu.Unlock()
}

func stopStream(hostID int, wsID int, typeName string) {
	streamMu.Lock()
	key := fmt.Sprintf("%d:%d:%s", wsID, hostID, typeName)
	if s, exists := streamSessions[key]; exists {
		s.Close()
		delete(streamSessions, key)
	}
	streamMu.Unlock()
}

package bridge

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// handleScpCommand gestisce le operazioni di scp.
func handleScpCommand(hostID int, termID string, payload map[string]interface{}) {
	// Check tools
	if _, err := exec.LookPath("scp"); err != nil {
		sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": "scp command not found on bridge."})
		return
	}

	mode, _ := payload["mode"].(string)
	direction, _ := payload["direction"].(string)
	source, _ := payload["source"].(string)
	destination, _ := payload["destination"].(string)
	options, _ := payload["options"].(string)
	useScreen, _ := payload["use_screen"].(bool)
	isRoot, _ := payload["root"].(bool)

	hostBData, _ := payload["host_b"].(map[string]interface{})
	hostBIp, _ := hostBData["ip"].(string)
	hostBUser, _ := hostBData["user"].(string)
	hostBPass, _ := hostBData["pass"].(string)

	// Get Host A session info
	sessA, err := ensureSession(hostID, GetSessionIDWithPrefix(termID, "main"))
	if err != nil {
		sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Host A session error: " + err.Error()})
		return
	}
	credsA, err := getHostCredentials(hostID, GetWorkspaceID(termID))
	if err != nil {
		sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Host A credentials error: " + err.Error()})
		return
	}

	var finalCmd string

	// Opzioni SSH standard per evitare prompt interattivi (Host Key Checking)
	sshOpts := `-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null`

	// Assicurati che -r sia presente per copiare directory se non specificato
	if !strings.Contains(options, "-r") {
		options += " -r"
	}

	// Construct Command based on mode
	switch mode {
	case "local": // Bridge <-> Host A
		var scpCmd string

		if direction == "upload" { // Bridge -> Host A
			scpCmd = fmt.Sprintf(`scp %s %s "%s" %s@%s:"%s"`, sshOpts, options, source, credsA.User, credsA.IP, destination)
		} else { // Host A -> Bridge
			scpCmd = fmt.Sprintf(`scp %s %s %s@%s:"%s" "%s"`, sshOpts, options, credsA.User, credsA.IP, source, destination)
		}

		finalCmd = wrapRsyncAuth(scpCmd, credsA.Password)

	case "direct": // Host A -> Host B
		var scpCmdOnA string

		if direction == "upload" { // A -> B
			scpCmdOnA = fmt.Sprintf(`scp %s %s "%s" %s@%s:"%s"`, sshOpts, options, source, hostBUser, hostBIp, destination)
		} else { // B -> A
			scpCmdOnA = fmt.Sprintf(`scp %s %s %s@%s:"%s" "%s"`, sshOpts, options, hostBUser, hostBIp, source, destination)
		}

		scpCmdOnA = wrapRsyncAuth(scpCmdOnA, hostBPass)
		if isRoot {
			scpCmdOnA = wrapSudo(scpCmdOnA, credsA.Password)
		}

		// For direct mode, we execute via SSH on Host A.
		// If screen is requested, we wrap it.
		if useScreen {
			screenName := fmt.Sprintf("transfer_%d", time.Now().Unix())
			scpCmdOnA = fmt.Sprintf("screen -dmS %s bash -c '%s'", screenName, strings.ReplaceAll(scpCmdOnA, "'", "'\\''"))
		}

		out, err := runSingleCommand(sessA.Client, scpCmdOnA)
		if err != nil {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": "SCP (direct) failed: " + err.Error() + "\n" + out})
			return
		}

		msg := "SCP (direct) completed."
		if useScreen {
			msg = "SCP started in background (Screen) on Host A."
		}
		sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "success", "msg": msg})
		return

	case "bridge": // A -> Bridge -> B
		tmpDir := filepath.Join(os.TempDir(), "shelldeck_scp", fmt.Sprintf("%d", time.Now().UnixNano()))
		if err := os.MkdirAll(tmpDir, 0755); err != nil {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Failed to create temp dir: " + err.Error()})
			return
		}
		if !useScreen {
			defer os.RemoveAll(tmpDir)
		}

		tmpPath := filepath.Join(tmpDir, filepath.Base(source))
		var step1, step2 string

		if direction == "upload" { // A -> Bridge -> B
			step1 = fmt.Sprintf(`scp %s %s %s@%s:"%s" "%s"`, sshOpts, options, credsA.User, credsA.IP, source, tmpPath)
			step1 = wrapRsyncAuth(step1, credsA.Password)

			step2 = fmt.Sprintf(`scp %s %s "%s" %s@%s:"%s"`, sshOpts, options, tmpPath, hostBUser, hostBIp, destination)
			step2 = wrapRsyncAuth(step2, hostBPass)
		} else { // B -> Bridge -> A
			step1 = fmt.Sprintf(`scp %s %s %s@%s:"%s" "%s"`, sshOpts, options, hostBUser, hostBIp, source, tmpPath)
			step1 = wrapRsyncAuth(step1, hostBPass)

			step2 = fmt.Sprintf(`scp %s %s "%s" %s@%s:"%s"`, sshOpts, options, tmpPath, credsA.User, credsA.IP, destination)
			step2 = wrapRsyncAuth(step2, credsA.Password)
		}

		if useScreen {
			// Write script
			scriptContent := fmt.Sprintf("#!/bin/bash\n%s\nif [ $? -eq 0 ]; then\n  %s\nfi\nrm -rf %s\n", step1, step2, tmpDir)
			scriptFile := filepath.Join(tmpDir, "transfer.sh")
			os.WriteFile(scriptFile, []byte(scriptContent), 0755)

			screenName := fmt.Sprintf("transfer_%d", time.Now().Unix())
			finalCmd = fmt.Sprintf("screen -dmS %s %s", screenName, scriptFile)
		} else {
			// Run sequentially with progress (Bridge Local Execution)
			finalCmd = fmt.Sprintf("(%s) && (%s)", step1, step2)
		}

	default:
		sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Unknown scp mode: " + mode})
		return
	}

	// --- EXECUTION ---

	// SCREEN EXECUTION
	if useScreen {
		// Run detached, return immediately
		cmd := exec.Command("sh", "-c", finalCmd)
		err := cmd.Run()
		if err != nil {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Screen launch failed: " + err.Error()})
		} else {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Transfer started in background (Screen)."})
		}
		return
	}

	// STANDARD EXECUTION
	cmd := exec.Command("sh", "-c", finalCmd)

	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Start failed: " + err.Error()})
		return
	}

	// Progress ID (Indeterminate per SCP se non supportato parsing)
	tID := fmt.Sprintf("scp-%d", time.Now().UnixNano())
	sendToHQ("transfer_update", hostID, termID, map[string]interface{}{"id": tID, "type": direction, "filename": filepath.Base(source), "progress": 0, "status": "running"})

	// Capture stderr
	var errBuf bytes.Buffer
	go io.Copy(&errBuf, stderr)

	err = cmd.Wait()

	if err != nil {
		sendToHQ("transfer_update", hostID, termID, map[string]interface{}{"id": tID, "status": "error", "progress": 0})
		sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": "SCP failed: " + err.Error() + "\n" + errBuf.String()})
	} else {
		sendToHQ("transfer_update", hostID, termID, map[string]interface{}{"id": tID, "status": "done", "progress": 100})
		sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "success", "msg": "SCP completed."})
	}
}

func wrapSudo(script string, password string) string {
	if password == "" {
		return fmt.Sprintf("sudo sh -c '%s'", strings.ReplaceAll(script, "'", "'\\''"))
	}
	safePass := strings.ReplaceAll(password, "'", "'\\''")
	safeScript := strings.ReplaceAll(script, "'", "'\\''")
	return fmt.Sprintf("echo '%s' | sudo -S -p '' sh -c '%s'", safePass, safeScript)
}

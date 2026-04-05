package bridge

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// handleRsyncCommand gestisce le operazioni di rsync.
func handleRsyncCommand(hostID int, termID string, payload map[string]interface{}) {
	// Check tools
	if _, err := exec.LookPath("rsync"); err != nil {
		sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": "rsync command not found on bridge."})
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
	sshOpts := `-e "ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null"`

	var rsyncPathA string
	if isRoot {
		if credsA.Password == "" {
			rsyncPathA = `--rsync-path="sudo rsync"`
		} else {
			safePassA := strings.ReplaceAll(credsA.Password, "'", "'\\''")
			rsyncPathA = fmt.Sprintf(`--rsync-path="echo '%s' | sudo -S -p '' rsync"`, safePassA)
		}
	}

	// Construct Command based on mode
	switch mode {
	case "local": // Bridge <-> Host A
		var rsyncCmd string
		// Ensure progress info is requested if not using screen
		if !useScreen && !strings.Contains(options, "--info=progress2") && !strings.Contains(options, "--progress") {
			options += " --progress"
		}

		optsA := options
		if rsyncPathA != "" {
			optsA = fmt.Sprintf("%s %s", options, rsyncPathA)
		}

		if direction == "upload" { // Bridge -> Host A
			rsyncCmd = fmt.Sprintf(`rsync %s %s "%s" %s@%s:"%s"`, sshOpts, optsA, source, credsA.User, credsA.IP, destination)
		} else { // Host A -> Bridge
			rsyncCmd = fmt.Sprintf(`rsync %s %s %s@%s:"%s" "%s"`, sshOpts, optsA, credsA.User, credsA.IP, source, destination)
		}

		finalCmd = wrapRsyncAuth(rsyncCmd, credsA.Password)

	case "direct": // Host A -> Host B
		var rsyncCmdOnA string
		if !useScreen && !strings.Contains(options, "--progress") {
			options += " --progress"
		}
		if direction == "upload" { // A -> B
			rsyncCmdOnA = fmt.Sprintf(`rsync %s %s "%s" %s@%s:"%s"`, sshOpts, options, source, hostBUser, hostBIp, destination)
		} else { // B -> A
			rsyncCmdOnA = fmt.Sprintf(`rsync %s %s %s@%s:"%s" "%s"`, sshOpts, options, hostBUser, hostBIp, source, destination)
		}

		rsyncCmdOnA = wrapRsyncAuth(rsyncCmdOnA, hostBPass)
		if isRoot {
			rsyncCmdOnA = wrapSudo(rsyncCmdOnA, credsA.Password)
		}

		// For direct mode, we execute via SSH on Host A.
		// If screen is requested, we wrap it.
		if useScreen {
			screenName := fmt.Sprintf("transfer_%d", time.Now().Unix())
			// Check for screen on Host A first? Assuming yes or basic fail.
			rsyncCmdOnA = fmt.Sprintf("screen -dmS %s bash -c '%s'", screenName, strings.ReplaceAll(rsyncCmdOnA, "'", "'\\''"))
		}

		// Run on Host A
		// Note: For progress bar in direct mode, we would need to stream stdout from runSingleCommand,
		// but runSingleCommand is currently blocking/buffered.
		// For now, we support progress only in 'local' and 'bridge' (where we run local commands) OR update runSingleCommand.
		// Given constraints, 'direct' + 'progress' might be limited to final output unless we open a stream.
		// Fallback: direct mode executes and returns result at end.

		out, err := runSingleCommand(sessA.Client, rsyncCmdOnA)
		if err != nil {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Rsync (direct) failed: " + err.Error() + "\n" + out})
			return
		}

		msg := "Rsync (direct) completed."
		if useScreen {
			msg = "Rsync started in background (Screen) on Host A."
		}
		sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "success", "msg": msg})
		return

	case "bridge": // A -> Bridge -> B
		// Bridge mode with Screen is complex (2 steps).
		// We create a shell script on the bridge and run THAT in screen.
		tmpDir := filepath.Join(os.TempDir(), "shelldeck_rsync", fmt.Sprintf("%d", time.Now().UnixNano()))
		if err := os.MkdirAll(tmpDir, 0755); err != nil {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Failed to create temp dir: " + err.Error()})
			return
		}
		// If using screen, we don't defer remove, the script must do it.
		// If NOT using screen, we defer remove.
		if !useScreen {
			defer os.RemoveAll(tmpDir)
		}

		tmpPath := filepath.Join(tmpDir, filepath.Base(source))
		var step1, step2 string

		if !useScreen && !strings.Contains(options, "--progress") {
			options += " --progress"
		}

		optsA := options
		if rsyncPathA != "" {
			optsA = fmt.Sprintf("%s %s", options, rsyncPathA)
		}

		if direction == "upload" { // A -> Bridge -> B
			step1 = fmt.Sprintf(`rsync %s %s %s@%s:"%s" "%s"`, sshOpts, optsA, credsA.User, credsA.IP, source, tmpPath)
			step1 = wrapRsyncAuth(step1, credsA.Password)

			step2 = fmt.Sprintf(`rsync %s %s "%s" %s@%s:"%s"`, sshOpts, options, tmpPath, hostBUser, hostBIp, destination)
			step2 = wrapRsyncAuth(step2, hostBPass)
		} else { // B -> Bridge -> A
			step1 = fmt.Sprintf(`rsync %s %s %s@%s:"%s" "%s"`, sshOpts, options, hostBUser, hostBIp, source, tmpPath)
			step1 = wrapRsyncAuth(step1, hostBPass)

			step2 = fmt.Sprintf(`rsync %s %s "%s" %s@%s:"%s"`, sshOpts, optsA, tmpPath, credsA.User, credsA.IP, destination)
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
			// We construct a combined command or run one by one?
			// To reuse the progress parsing logic below, we ideally run one `sh -c "cmd1 && cmd2"`
			finalCmd = fmt.Sprintf("(%s) && (%s)", step1, step2)
		}

	default:
		sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Unknown rsync mode: " + mode})
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

	// STANDARD EXECUTION (With Progress)
	cmd := exec.Command("sh", "-c", finalCmd)

	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Start failed: " + err.Error()})
		return
	}

	// Progress ID
	tID := fmt.Sprintf("rsync-%d", time.Now().UnixNano())
	sendToHQ("transfer_update", hostID, termID, map[string]interface{}{"id": tID, "type": direction, "filename": filepath.Base(source), "progress": 0, "status": "running"})

	// Stdout Parsing goroutine
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			// Basic parsing for standard rsync output like: "  32,768 100%   31.25MB/s    0:00:00 (xfr#1, to-chk=0/1)"
			if strings.Contains(line, "%") {
				fields := strings.Fields(line)
				for _, f := range fields {
					if strings.HasSuffix(f, "%") {
						var perc int
						fmt.Sscanf(strings.TrimSuffix(f, "%"), "%d", &perc)
						sendToHQ("transfer_update", hostID, termID, map[string]interface{}{"id": tID, "progress": perc, "status": "running"})
						break
					}
				}
			}
		}
	}()

	// Capture stderr
	var errBuf bytes.Buffer
	go io.Copy(&errBuf, stderr)

	err = cmd.Wait()

	if err != nil {
		sendToHQ("transfer_update", hostID, termID, map[string]interface{}{"id": tID, "status": "error", "progress": 0})
		sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Rsync failed: " + err.Error() + "\n" + errBuf.String()})
	} else {
		sendToHQ("transfer_update", hostID, termID, map[string]interface{}{"id": tID, "status": "done", "progress": 100})
		sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Rsync completed."})
	}
}

// wrapRsyncAuth avvolge il comando rsync in uno script che gestisce l'autenticazione.
// Prova prima sshpass (se installato), altrimenti usa un fallback con SSH_ASKPASS e file temporaneo.
func wrapRsyncAuth(cmd string, password string) string {
	if password == "" {
		return cmd
	}
	safePass := strings.ReplaceAll(password, "'", "'\\''")

	// Script ibrido:
	// 1. Controlla se sshpass esiste. Se sì, usalo.
	// 2. Se no, crea un piccolo script askpass temporaneo, imposta le env var, esegue e pulisce.
	return fmt.Sprintf(`
if command -v sshpass >/dev/null 2>&1; then
	sshpass -p '%s' %s
else
	export SSH_ASKPASS=$(mktemp)
	chmod 700 "$SSH_ASKPASS"
	echo "#!/bin/sh" > "$SSH_ASKPASS"
	echo "echo '%s'" >> "$SSH_ASKPASS"
	export SSH_ASKPASS
	export SSH_ASKPASS_REQUIRE=force
	export DISPLAY=:0
	%s
	RET=$?
	rm -f "$SSH_ASKPASS"
	exit $RET
fi
`, safePass, cmd, safePass, cmd)
}

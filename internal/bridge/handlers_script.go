package bridge

import (
	"encoding/base64"
	"fmt"
	"path"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

func handleScriptUpload(hostID int, termID string, payload map[string]interface{}) {
	sess, err := ensureSession(hostID, GetSessionIDWithPrefix(termID, "main"))
	if err != nil {
		sendToHQ("script_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Connection Error: " + err.Error()})
		return
	}
	client := sess.Client

	contentB64, _ := payload["content"].(string)
	filename, _ := payload["filename"].(string)

	// Determine extension from filename to preserve it in tmp
	ext := path.Ext(filename)
	if ext == "" {
		ext = ".sh"
	}
	// Decode content
	// We use a temp file on the remote to handle content safely
	tmpFileName := fmt.Sprintf("shelldeck_%d%s", time.Now().UnixNano(), ext)
	tmpFile := fmt.Sprintf("/tmp/%s", tmpFileName)

	// Upload content via Stdin (Robust upload)
	contentBytes, _ := base64.StdEncoding.DecodeString(contentB64)
	sUpload, err := client.NewSession()
	if err == nil {
		stdinUpload, _ := sUpload.StdinPipe()
		sUpload.Start(fmt.Sprintf("cat > %s", tmpFile))
		stdinUpload.Write(contentBytes)
		stdinUpload.Close()
		sUpload.Wait()
		sUpload.Close()
	} else {
		sendToHQ("script_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Upload failed: " + err.Error()})
		return
	}

	// Fix line endings (dos2unix) just in case
	runSingleCommand(client, fmt.Sprintf("sed -i 's/\r$//' %s", tmpFile))
	runSingleCommand(client, fmt.Sprintf("chmod +x %s", tmpFile))

	// Notify Frontend that upload is done
	sendToHQ("script_upload_ack", hostID, termID, map[string]string{
		"status":      "success",
		"remote_file": tmpFileName,
	})
}

func handleScriptRun(hostID int, termID string, payload map[string]interface{}) {
	sess, err := ensureSession(hostID, GetSessionIDWithPrefix(termID, "main"))
	if err != nil {
		return
	}
	client := sess.Client

	mode, _ := payload["mode"].(string)
	tmpFileName, _ := payload["filename"].(string) // Remote filename from ack
	tmpFile := fmt.Sprintf("/tmp/%s", tmpFileName)
	runAsUser, _ := payload["run_as_user"].(string)

	scriptPath, _ := payload["script_path"].(string)
	filename := path.Base(scriptPath)
	if filename == "." || filename == "/" {
		filename = tmpFileName
	}

	// Per RAM Exec, il contenuto arriva direttamente nel payload
	ramContent, _ := payload["content"].(string)
	sysdType, _ := payload["sysd_type"].(string)
	sysdName, _ := payload["sysd_name"].(string)

	// cmdPrefix non viene usato qui per il runtime interattivo, costruiamo il comando specifico sotto

	switch mode {
	case "runtime":
		// interactive is always true for runtime in this flow
		useScreen, _ := payload["use_screen"].(bool)
		screenLabel, _ := payload["screen_label"].(string)
		interpreter, _ := payload["interpreter"].(string)

		// 1. Costruisci il comando base da eseguire dentro /tmp
		// Es: "./script.sh" oppure "python3 script.py"
		runCmd := fmt.Sprintf("./%s", tmpFileName)
		if interpreter != "" {
			runCmd = fmt.Sprintf("%s %s", interpreter, tmpFileName)
		}

		// 2. Avvolgi in una shell che fa cd /tmp, esegue, e poi rimane aperta
		// "cd /tmp && echo 'Start' && ./script; echo 'End'; exec bash"
		wrappedCmd := fmt.Sprintf("cd /tmp && echo '--- Shelldeck Start ---' && %s; echo '--- Shelldeck End ---'; exec bash", runCmd)

		var finalCmd string

		if useScreen {
			// Check if screen is installed
			if out, _ := runSingleCommand(client, "command -v screen"); strings.TrimSpace(out) == "" {
				runSingleCommand(client, fmt.Sprintf("rm -f %s", tmpFile))
				sendToHQ("script_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Screen is not installed on the remote host."})
				return
			}
			if screenLabel == "" {
				screenLabel = "shelldeck_script"
			}

			// FIX SCREEN: Usa exec bash alla fine per mantenere la shell dentro screen aperta
			safeWrapped := strings.ReplaceAll(wrappedCmd, "'", "'\\''")
			finalCmd = fmt.Sprintf("screen -S %s bash -c '%s; exec bash'", screenLabel, safeWrapped)
		} else {
			// Esegui direttamente in bash
			safeWrapped := strings.ReplaceAll(wrappedCmd, "'", "'\\''")
			finalCmd = fmt.Sprintf("bash -c '%s'", safeWrapped)
		}

		// Gestione SUDO con ASKPASS per evitare problemi di echo e stdin
		var askPassFile string
		if runAsUser == "root" {
			// 1. Crea script askpass temporaneo
			askPassFile = fmt.Sprintf("/tmp/shelldeck_askpass_%d.sh", time.Now().UnixNano())
			safePass := strings.ReplaceAll(sess.Password, "'", "'\\''")
			askPassContent := fmt.Sprintf("#!/bin/sh\necho '%s'", safePass)

			// Carica lo script askpass
			runSingleCommand(client, fmt.Sprintf("cat > %s << 'EOF'\n%s\nEOF", askPassFile, askPassContent))
			runSingleCommand(client, fmt.Sprintf("chmod 700 %s", askPassFile))

			// 2. Costruisci comando sudo che usa askpass
			safeFinal := strings.ReplaceAll(finalCmd, "'", "'\\''")
			// -A usa askpass, -k ignora cache, preserviamo env per SUDO_ASKPASS
			// Eseguiamo, salviamo exit code, rimuoviamo askpass, e usciamo con exit code originale
			finalCmd = fmt.Sprintf("export SUDO_ASKPASS='%s'; sudo -A -k bash -c '%s'; RET=$?; rm -f '%s'; exit $RET", askPassFile, safeFinal, askPassFile)
		}

		if true { // Always interactive
			go func() {
				s, err := client.NewSession()
				if err != nil {
					wsMu.Lock()
					delete(sshSessions, fmt.Sprintf("%d:%s", hostID, termID))
					wsMu.Unlock()
					sendToHQ("script_output", hostID, termID, map[string]string{"status": "error", "output": "Session error: " + err.Error()})
					return
				}

				modes := ssh.TerminalModes{
					ssh.ECHO:          1,
					ssh.TTY_OP_ISPEED: 14400,
					ssh.TTY_OP_OSPEED: 14400,
				}
				if err := s.RequestPty("xterm-256color", 40, 80, modes); err != nil {
					wsMu.Lock()
					delete(sshSessions, fmt.Sprintf("%d:%s", hostID, termID))
					wsMu.Unlock()
					s.Close()
					return
				}

				stdin, _ := s.StdinPipe()
				stdout, _ := s.StdoutPipe()
				stderr, _ := s.StderrPipe()

				wsMu.Lock()
				// Aggiorniamo il placeholder con la sessione reale
				sshSessions[fmt.Sprintf("%d:%s", hostID, termID)] = &SSHSessionWrapper{Session: s, Stdin: stdin, Client: client}
				wsMu.Unlock()

				if err := s.Start(finalCmd); err != nil {
					wsMu.Lock()
					delete(sshSessions, fmt.Sprintf("%d:%s", hostID, termID))
					wsMu.Unlock()
					s.Close()
					sendToHQ("script_output", hostID, termID, map[string]string{"status": "error", "output": "Execution failed: " + err.Error()})
					return
				}

				go forwardReader(hostID, termID, stdout)
				go forwardReader(hostID, termID, stderr)

				s.Wait()
				s.Close()
				runSingleCommand(client, fmt.Sprintf("rm -f %s", tmpFile))
				// Cleanup di sicurezza per askpass se la sessione muore male
				if askPassFile != "" {
					runSingleCommand(client, fmt.Sprintf("rm -f %s", askPassFile))
				}
				// Close message handled by client disconnect usually, but we can send explicit close
				sendToHQ("ssh_close", hostID, termID, nil) // Signal frontend to close/reset
			}()
			return
		}

	case "drop":
		targetDir, _ := payload["target_path"].(string)

		// Smart Path Logic
		if targetDir == "" {
			// Empty -> Current dir + original filename
			targetDir = "./" + filename
		} else if !strings.Contains(targetDir, "/") {
			// Just a name -> Current dir + new name
			targetDir = "./" + targetDir
		} else if strings.HasSuffix(targetDir, "/") {
			// Ends in / -> Directory + original filename
			targetDir = targetDir + filename
		}
		// Else: It's a full path (e.g. /opt/script.sh) -> Use as is

		targetPath := targetDir

		// Ensure target directory exists
		targetDirOnly := path.Dir(targetPath)
		mkdirCmd := fmt.Sprintf("mkdir -p \"%s\"", targetDirOnly)

		// Use cp to copy from tmp to target
		cpCmd := fmt.Sprintf("cp -f \"%s\" \"%s\"", tmpFile, targetPath)

		safePass := strings.ReplaceAll(sess.Password, "'", "'\\''")
		cmdPrefix := fmt.Sprintf("echo '%s' | sudo -S -p '' ", safePass)
		if runAsUser == "root" {
			mkdirCmd = fmt.Sprintf("%s%s", cmdPrefix, mkdirCmd)
			cpCmd = fmt.Sprintf("%scp -f \"%s\" \"%s\"", cmdPrefix, tmpFile, targetPath)
		}

		runSingleCommand(client, mkdirCmd)
		out, err := runSingleCommand(client, cpCmd)
		runSingleCommand(client, fmt.Sprintf("rm -f %s", tmpFile)) // Cleanup tmp

		if err != nil {
			sendToHQ("script_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Drop failed: " + out})
		} else {
			sendToHQ("script_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Script dropped to " + targetPath})
		}

	case "cron":
		schedule, _ := payload["schedule"].(string)    // e.g. "0 * * * *"
		savePath, _ := payload["target_path"].(string) // Reused field for save path
		cronAsRoot, _ := payload["cron_as_root"].(bool)

		finalPath := savePath
		if finalPath == "" {
			if cronAsRoot {
				finalPath = fmt.Sprintf("/opt/shelldeck/jobs/%s", filename)
			} else {
				finalPath = fmt.Sprintf("~/.shelldeck/jobs/%s", filename)
			}
		}

		// Ensure dir exists
		dir := path.Dir(finalPath)
		mkdirCmd := fmt.Sprintf("mkdir -p \"%s\"", dir)

		// Create dir, move file, add to crontab
		// Note: We use 'cp' then 'rm' pattern to be safe across filesystems
		setupCmd := fmt.Sprintf("%s && cp -f %s %s && chmod +x %s", mkdirCmd, tmpFile, finalPath, finalPath)

		var out string
		var err error

		if cronAsRoot {
			safePass := strings.ReplaceAll(sess.Password, "'", "'\\''")

			// 1. Setup File (mkdir, cp, chmod) as Root via sudo sh -c
			safeSetupCmd := strings.ReplaceAll(setupCmd, "\"", "\\\"")
			fullSetupCmd := fmt.Sprintf("echo '%s' | sudo -S -p '' sh -c \"%s\"", safePass, safeSetupCmd)
			runSingleCommand(client, fullSetupCmd)

			// 2. Update Crontab as Root using temp file (safer than pipes with sudo)
			tmpCron := fmt.Sprintf("/tmp/shelldeck_cron_%d", time.Now().UnixNano())

			// Dump current cron (ignore error if empty)
			runSingleCommand(client, fmt.Sprintf("echo '%s' | sudo -S -p '' sh -c \"crontab -l > %s 2>/dev/null || true\"", safePass, tmpCron))

			// Append new job
			line := fmt.Sprintf("%s %s # Shelldeck Job", schedule, finalPath)
			safeLine := strings.ReplaceAll(line, "\"", "\\\"")
			runSingleCommand(client, fmt.Sprintf("echo '%s' | sudo -S -p '' sh -c \"echo \\\"%s\\\" >> %s\"", safePass, safeLine, tmpCron))

			// Install & Cleanup
			out, err = runSingleCommand(client, fmt.Sprintf("echo '%s' | sudo -S -p '' crontab %s", safePass, tmpCron))
			runSingleCommand(client, fmt.Sprintf("echo '%s' | sudo -S -p '' rm -f %s", safePass, tmpCron))
		} else {
			// User mode (Standard pipe works fine here)
			runSingleCommand(client, setupCmd)
			cronCmd := fmt.Sprintf("(crontab -l 2>/dev/null; echo \"%s %s # Shelldeck Job\") | crontab -", schedule, finalPath)
			out, err = runSingleCommand(client, cronCmd)
		}
		runSingleCommand(client, fmt.Sprintf("rm -f %s", tmpFile))

		if err != nil {
			sendToHQ("script_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Cron setup failed: " + out})
		} else {
			sendToHQ("script_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Scheduled: " + schedule})
		}

	case "systemd":
		// Common setup for Service/Timer/OneShot
		cronAsRoot, _ := payload["cron_as_root"].(bool) // Reused flag for Root/User
		schedule, _ := payload["schedule"].(string)

		// Paths
		var binDir, unitDir, execDir string
		if cronAsRoot {
			binDir = "/usr/local/bin"
			unitDir = "/etc/systemd/system"
			execDir = "/usr/local/bin"
		} else {
			binDir = "~/.local/bin"
			unitDir = "~/.config/systemd/user"
			execDir = "%h/.local/bin" // Systemd specifier for home directory
		}

		fsScriptPath := fmt.Sprintf("%s/%s.sh", binDir, sysdName)    // Path for shell commands (cp, chmod)
		unitScriptPath := fmt.Sprintf("%s/%s.sh", execDir, sysdName) // Path for Systemd Unit (ExecStart)
		serviceFile := fmt.Sprintf("%s/%s.service", unitDir, sysdName)
		timerFile := fmt.Sprintf("%s/%s.timer", unitDir, sysdName)

		// Commands builder
		var cmds []string

		// 1. Prepare dirs
		cmds = append(cmds, fmt.Sprintf("mkdir -p %s %s", binDir, unitDir))

		// 2. Move script
		cmds = append(cmds, fmt.Sprintf("cp -f %s %s && chmod +x %s", tmpFile, fsScriptPath, fsScriptPath))

		// 3. Generate Service Unit
		serviceContent := fmt.Sprintf(`[Unit]
Description=Shelldeck Service: %s

[Service]
Type=%s
ExecStart=%s

[Install]
WantedBy=default.target`, sysdName, map[string]string{"service": "simple", "timer": "oneshot", "oneshot": "oneshot"}[sysdType], unitScriptPath)

		cmds = append(cmds, fmt.Sprintf("cat > %s << 'EOF'\n%s\nEOF", serviceFile, serviceContent))

		// 4. Generate Timer Unit (if needed)
		if sysdType == "timer" {
			timerContent := fmt.Sprintf(`[Unit]
Description=Timer for %s

[Timer]
OnCalendar=%s
Persistent=true

[Install]
WantedBy=timers.target`, sysdName, schedule)
			cmds = append(cmds, fmt.Sprintf("cat > %s << 'EOF'\n%s\nEOF", timerFile, timerContent))
		}

		// 5. Reload & Enable
		ctlCmd := "systemctl"
		if !cronAsRoot {
			ctlCmd = "systemctl --user"
			// Ensure linger is enabled for user timers to run when logged out
			// cmds = append(cmds, "loginctl enable-linger $USER") // Optional, might require sudo
		}

		cmds = append(cmds, fmt.Sprintf("%s daemon-reload", ctlCmd))

		targetUnit := sysdName + ".service"
		if sysdType == "timer" {
			targetUnit = sysdName + ".timer"
		}

		cmds = append(cmds, fmt.Sprintf("%s enable --now %s", ctlCmd, targetUnit))

		// Execute chain
		fullScript := strings.Join(cmds, "\n")

		if cronAsRoot {
			// Wrap in sudo sh -c
			safePass := strings.ReplaceAll(sess.Password, "'", "'\\''")
			// Use a temp script for the whole setup to avoid quoting hell
			setupScript := fmt.Sprintf("/tmp/shelldeck_setup_%d.sh", time.Now().UnixNano())
			runSingleCommand(client, fmt.Sprintf("cat > %s << 'EOS'\n%s\nEOS", setupScript, fullScript))
			runSingleCommand(client, fmt.Sprintf("chmod +x %s", setupScript))

			out, err := runSingleCommand(client, fmt.Sprintf("echo '%s' | sudo -S -p '' %s", safePass, setupScript))
			runSingleCommand(client, fmt.Sprintf("echo '%s' | sudo -S -p '' rm -f %s", safePass, setupScript))

			if err != nil {
				sendToHQ("script_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Systemd setup failed: " + out})
			} else {
				sendToHQ("script_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Systemd Unit Created & Started"})
			}
		} else {
			// User mode
			out, err := runSingleCommand(client, fullScript)
			if err != nil {
				sendToHQ("script_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Systemd setup failed: " + out})
			} else {
				sendToHQ("script_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Systemd Unit Created & Started (User Mode)"})
			}
		}
		runSingleCommand(client, fmt.Sprintf("rm -f %s", tmpFile))

	case "ram":
		// Esecuzione diretta in RAM (Paste nel terminale)
		// Non usiamo file temporanei. Inviamo il testo come input SSH.
		// Se è root, prima eleviamo.

		// Nota: Questo richiede che il frontend abbia aperto un terminale interattivo (xterm)
		// e che noi inviamo i dati a quel terminale.
		// In realtà, 'handleScriptRun' qui gira nel backend. Per "incollare", dobbiamo scrivere nello stdin della sessione.
		// Ma per "RAM Exec", vogliamo vedere l'output in tempo reale.
		// Quindi riutilizziamo la logica "runtime" ma senza il comando file.

		go func() {
			s, err := client.NewSession()
			if err != nil {
				return
			}

			modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
			if err := s.RequestPty("xterm-256color", 40, 80, modes); err != nil {
				s.Close()
				return
			}

			stdin, _ := s.StdinPipe()
			stdout, _ := s.StdoutPipe()
			stderr, _ := s.StderrPipe()

			wsMu.Lock()
			sshSessions[fmt.Sprintf("%d:%s", hostID, termID)] = &SSHSessionWrapper{Session: s, Stdin: stdin, Client: client}
			wsMu.Unlock()

			// Avvia shell
			if err := s.Start("/bin/bash"); err != nil {
				s.Close()
				return
			}

			go forwardReader(hostID, termID, stdout)
			go forwardReader(hostID, termID, stderr)

			// Inietta comandi
			time.Sleep(500 * time.Millisecond)

			// Se root, eleva prima
			if runAsUser == "root" {
				// Usa sudo -s per shell interattiva root
				// Gestione password via stdin non è ideale qui, meglio sudo -S
				// Ma sudo -S non dà prompt.
				// Proviamo sudo -i e aspettiamo
				stdin.Write([]byte("sudo -i\n"))
				time.Sleep(1 * time.Second)
				stdin.Write([]byte(sess.Password + "\n"))
				time.Sleep(1 * time.Second)
			}

			// Incolla lo script
			// Aggiungiamo echo finale per conferma visuale
			stdin.Write([]byte(ramContent + "\n"))
			stdin.Write([]byte("echo '\n[RAM Exec Finished]'\n"))

			s.Wait()
			s.Close()
			sendToHQ("ssh_close", hostID, termID, nil)
		}()
	}
}

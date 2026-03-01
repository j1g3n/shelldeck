package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"path"          // IMPORTANTE: Gestisce percorsi Linux (con /) anche se siamo su Windows
	"path/filepath" // IMPORTANTE: Gestisce percorsi Locali del PC dove gira il bridge
	"strconv"
	"strings"
	"time"
)

func handleFSCommand(hostID int, termID string, payload map[string]interface{}) {
	sess, err := ensureSession(hostID, GetSessionIDWithPrefix(termID, "main"))
	if err != nil {
		sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Connection failed: " + err.Error()})
		return
	}
	action, _ := payload["action"].(string)
	isRoot, _ := payload["root"].(bool)
	client := sess.Client

	cmdPrefix := ""
	if isRoot {
		safePass := strings.ReplaceAll(sess.Password, "'", "'\\''")
		cmdPrefix = fmt.Sprintf("echo '%s' | sudo -S -p '' ", safePass)
	}

	switch action {
	case "get_pwd":
		out, _ := runSingleCommand(client, "pwd")
		sendToHQ("pwd_result", hostID, termID, map[string]string{"path": strings.TrimSpace(out)})
	// --- LISTING (Locale e Remoto) ---
	case "bridge_list":
		rPath, _ := payload["path"].(string)
		curr, _ := payload["current"].(string)
		bPath := "."
		if rPath != "" && rPath != "." {
			if filepath.IsAbs(rPath) {
				bPath = rPath
			} else {
				bPath = filepath.Join(curr, rPath)
			}
		}
		bPath = filepath.Clean(bPath)
		abs, _ := filepath.Abs(bPath)
		files, _ := os.ReadDir(abs)
		var items []BridgeFileItem
		for _, f := range files {
			items = append(items, BridgeFileItem{Name: f.Name(), IsDir: f.IsDir(), Path: filepath.Join(abs, f.Name())})
		}
		sendToHQ("bridge_fs_list", hostID, termID, map[string]interface{}{"path": abs, "parent": filepath.Dir(abs), "items": items})

	case "bridge_mkdir":
		p, _ := payload["path"].(string)
		n, _ := payload["name"].(string)
		fullPath := filepath.Join(p, n)
		err := os.MkdirAll(fullPath, 0755)
		if err != nil {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Failed to create local directory: " + err.Error()})
		} else {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "success_local"})
		}

	case "list_long":
		p, _ := payload["path"].(string)
		// Tenta comando GNU (più preciso), fallback su standard (compatibile con Alpine/BSD/macOS)
		cmd := fmt.Sprintf("%sLC_ALL=C ls -lAF --time-style=long-iso --group-directories-first \"%s\" 2>/dev/null || %sLC_ALL=C ls -lAF \"%s\"", cmdPrefix, p, cmdPrefix, p)
		out, _ := runSingleCommand(client, cmd)
		sendToHQ("fs_list", hostID, termID, out)

	// --- LETTURA / SCRITTURA ---
	case "read":
		f, _ := payload["filename"].(string)
		out, _ := runSingleCommand(client, fmt.Sprintf("%scat \"%s\"", cmdPrefix, f))
		sendToHQ("file_content", hostID, termID, map[string]string{"filename": f, "content": out})

	case "read_base64":
		f, _ := payload["filename"].(string)
		// Legge il file e lo converte in base64 (senza wrap)
		cmd := fmt.Sprintf("%scat \"%s\" | base64 -w 0", cmdPrefix, f)
		out, err := runSingleCommand(client, cmd)
		if err != nil {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Read failed: " + err.Error()})
		} else {
			sendToHQ("file_content_base64", hostID, termID, map[string]string{"filename": f, "content": strings.TrimSpace(out)})
		}

	case "save":
		f, _ := payload["filename"].(string)
		c, _ := payload["content"].(string)

		tmp := fmt.Sprintf("/tmp/save_%d.tmp", time.Now().UnixNano())
		s, err := client.NewSession()
		if err != nil {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Session error: " + err.Error()})
			return
		}
		stdin, _ := s.StdinPipe()

		if err := s.Start("cat > " + tmp); err != nil {
			s.Close()
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Start error: " + err.Error()})
			return
		}

		stdin.Write([]byte(c))
		stdin.Close()
		s.Wait()
		s.Close()

		mvCmd := fmt.Sprintf("mv -f \"%s\" \"%s\"", tmp, f)
		if isRoot {
			mvCmd = fmt.Sprintf("%smv -f \"%s\" \"%s\"", cmdPrefix, tmp, f)
		}

		out, err := runSingleCommand(client, mvCmd)
		if err != nil {
			runSingleCommand(client, "rm -f "+tmp)
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Save failed: " + err.Error() + " " + out})
		} else {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "success"})
		}

	// --- OPERAZIONI FILE (Create, Rename, Delete...) ---
	case "create":
		t, _ := payload["type"].(string)
		p, _ := payload["path"].(string)
		n, _ := payload["name"].(string)
		full := path.Join(p, n)
		cmd := ""
		if t == "dir" {
			cmd = fmt.Sprintf("%smkdir -p \"%s\"", cmdPrefix, full)
		} else {
			cmd = fmt.Sprintf("%stouch \"%s\"", cmdPrefix, full)
		}
		out, err := runSingleCommand(client, cmd)
		if err != nil {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": err.Error() + ": " + out})
		} else {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "success"})
		}

	case "rename":
		p, _ := payload["path"].(string)
		o, _ := payload["oldName"].(string)
		n, _ := payload["newName"].(string)
		oldPath := path.Join(p, o)
		newPath := path.Join(p, n)
		out, err := runSingleCommand(client, fmt.Sprintf("%smv \"%s\" \"%s\"", cmdPrefix, oldPath, newPath))
		if err != nil {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": err.Error() + ": " + out})
		} else {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "success"})
		}

	case "delete":
		p, _ := payload["path"].(string)
		if p != "/" && p != "" {
			out, err := runSingleCommand(client, fmt.Sprintf("%srm -rf \"%s\"", cmdPrefix, p))
			if err != nil {
				sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": err.Error() + ": " + out})
			} else {
				sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "success"})
			}
		} else {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Cannot delete root or empty path"})
		}

	case "copy_paste":
		src, _ := payload["source"].(string)
		dst, _ := payload["destDir"].(string)
		newN, _ := payload["newName"].(string)
		dstFull := path.Join(dst, newN)
		out, err := runSingleCommand(client, fmt.Sprintf("%scp -r \"%s\" \"%s\"", cmdPrefix, src, dstFull))
		if err != nil {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": err.Error() + ": " + out})
		} else {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "success"})
		}

	case "move":
		src, _ := payload["source"].(string)
		dst, _ := payload["destDir"].(string)
		newN, _ := payload["newName"].(string)
		dstFull := path.Join(dst, newN)
		out, err := runSingleCommand(client, fmt.Sprintf("%smv \"%s\" \"%s\"", cmdPrefix, src, dstFull))
		if err != nil {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": err.Error() + ": " + out})
		} else {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "success"})
		}

	case "link":
		t, _ := payload["target"].(string)
		ln, _ := payload["linkName"].(string)
		lt, _ := payload["linkType"].(string)
		bp, _ := payload["path"].(string)
		fullLink := path.Join(bp, ln)
		out, err := runSingleCommand(client, fmt.Sprintf("%sln %s \"%s\" \"%s\"", cmdPrefix, lt, t, fullLink))
		if err != nil {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": err.Error() + ": " + out})
		} else {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "success"})
		}

	case "set_perms":
		p, _ := payload["path"].(string)
		cm, _ := payload["chmod"].(string)
		co, _ := payload["chown"].(string)
		var err error
		var out string
		if cm != "" {
			out, err = runSingleCommand(client, fmt.Sprintf("%schmod %s \"%s\"", cmdPrefix, cm, p))
		}
		if err == nil && co != "" {
			out, err = runSingleCommand(client, fmt.Sprintf("%schown %s \"%s\"", cmdPrefix, co, p))
		}
		if err != nil {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": err.Error() + ": " + out})
		} else {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "success"})
		}

	case "wget":
		u, _ := payload["url"].(string)
		p, _ := payload["path"].(string)
		out, err := runSingleCommand(client, fmt.Sprintf("%swget -P \"%s\" %s", cmdPrefix, p, u))
		if err != nil {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": err.Error() + ": " + out})
		} else {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "success"})
		}

	// --- TRANSFER (Upload/Download Browser) ---
	case "upload":
		f, _ := payload["filename"].(string)
		c, _ := payload["content"].(string)
		p, _ := payload["path"].(string)
		full := path.Join(p, f)
		d, _ := base64.StdEncoding.DecodeString(c)
		tmp := fmt.Sprintf("/tmp/upload_%d.tmp", time.Now().UnixNano())

		s, _ := client.NewSession()
		stdin, _ := s.StdinPipe()
		s.Start("cat > " + tmp)
		stdin.Write(d)
		stdin.Close()
		s.Wait()
		s.Close()

		mvCmd := fmt.Sprintf("mv \"%s\" \"%s\"", tmp, full)
		if isRoot {
			mvCmd = fmt.Sprintf("%smv \"%s\" \"%s\"", cmdPrefix, tmp, full)
		}
		out, err := runSingleCommand(client, mvCmd)
		runSingleCommand(client, "rm -f "+tmp)

		if err != nil {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": err.Error() + ": " + out})
		} else {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "success"})
		}

	case "download":
		f, _ := payload["filename"].(string)
		checkCmd := fmt.Sprintf("%stest -d \"%s\" && echo 'DIR' || echo 'FILE'", cmdPrefix, f)
		isDirRes, _ := runSingleCommand(client, checkCmd)
		isDir := strings.TrimSpace(isDirRes) == "DIR"

		var streamCmd, downName string
		if isDir {
			clean := strings.TrimSuffix(f, "/")
			streamCmd = fmt.Sprintf("%star -czf - -C \"%s\" \"%s\" | base64 -w 0", cmdPrefix, path.Dir(clean), path.Base(clean))
			downName = path.Base(clean) + ".tar.gz"
		} else {
			streamCmd = fmt.Sprintf("%scat \"%s\" | base64 -w 0", cmdPrefix, f)
			downName = path.Base(f)
		}

		out, err := runSingleCommand(client, streamCmd)
		if err != nil {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Download failed: " + err.Error()})
			return
		}
		sendToHQ("file_download_res", hostID, termID, map[string]interface{}{"filename": downName, "is_dir": isDir, "content": strings.TrimSpace(out)})

	// --- TRANSFER NATIVE (Bridge <-> Host) USING SFTP ---
	case "upload_native":
		lPath, _ := payload["localPath"].(string)
		rDir, _ := payload["remotePath"].(string)
		fileName := filepath.Base(lPath)
		rFull := path.Join(rDir, fileName)
		tID := fmt.Sprintf("up-%d", time.Now().UnixNano())

		fileInfo, err := os.Stat(lPath)
		if err != nil {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Cannot stat local file: " + err.Error()})
			return
		}

		sendToHQ("transfer_update", hostID, termID, map[string]interface{}{"id": tID, "type": "upload", "filename": fileName, "progress": 0, "status": "running"})

		// Create SFTP client
		sftpClient, err := getSFTPClient(client)
		if err != nil {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": "SFTP client creation failed: " + err.Error()})
			return
		}
		defer sftpClient.Close()

		var uploadErr error
		if fileInfo.IsDir() {
			// Directory upload via SFTP (native, no tar)
			rFull = rDir + "/" + fileName
			if isRoot {
				// For root, create directory first
				runSingleCommand(client, fmt.Sprintf("mkdir -p \"%s\"", rFull))
			}
			uploadErr = uploadDirSFTP(sftpClient, lPath, rFull)
		} else {
			// File upload via SFTP
			uploadErr = uploadSingleFileSFTP(sftpClient, lPath, rFull)
		}

		if uploadErr != nil {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Upload failed: " + uploadErr.Error()})
			return
		}

		sendToHQ("transfer_update", hostID, termID, map[string]interface{}{"id": tID, "status": "done", "progress": 100})
		sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "success", "action": "upload_native"})

	case "download_native":
		rFile, _ := payload["remoteFile"].(string)
		lDir, _ := payload["localDestDir"].(string)
		if lDir == "" {
			os.Mkdir("downloads", 0755)
			lDir = "downloads"
		}

		checkCmd := fmt.Sprintf("%stest -d \"%s\" && echo 'DIR' || echo 'FILE'", cmdPrefix, rFile)
		isDirRes, err := runSingleCommand(client, checkCmd)
		if err != nil {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Failed to check remote path type: " + err.Error()})
			return
		}
		isDir := strings.TrimSpace(isDirRes) == "DIR"

		tID := fmt.Sprintf("down-%d", time.Now().UnixNano())
		fileName := path.Base(rFile)
		sendToHQ("transfer_update", hostID, termID, map[string]interface{}{"id": tID, "type": "download", "filename": fileName, "progress": 0, "status": "running"})

		// Create SFTP client
		sftpClient, err := getSFTPClient(client)
		if err != nil {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": "SFTP client creation failed: " + err.Error()})
			return
		}
		defer sftpClient.Close()

		var downloadErr error
		if isDir {
			// Directory download via SFTP (native, no tar)
			lDest := filepath.Join(lDir, fileName)
			downloadErr = downloadDirSFTP(sftpClient, rFile, lDest)
		} else {
			// File download via SFTP
			lDest := filepath.Join(lDir, fileName)
			downloadErr = downloadSingleFileSFTP(sftpClient, rFile, lDest)
		}

		if downloadErr != nil {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Download failed: " + downloadErr.Error()})
			return
		}

		sendToHQ("transfer_update", hostID, termID, map[string]interface{}{"id": tID, "status": "done", "progress": 100})
		sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "success", "action": "download_native"})

	case "search_recursive":
		searchPath, _ := payload["path"].(string)
		query, _ := payload["query"].(string)

		// Sanitizzazione di base per i doppi apici
		safeQuery := strings.ReplaceAll(query, `"`, `\"`)

		// Ottimizzazione: eseguiamo il controllo directory/file direttamente nel comando Bash remoto.
		// Restituisce l'output nel formato "1|/percorso/dir" o "0|/percorso/file" in una singola esecuzione.
		findCmd := fmt.Sprintf(`%sfind "%s" -iname "*%s*" 2>/dev/null | head -50 | while read -r f; do if [ -d "$f" ]; then echo "1|$f"; else echo "0|$f"; fi; done`, cmdPrefix, searchPath, safeQuery)

		out, _ := runSingleCommand(client, findCmd)

		// FONDAMENTALE: Inizializza come slice vuoto per restituire "[]" e non "null" in JSON
		results := make([]map[string]interface{}, 0)

		// Se non ci sono risultati, invia subito l'array vuoto senza fare loop
		if strings.TrimSpace(out) == "" {
			sendToHQ("search_results", hostID, termID, results)
			return
		}

		lines := strings.Split(strings.TrimSpace(out), "\n")

		for _, line := range lines {
			if line == "" {
				continue
			}

			// Separiamo il flag booleano (1 o 0) dal percorso reale
			parts := strings.SplitN(line, "|", 2)
			if len(parts) != 2 {
				continue
			}

			isDir := parts[0] == "1"
			fullPath := parts[1]
			fileName := path.Base(fullPath)

			results = append(results, map[string]interface{}{
				"name":  fileName,
				"path":  fullPath,
				"isDir": isDir,
			})
		}

		sendToHQ("search_results", hostID, termID, results)

	case "search_content":
		searchPath, _ := payload["path"].(string)
		query, _ := payload["query"].(string)

		// Racchiudiamo tutto in una Goroutine asincrona.
		// In questo modo il server Go non si blocca ad aspettare grep.
		go func(p, q string) {
			safeQuery := strings.ReplaceAll(q, "'", `'\''`)

			// 1. Aggiunto "| head -100" alla fine dell'intero blocco per limitare l'output.
			// Questo impedisce al browser di freezarsi e ferma grep appena trova 100 risultati.
			cmd := fmt.Sprintf(`%s(grep -rnI '%s' "%s" 2>/dev/null; find "%s" -type f -iname '*%s*' 2>/dev/null | sed 's/$/:1:[Trovato nel nome del file]/') | head -100`, cmdPrefix, safeQuery, p, p, safeQuery)

			out, err := runSingleCommand(client, cmd)

			if err != nil && strings.TrimSpace(out) == "" {
				sendToHQ("search_content_results", hostID, termID, []map[string]interface{}{})
				return
			}

			lines := strings.Split(strings.TrimSpace(out), "\n")
			results := make([]map[string]interface{}, 0)

			for _, line := range lines {
				if line == "" {
					continue
				}
				parts := strings.SplitN(line, ":", 3)
				if len(parts) < 3 {
					continue
				}
				lineNumber, _ := strconv.Atoi(parts[1])
				results = append(results, map[string]interface{}{
					"file":    parts[0],
					"line":    lineNumber,
					"content": parts[2],
				})
			}

			sendToHQ("search_content_results", hostID, termID, results)
		}(searchPath, query) // Passiamo le variabili alla goroutine
	case "compress":
		basePath, _ := payload["path"].(string)
		format, _ := payload["format"].(string)
		archiveName, _ := payload["archiveName"].(string)

		pathsRaw, _ := payload["paths"].([]interface{})
		var pathsStr string
		for _, p := range pathsRaw {
			pathsStr += fmt.Sprintf("\"%s\" ", p.(string))
		}

		var cmd string
		if format == "zip" {
			cmd = fmt.Sprintf("%scd \"%s\" && zip -r \"%s.zip\" %s", cmdPrefix, basePath, archiveName, pathsStr)
		} else {
			cmd = fmt.Sprintf("%scd \"%s\" && tar -czvf \"%s.tar.gz\" %s", cmdPrefix, basePath, archiveName, pathsStr)
		}

		out, err := runSingleCommand(client, cmd)
		if err != nil {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Compression failed: " + err.Error() + "\n" + out})
		} else {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "success"})
		}

	case "extract":
		f, _ := payload["file"].(string)
		basePath := path.Dir(f)

		var cmd string
		if strings.HasSuffix(f, ".zip") {
			cmd = fmt.Sprintf("%sunzip \"%s\" -d \"%s\"", cmdPrefix, f, basePath)
		} else if strings.HasSuffix(f, ".tar.gz") || strings.HasSuffix(f, ".tgz") {
			cmd = fmt.Sprintf("%star -xzvf \"%s\" -C \"%s\"", cmdPrefix, f, basePath)
		} else if strings.HasSuffix(f, ".tar") {
			cmd = fmt.Sprintf("%star -xvf \"%s\" -C \"%s\"", cmdPrefix, f, basePath)
		} else {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Formato non supportato per l'estrazione automatica."})
			return
		}

		out, err := runSingleCommand(client, cmd)
		if err != nil {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Extract failed: " + err.Error() + "\n" + out})
		} else {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "success"})
		}

	case "autocomplete":
		partialPath, _ := payload["path"].(string)
		side, _ := payload["side"].(string)
		var matches []string

		if side == "local" {
			matches, _ = filepath.Glob(partialPath + "*")
		} else { // remote
			// Note: This is a basic implementation. It might not handle all edge cases with special characters.
			// Using `ls -d` to list directory entries themselves, not their content.
			cmd := fmt.Sprintf("ls -d %s* 2>/dev/null", partialPath)
			out, err := runSingleCommand(client, cmd)
			if err == nil {
				matches = strings.Split(strings.TrimSpace(out), "\n")
			}
		}
		// In case of no matches or an error, an empty array is sent, which is fine.
		sendToHQ("autocomplete_res", hostID, termID, map[string]interface{}{"matches": matches, "side": side})

	case "diff":
		p1, _ := payload["path1"].(string)
		p2, _ := payload["path2"].(string)
		ignoreSpace, _ := payload["ignoreSpace"].(bool)
		ignoreCase, _ := payload["ignoreCase"].(bool)
		ignoreBlank, _ := payload["ignoreBlank"].(bool)
		saveOutput, _ := payload["saveOutput"].(bool)
		outputFile, _ := payload["outputFile"].(string)

		opts := "-u" // Unified diff format
		if ignoreSpace {
			opts += "w"
		}
		if ignoreCase {
			opts += "i"
		}
		if ignoreBlank {
			opts += "B"
		}

		// Sanitizzazione input per evitare command injection e gestire spazi/caratteri speciali
		p1 = strings.ReplaceAll(p1, "'", "'\\''")
		p2 = strings.ReplaceAll(p2, "'", "'\\''")

		var cmd string
		if saveOutput && outputFile != "" {
			outputFile = strings.ReplaceAll(outputFile, "'", "'\\''")
			// Usa tee per salvare su file e stampare su stdout contemporaneamente
			cmd = fmt.Sprintf("%stimeout 10s diff %s '%s' '%s' | tee '%s' || true", cmdPrefix, opts, p1, p2, outputFile)
		} else {
			// Esegue diff con timeout di 10s per evitare blocchi. || true gestisce exit code 1 (diff found)
			cmd = fmt.Sprintf("%stimeout 10s diff %s '%s' '%s' || true", cmdPrefix, opts, p1, p2)
		}
		out, err := runSingleCommand(client, cmd)

		if err != nil {
			sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Diff error: " + err.Error() + "\n" + out})
		} else {
			sendToHQ("diff_result", hostID, termID, map[string]string{"content": out})
		}

	case "smart_open":
		p, _ := payload["path"].(string)

		// Verifica il tipo di file seguendo i link simbolici (-L)
		checkCmd := fmt.Sprintf("%sstat -L -c %%F \"%s\"", cmdPrefix, p)
		typeOut, _ := runSingleCommand(client, checkCmd)
		typeOut = strings.ToLower(strings.TrimSpace(typeOut))

		if strings.Contains(typeOut, "directory") {
			// È una directory (o link a directory): elenca il contenuto
			// Aggiungiamo / alla fine per forzare ls a mostrare il contenuto e non il link stesso
			lsCmd := fmt.Sprintf("%sLC_ALL=C ls -lhAF --group-directories-first \"%s/\"", cmdPrefix, p)
			out, _ := runSingleCommand(client, lsCmd)
			sendToHQ("smart_open_res", hostID, termID, map[string]string{"type": "dir", "path": p, "content": out})
		} else {
			// È un file (o altro): prova a leggerlo
			catCmd := fmt.Sprintf("%scat \"%s\"", cmdPrefix, p)
			out, err := runSingleCommand(client, catCmd)
			if err != nil {
				sendToHQ("fs_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Smart open failed: " + err.Error()})
			} else {
				sendToHQ("smart_open_res", hostID, termID, map[string]string{"type": "file", "path": p, "content": out})
			}
		}
	}

}

package main

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

func handleMariaDBCommand(hostID int, termID string, payload map[string]interface{}) {
	sess, err := ensureSession(hostID, GetSessionIDWithPrefix(termID, "mariadb"))
	if err != nil {
		sendToHQ("mariadb_error", hostID, termID, "Connection Error: "+err.Error())
		return
	}

	action, _ := payload["action"].(string)
	dbUser, _ := payload["db_user"].(string)
	dbPass, _ := payload["db_pass"].(string)

	// Prepare sudo prefix for system ops (config edit)
	safeSudoPass := strings.ReplaceAll(sess.Password, "'", "'\\''")
	sudoPrefix := fmt.Sprintf("echo '%s' | sudo -S -p '' ", safeSudoPass)

	// Costruisci il comando base. Se c'è user/pass usa quelli, altrimenti sudo (socket auth)
	baseCmd := "sudo mysql"
	baseDump := "sudo mysqldump"

	if dbUser != "" {
		// Escape basilare per la password per evitare injection nella shell
		safePass := strings.ReplaceAll(dbPass, "'", "'\\''")
		baseCmd = fmt.Sprintf("mysql -u %s -p'%s'", dbUser, safePass)
		baseDump = fmt.Sprintf("mysqldump -u %s -p'%s'", dbUser, safePass)
	}

	client := sess.Client

	switch action {
	case "check_status":
		// Controlla stato systemd (prova mariadb, fallback mysql)
		cmd := "if systemctl is-active --quiet mariadb; then echo 'active'; else if systemctl is-active --quiet mysql; then echo 'active'; else echo 'inactive'; fi; fi"
		out, _ := runSingleCommand(client, cmd)
		status := strings.TrimSpace(out)

		// Ottieni ultimi logs
		logCmd := fmt.Sprintf("%sjournalctl -u mariadb -u mysql -n 50 --no-pager", sudoPrefix)
		logs, _ := runSingleCommand(client, logCmd)

		sendToHQ("mariadb_status", hostID, termID, map[string]string{
			"status": status,
			"logs":   logs,
		})

	case "service_op":
		op, _ := payload["op"].(string) // start, stop, restart
		if op != "start" && op != "stop" && op != "restart" {
			return
		}
		cmd := fmt.Sprintf("%ssystemctl %s mariadb || %ssystemctl %s mysql", sudoPrefix, op, sudoPrefix, op)
		out, err := runSingleCommand(client, cmd)
		if err != nil {
			sendToHQ("mariadb_op_res", hostID, termID, map[string]string{"status": "error", "msg": out})
		} else {
			sendToHQ("mariadb_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Service " + op})
			// Trigger refresh status automatico
			handleMariaDBCommand(hostID, termID, map[string]interface{}{"action": "check_status", "db_user": dbUser, "db_pass": dbPass})
		}

	case "list_dbs":
		// Query per DB e dimensioni in MB
		query := "SELECT table_schema, ROUND(SUM(data_length + index_length) / 1024 / 1024, 2) FROM information_schema.TABLES GROUP BY table_schema;"
		// -N: no headers, -B: batch (tab separated)
		cmd := fmt.Sprintf("%s -N -B -e \"%s\"", baseCmd, query)

		out, err := runSingleCommand(client, cmd)
		if err != nil {
			sendToHQ("mariadb_error", hostID, termID, "Error listing DBs (Check credentials): "+out)
			return
		}

		var dbs []map[string]interface{}
		lines := strings.Split(out, "\n")
		for _, line := range lines {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				dbs = append(dbs, map[string]interface{}{"name": parts[0], "size": parts[1]})
			} else if len(parts) == 1 && strings.TrimSpace(parts[0]) != "" {
				// DB vuoti o senza tabelle accessibili
				dbs = append(dbs, map[string]interface{}{"name": parts[0], "size": "0"})
			}
		}
		sendToHQ("mariadb_dbs", hostID, termID, dbs)

	case "list_users":
		query := "SELECT User, Host FROM mysql.user;"
		cmd := fmt.Sprintf("%s -N -B -e \"%s\"", baseCmd, query)

		out, err := runSingleCommand(client, cmd)
		if err != nil {
			sendToHQ("mariadb_error", hostID, termID, "Error listing Users: "+out)
			return
		}

		var users []map[string]string
		lines := strings.Split(out, "\n")
		for _, line := range lines {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				users = append(users, map[string]string{"user": parts[0], "host": parts[1]})
			}
		}
		sendToHQ("mariadb_users", hostID, termID, users)

	case "dump_db":
		dbName, _ := payload["name"].(string)
		if dbName == "" {
			return
		}

		sendToHQ("mariadb_dump_started", hostID, termID, map[string]string{"msg": "Dump started for " + dbName})

		// Esegui in background (goroutine) per non bloccare
		go func() {
			filename := fmt.Sprintf("/tmp/%s_%d.sql.gz", dbName, time.Now().Unix())
			// mysqldump | gzip
			cmd := fmt.Sprintf("%s %s | gzip > %s", baseDump, dbName, filename)

			// Nota: usiamo lo stesso client SSH. Assumiamo che il client supporti la concorrenza (standard in Go ssh)
			out, err := runSingleCommand(client, cmd)

			if err != nil {
				sendToHQ("mariadb_dump_res", hostID, termID, map[string]string{
					"status": "error",
					"msg":    "Dump failed: " + out,
					"db":     dbName,
				})
			} else {
				sendToHQ("mariadb_dump_res", hostID, termID, map[string]string{
					"status": "success",
					"msg":    "Dump saved to " + filename,
					"file":   filename,
					"db":     dbName,
				})
			}
		}()

	case "list_configs":
		confType, _ := payload["type"].(string)
		// Use sh -c to allow shell expansion and handle multiple paths robustly
		paths := "/etc/mysql /etc/my.cnf /etc/mysql*"
		if confType == "mariadb" {
			paths = "/etc/mysql /etc/mariadb /etc/my.cnf.d /etc/my.cnf /etc/mysql*"
		}

		// Remove 2>/dev/null to capture errors in output for debugging, wrap in sh -c
		cmd := fmt.Sprintf("%ssh -c \"find %s -maxdepth 3 -type f -name '*.cnf'\"", sudoPrefix, paths)
		out, _ := runSingleCommand(client, cmd)

		var files []string
		lines := strings.Split(out, "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			// Filter out find errors and ensure absolute path
			if line != "" && strings.HasPrefix(line, "/") && !strings.Contains(line, "No such file") && !strings.Contains(line, "Permission denied") {
				files = append(files, line)
			}
		}

		if len(files) == 0 && (strings.Contains(strings.ToLower(out), "password") || strings.Contains(strings.ToLower(out), "sudo:")) {
			sendToHQ("mariadb_error", hostID, termID, "Sudo Error: Check session password. Output: "+out)
			return
		}

		sendToHQ("mariadb_config_list", hostID, termID, files)

	case "read_config":
		file, _ := payload["file"].(string)
		// Simple security check to ensure we are reading config files
		if !strings.HasPrefix(file, "/etc/") {
			return
		}
		cmd := fmt.Sprintf("%scat \"%s\"", sudoPrefix, file)
		out, _ := runSingleCommand(client, cmd)
		sendToHQ("mariadb_config_content", hostID, termID, map[string]string{"file": file, "content": out})

	case "save_config":
		file, _ := payload["file"].(string)
		content, _ := payload["content"].(string)
		if !strings.HasPrefix(file, "/etc/") {
			return
		}
		b64 := base64.StdEncoding.EncodeToString([]byte(content))
		tmpFile := fmt.Sprintf("/tmp/mdb_conf_%d", time.Now().UnixNano())

		runSingleCommand(client, fmt.Sprintf("echo '%s' | base64 -d > %s", b64, tmpFile))
		runSingleCommand(client, fmt.Sprintf("%smv %s \"%s\"", sudoPrefix, tmpFile, file))

		sendToHQ("mariadb_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Config saved"})
	}
}

package bridge

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

// Struttura per inviare i dati utente al frontend in modo pulito
type SystemUser struct {
	Username string `json:"username"`
	UID      string `json:"uid"`
	GID      string `json:"gid"` // Usato come "Gruppo principale" per visualizzazione
	Home     string `json:"home"`
	Shell    string `json:"shell"`
	Sudo     bool   `json:"sudo"`
}

// Struttura per i gruppi
type SystemGroup struct {
	Name string `json:"name"`
	GID  string `json:"gid"`
}

// Struttura per i dati temporali
type TimeInfo struct {
	LocalTime string `json:"local_time"`
	Date      string `json:"date"`
	Timezone  string `json:"timezone"`
	NTPActive bool   `json:"ntp_active"`
}

func handleAdminCommand(hostID int, termID string, payload map[string]interface{}) {
	// I comandi admin non sono interattivi, usano la sessione "main"
	sess, err := ensureSession(hostID, GetSessionIDWithPrefix(termID, "main"))
	if err != nil {
		sendToHQ("tool_output", hostID, termID, "Connection Error: "+err.Error())
		return
	}

	action, _ := payload["action"].(string)
	// La password per sudo è ora nel wrapper della sessione "main"
	cmdPrefix := fmt.Sprintf("echo '%s' | sudo -S -p '' ", sess.Password)

	// Il client SSH è ora nel wrapper della sessione
	client := sess.Client

	switch action {

	// --- USER MANAGEMENT ---

	case "list_users":
		outUsers, _ := runSingleCommand(client, "getent passwd")
		outGroups, _ := runSingleCommand(client, "getent group sudo wheel")

		adminUsers := make(map[string]bool)
		linesGrp := strings.Split(outGroups, "\n")
		for _, line := range linesGrp {
			parts := strings.Split(line, ":")
			if len(parts) >= 4 && parts[3] != "" {
				members := strings.Split(parts[3], ",")
				for _, m := range members {
					adminUsers[m] = true
				}
			}
		}

		var usersList []SystemUser
		lines := strings.Split(outUsers, "\n")
		for _, line := range lines {
			if line == "" {
				continue
			}
			fields := strings.Split(line, ":")
			if len(fields) >= 7 {
				u := SystemUser{
					Username: fields[0],
					UID:      fields[2],
					GID:      fields[3],
					Home:     fields[5],
					Shell:    fields[6],
					Sudo:     adminUsers[fields[0]] || fields[2] == "0",
				}
				usersList = append(usersList, u)
			}
		}
		sendToHQ("admin_user_list", hostID, termID, usersList)

	case "create_user":
		user, _ := payload["username"].(string)
		pass, _ := payload["password"].(string)
		shell, _ := payload["shell"].(string)
		sudo, _ := payload["sudo"].(bool)

		if user == "" || pass == "" {
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Dati mancanti"})
			return
		}

		cmd := fmt.Sprintf("%suseradd -m -s %s %s", cmdPrefix, shell, user)
		out, err := runSingleCommand(client, cmd)
		if err != nil {
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Errore creazione: " + out})
			return
		}

		cmdPass := fmt.Sprintf("echo '%s:%s' | %schpasswd", user, pass, cmdPrefix)
		runSingleCommand(client, cmdPass)

		if sudo {
			runSingleCommand(client, fmt.Sprintf("%susermod -aG sudo %s", cmdPrefix, user))
			runSingleCommand(client, fmt.Sprintf("%susermod -aG wheel %s", cmdPrefix, user))
		}

		sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Utente creato"})

	case "delete_user":
		user, _ := payload["username"].(string)
		cmd := fmt.Sprintf("%suserdel -r %s", cmdPrefix, user)
		out, err := runSingleCommand(client, cmd)
		if err != nil {
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "error", "msg": out})
		} else {
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Utente eliminato"})
		}

	case "change_pass":
		user, _ := payload["username"].(string)
		pass, _ := payload["password"].(string)
		cmdPass := fmt.Sprintf("echo '%s:%s' | %schpasswd", user, pass, cmdPrefix)
		_, err := runSingleCommand(client, cmdPass)
		if err != nil {
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Errore cambio password"})
		} else {
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Password cambiata"})
		}

	// --- TIME & DATE ---

	case "get_time_info":
		out, _ := runSingleCommand(client, "timedatectl")
		info := TimeInfo{
			LocalTime: "Unknown", Date: "Unknown", Timezone: "Unknown", NTPActive: false,
		}
		lines := strings.Split(out, "\n")
		for _, l := range lines {
			l = strings.TrimSpace(l)
			if strings.HasPrefix(l, "Local time:") {
				parts := strings.Fields(l)
				if len(parts) >= 5 {
					info.Date = parts[3]
					info.LocalTime = parts[4]
				}
			}
			if strings.HasPrefix(l, "Time zone:") {
				parts := strings.Fields(l)
				if len(parts) >= 3 {
					info.Timezone = parts[2]
				}
			}
			if strings.HasPrefix(l, "NTP service:") || strings.HasPrefix(l, "System clock synchronized:") {
				if strings.Contains(l, "active") || strings.Contains(l, "yes") {
					info.NTPActive = true
				}
			}
		}
		sendToHQ("admin_time_info", hostID, termID, info)

	case "set_time":
		dt, _ := payload["datetime"].(string)
		// Disable NTP first to allow manual set
		runSingleCommand(client, fmt.Sprintf("%stimedatectl set-ntp false", cmdPrefix))
		cmd := fmt.Sprintf("%stimedatectl set-time '%s'", cmdPrefix, dt)
		out, err := runSingleCommand(client, cmd)
		if err != nil {
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Errore set time: " + out})
		} else {
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Orario aggiornato"})
		}

	case "set_timezone":
		tz, _ := payload["timezone"].(string)
		cmd := fmt.Sprintf("%stimedatectl set-timezone %s", cmdPrefix, tz)
		_, err := runSingleCommand(client, cmd)
		if err != nil {
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Errore set timezone"})
		} else {
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Timezone aggiornata"})
		}

	case "set_ntp":
		state, _ := payload["state"].(bool)
		stateStr := "false"
		if state {
			stateStr = "true"
		}
		cmd := fmt.Sprintf("%stimedatectl set-ntp %s", cmdPrefix, stateStr)
		_, err := runSingleCommand(client, cmd)
		if err != nil {
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Errore set NTP"})
		} else {
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Stato NTP aggiornato"})
		}

	// --- SYSCTL ---

	case "sysctl_dump":
		out, _ := runSingleCommand(client, "sysctl -a 2>/dev/null")
		sendToHQ("admin_sysctl_dump", hostID, termID, out)

	case "sysctl_set":
		key, _ := payload["key"].(string)
		val, _ := payload["value"].(string)
		safeVal := strings.ReplaceAll(val, "'", "'\\''")
		cmd := fmt.Sprintf("%ssysctl -w %s='%s'", cmdPrefix, key, safeVal)
		out, err := runSingleCommand(client, cmd)
		if err != nil {
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "error", "msg": out})
		} else {
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Parametro applicato"})
		}

	case "get_sysctl_conf":
		out, _ := runSingleCommand(client, "cat /etc/sysctl.conf")
		sendToHQ("sysctl_conf_content", hostID, termID, out)

	case "save_sysctl_conf":
		content, _ := payload["content"].(string)
		b64 := base64.StdEncoding.EncodeToString([]byte(content))
		runSingleCommand(client, fmt.Sprintf("echo '%s' | base64 -d | %stee /etc/sysctl.conf", b64, cmdPrefix))
		out, err := runSingleCommand(client, fmt.Sprintf("%ssysctl -p", cmdPrefix))
		if err != nil {
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Saved but reload failed: " + out})
		} else {
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Configuration imported and reloaded."})
		}

	// --- HOSTNAME & HOSTS FILE ---

	case "get_hosts_info":
		hn, _ := runSingleCommand(client, "hostname")
		hf, _ := runSingleCommand(client, "cat /etc/hosts")
		sendToHQ("admin_hosts_info", hostID, termID, map[string]string{
			"hostname":   strings.TrimSpace(hn),
			"hosts_file": hf,
		})

	case "set_hostname":
		hostname, _ := payload["hostname"].(string)
		if hostname != "" {
			runSingleCommand(client, fmt.Sprintf("%shostnamectl set-hostname %s", cmdPrefix, hostname))
			runSingleCommand(client, fmt.Sprintf("%shostname %s", cmdPrefix, hostname))
			runSingleCommand(client, fmt.Sprintf("%ssh -c 'echo \"%s\" > /etc/hostname'", cmdPrefix, hostname))
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Hostname aggiornato"})
		}

	case "set_hosts_file":
		content, _ := payload["content"].(string)
		b64 := base64.StdEncoding.EncodeToString([]byte(content))
		runSingleCommand(client, fmt.Sprintf("%ssh -c 'echo \"%s\" | base64 -d > /etc/hosts'", cmdPrefix, b64))
		sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "success", "msg": "/etc/hosts salvato"})

	// --- GROUPS MANAGEMENT ---

	case "list_groups":
		out, _ := runSingleCommand(client, "getent group")
		var groups []SystemGroup
		lines := strings.Split(out, "\n")
		for _, line := range lines {
			parts := strings.Split(line, ":")
			if len(parts) >= 3 {
				groups = append(groups, SystemGroup{Name: parts[0], GID: parts[2]})
			}
		}
		sendToHQ("admin_group_list", hostID, termID, groups)

	case "create_group":
		name, _ := payload["name"].(string)
		if name != "" {
			out, err := runSingleCommand(client, fmt.Sprintf("%sgroupadd %s", cmdPrefix, name))
			if err != nil {
				sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "error", "msg": out})
			} else {
				sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Gruppo creato"})
			}
		}

	case "delete_group":
		name, _ := payload["name"].(string)
		if name != "" {
			out, err := runSingleCommand(client, fmt.Sprintf("%sgroupdel %s", cmdPrefix, name))
			if err != nil {
				sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "error", "msg": out})
			} else {
				sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Gruppo eliminato"})
			}
		}

	// --- USER DETAILS & SSH ---

	case "get_user_details":
		user, _ := payload["username"].(string)
		// Gruppi
		outGroups, _ := runSingleCommand(client, fmt.Sprintf("id -Gn %s", user))
		// Home dir e SSH Keys
		outPasswd, _ := runSingleCommand(client, fmt.Sprintf("getent passwd %s", user))
		parts := strings.Split(strings.TrimSpace(outPasswd), ":")
		homeDir := ""
		if len(parts) >= 6 {
			homeDir = parts[5]
		}
		sendToHQ("admin_user_details", hostID, termID, map[string]interface{}{
			"username": user,
			"groups":   strings.Fields(outGroups),
			"home":     homeDir,
		})

	case "update_user_groups":
		user, _ := payload["username"].(string)
		groups, _ := payload["groups"].(string)
		cmd := fmt.Sprintf("%susermod -G %s %s", cmdPrefix, groups, user)
		out, err := runSingleCommand(client, cmd)
		if err != nil {
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "error", "msg": out})
		} else {
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Gruppi aggiornati"})
		}

	case "get_user_ssh_info":
		user, _ := payload["username"].(string)
		outInfo, _ := runSingleCommand(client, fmt.Sprintf("getent passwd %s", user))
		parts := strings.Split(strings.TrimSpace(outInfo), ":")
		homeDir := ""
		if len(parts) >= 6 {
			homeDir = parts[5]
		}

		authKeys, _ := runSingleCommand(client, fmt.Sprintf("%scat %s/.ssh/authorized_keys 2>/dev/null", cmdPrefix, homeDir))
		listFiles, _ := runSingleCommand(client, fmt.Sprintf("%sls -1 %s/.ssh/ 2>/dev/null", cmdPrefix, homeDir))

		sendToHQ("admin_user_ssh_info", hostID, termID, map[string]interface{}{
			"username":        user,
			"authorized_keys": authKeys,
			"files":           strings.Split(strings.TrimSpace(listFiles), "\n"),
		})

	case "save_authorized_keys":
		user, _ := payload["username"].(string)
		content, _ := payload["keys"].(string)
		outInfo, _ := runSingleCommand(client, fmt.Sprintf("getent passwd %s", user))
		parts := strings.Split(strings.TrimSpace(outInfo), ":")
		homeDir := ""
		gid := ""
		if len(parts) >= 6 {
			gid = parts[3]
			homeDir = parts[5]
		}
		b64 := base64.StdEncoding.EncodeToString([]byte(content))
		runSingleCommand(client, fmt.Sprintf("%smkdir -p %s/.ssh", cmdPrefix, homeDir))
		runSingleCommand(client, fmt.Sprintf("%schown %s:%s %s/.ssh", cmdPrefix, user, gid, homeDir))
		runSingleCommand(client, fmt.Sprintf("%schmod 700 %s/.ssh", cmdPrefix, homeDir))
		runSingleCommand(client, fmt.Sprintf("%ssh -c 'echo \"%s\" | base64 -d > %s/.ssh/authorized_keys'", cmdPrefix, b64, homeDir))
		runSingleCommand(client, fmt.Sprintf("%schown %s:%s %s/.ssh/authorized_keys", cmdPrefix, user, gid, homeDir))
		runSingleCommand(client, fmt.Sprintf("%schmod 600 %s/.ssh/authorized_keys", cmdPrefix, homeDir))
		sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Authorized Keys salvate"})

	case "generate_user_ssh_key":
		user, _ := payload["username"].(string)
		keyType, _ := payload["type"].(string)
		outInfo, _ := runSingleCommand(client, fmt.Sprintf("getent passwd %s", user))
		parts := strings.Split(strings.TrimSpace(outInfo), ":")
		homeDir := ""
		gid := ""
		if len(parts) >= 6 {
			gid = parts[3]
			homeDir = parts[5]
		}
		keyName := "id_" + keyType
		keyPath := fmt.Sprintf("%s/.ssh/%s", homeDir, keyName)
		runSingleCommand(client, fmt.Sprintf("%smkdir -p %s/.ssh", cmdPrefix, homeDir))
		runSingleCommand(client, fmt.Sprintf("%schown %s:%s %s/.ssh", cmdPrefix, user, gid, homeDir))
		runSingleCommand(client, fmt.Sprintf("%schmod 700 %s/.ssh", cmdPrefix, homeDir))
		cmd := fmt.Sprintf("%sssh-keygen -t %s -f %s -N '' -q", cmdPrefix, keyType, keyPath)
		out, err := runSingleCommand(client, cmd)
		runSingleCommand(client, fmt.Sprintf("%schown %s:%s %s %s.pub", cmdPrefix, user, gid, keyPath, keyPath))
		if err != nil {
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Errore generazione: " + out})
		} else {
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Chiave generata: " + keyName})
		}

	case "read_user_key":
		user, _ := payload["username"].(string)
		file, _ := payload["file"].(string)
		outInfo, _ := runSingleCommand(client, fmt.Sprintf("getent passwd %s", user))
		parts := strings.Split(strings.TrimSpace(outInfo), ":")
		homeDir := ""
		if len(parts) >= 6 {
			homeDir = parts[5]
		}
		if strings.Contains(file, "/") || strings.Contains(file, "..") {
			return
		}
		content, _ := runSingleCommand(client, fmt.Sprintf("%scat %s/.ssh/%s", cmdPrefix, homeDir, file))
		sendToHQ("admin_ssh_key_content", hostID, termID, map[string]string{"file": file, "content": content})

	// --- SUDOERS ---
	case "read_sudoers":
		out, _ := runSingleCommand(client, fmt.Sprintf("%scat /etc/sudoers", cmdPrefix))
		sendToHQ("sudoers_content", hostID, termID, out)

	case "save_sudoers":
		content, _ := payload["content"].(string)
		b64 := base64.StdEncoding.EncodeToString([]byte(content))

		// Validate with visudo
		tmpFile := fmt.Sprintf("/tmp/sudoers_check_%d", time.Now().UnixNano())
		runSingleCommand(client, fmt.Sprintf("echo '%s' | base64 -d > %s", b64, tmpFile))

		checkOut, checkErr := runSingleCommand(client, fmt.Sprintf("%svisudo -c -f %s", cmdPrefix, tmpFile))
		if checkErr != nil {
			runSingleCommand(client, "rm "+tmpFile)
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Syntax Error: " + checkOut})
			return
		}

		runSingleCommand(client, fmt.Sprintf("%scp /etc/sudoers /etc/sudoers.bak.$(date +%%s)", cmdPrefix))
		runSingleCommand(client, fmt.Sprintf("%scat %s | %stee /etc/sudoers > /dev/null", cmdPrefix, tmpFile, cmdPrefix))
		runSingleCommand(client, "rm "+tmpFile)
		sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Sudoers salvato"})

	// --- LANGUAGE & LOCALE ---

	case "get_locale_info":
		status, _ := runSingleCommand(client, "localectl status")
		locales, _ := runSingleCommand(client, "localectl list-locales")
		keymaps, _ := runSingleCommand(client, "localectl list-keymaps")
		sendToHQ("admin_locale_info", hostID, termID, map[string]string{
			"status":  status,
			"locales": locales,
			"keymaps": keymaps,
		})

	case "set_locale":
		l, _ := payload["locale"].(string)
		out, err := runSingleCommand(client, fmt.Sprintf("%slocalectl set-locale LANG=%s", cmdPrefix, l))
		if err != nil {
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Errore set locale: " + out})
		} else {
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Locale aggiornato"})
		}

	case "set_keymap":
		k, _ := payload["keymap"].(string)
		k = strings.TrimSpace(k)
		out, err := runSingleCommand(client, fmt.Sprintf("%slocalectl set-keymap %s 2>&1", cmdPrefix, k))
		if err != nil {
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Errore set keymap: " + out})
		} else {
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Keymap aggiornata"})
		}

	case "gen_locale":
		l, _ := payload["locale"].(string)
		l = strings.TrimSpace(l)

		// Check for locale-gen (Debian/Ubuntu)
		checkDeb, _ := runSingleCommand(client, "which locale-gen")
		var out string
		var err error

		if strings.TrimSpace(checkDeb) != "" {
			out, err = runSingleCommand(client, fmt.Sprintf("%slocale-gen %s", cmdPrefix, l))
		} else {
			// RHEL/Fedora fallback using localedef
			// Format expected: it_IT.UTF-8 -> localedef -i it_IT -f UTF-8 it_IT.UTF-8
			parts := strings.Split(l, ".")
			if len(parts) == 2 {
				lang := parts[0]
				charset := parts[1]
				cmd := fmt.Sprintf("%slocaledef -c -i %s -f %s %s", cmdPrefix, lang, charset, l)
				out, err = runSingleCommand(client, cmd)
			}
		}

		if err != nil {
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Errore generazione locale: " + out})
		} else {
			runSingleCommand(client, fmt.Sprintf("%slocalectl set-locale LANG=%s", cmdPrefix, l))
			if strings.TrimSpace(checkDeb) != "" {
				runSingleCommand(client, fmt.Sprintf("%supdate-locale LANG=%s", cmdPrefix, l))
			}
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Locale generato e impostato"})
		}

	// --- KERNEL & BOOT ---

	case "get_kernel_info":
		uname, _ := runSingleCommand(client, "uname -r")

		// Enhanced lsmod: Name | Size | UsedBy | Description
		lsmodScript := "lsmod | tail -n +2 | while read m s u; do d=$(/sbin/modinfo -F description $m 2>/dev/null); echo \"$m|$s|$u|$d\"; done"
		lsmod, _ := runSingleCommand(client, lsmodScript)

		grub, _ := runSingleCommand(client, "cat /etc/default/grub")

		// Try to get GRUB menu entries
		grubEntries, _ := runSingleCommand(client, "grep -E \"^menuentry '\" /boot/grub/grub.cfg 2>/dev/null | cut -d \"'\" -f2 || grep -E \"^menuentry '\" /boot/grub2/grub.cfg 2>/dev/null | cut -d \"'\" -f2")

		// Get Blacklisted modules
		blacklist, _ := runSingleCommand(client, "grep -r \"^blacklist\" /etc/modprobe.d/ 2>/dev/null | awk '{print $2}'")

		sendToHQ("admin_kernel_info", hostID, termID, map[string]string{
			"current":      strings.TrimSpace(uname),
			"lsmod":        lsmod,
			"grub_config":  grub,
			"grub_entries": grubEntries,
			"blacklist":    blacklist,
		})

	case "list_all_modules":
		// Trova tutti i moduli disponibili, rimuove estensione e path, normalizza - in _
		// find /lib/modules/$(uname -r) ...
		cmd := "find /lib/modules/$(uname -r) -type f -name '*.ko*' -printf '%f\\n' | sed 's/\\.ko.*//' | tr '-' '_' | sort | uniq"
		out, _ := runSingleCommand(client, cmd)
		sendToHQ("admin_all_modules", hostID, termID, out)

	case "save_grub":
		content, _ := payload["content"].(string)
		b64 := base64.StdEncoding.EncodeToString([]byte(content))
		runSingleCommand(client, fmt.Sprintf("echo '%s' | base64 -d | %stee /etc/default/grub", b64, cmdPrefix))
		out, err := runSingleCommand(client, fmt.Sprintf("%supdate-grub || %sgrub2-mkconfig -o /boot/grub2/grub.cfg", cmdPrefix, cmdPrefix))
		if err != nil {
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Errore update-grub: " + out})
		} else {
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "success", "msg": "GRUB aggiornato"})
		}

	case "module_op":
		op, _ := payload["op"].(string)
		mod, _ := payload["module"].(string)
		var cmd string
		if op == "load" {
			cmd = fmt.Sprintf("%smodprobe %s", cmdPrefix, mod)
		} else if op == "unload" {
			cmd = fmt.Sprintf("%smodprobe -r %s", cmdPrefix, mod)
		} else if op == "blacklist" {
			cmd = fmt.Sprintf("%ssh -c 'echo \"blacklist %s\" >> /etc/modprobe.d/blacklist-shelldeck.conf'", cmdPrefix, mod)
		}
		out, err := runSingleCommand(client, cmd)
		if err != nil {
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Errore modulo: " + out})
		} else {
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Operazione modulo eseguita"})
		}

	case "list_packages":
		filter, _ := payload["filter"].(string)
		out, _ := runSingleCommand(client, fmt.Sprintf("dpkg -l | grep '%s' | awk '{print $2 \" \" $3}'", filter))
		if out == "" {
			out, _ = runSingleCommand(client, fmt.Sprintf("rpm -qa | grep '%s'", filter))
		}
		sendToHQ("admin_pkg_list", hostID, termID, out)

	case "remove_package":
		pkg, _ := payload["package"].(string)
		out, err := runSingleCommand(client, fmt.Sprintf("%sapt-get remove -y %s || %sdnf remove -y %s", cmdPrefix, pkg, cmdPrefix, pkg))
		if err != nil {
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Errore rimozione: " + out})
		} else {
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Pacchetto rimosso"})
		}

	case "get_old_kernels":
		current, _ := runSingleCommand(client, "uname -r")
		current = strings.TrimSpace(current)

		// Detect package manager
		// Use test -f for more reliable detection than which
		isDebian, _ := runSingleCommand(client, "[ -f /etc/debian_version ] && echo yes")
		isRpm, _ := runSingleCommand(client, "[ -f /etc/redhat-release ] || [ -f /etc/fedora-release ] && echo yes")

		var out string
		if strings.TrimSpace(isDebian) == "yes" {
			// Debian/Ubuntu
			out, _ = runSingleCommand(client, "dpkg --list | grep 'linux-image-[0-9]' | awk '/^ii/{ print $2 }'")
		} else if strings.TrimSpace(isRpm) == "yes" {
			// RHEL/Fedora
			out, _ = runSingleCommand(client, "rpm -q kernel")
		}

		sendToHQ("admin_old_kernels", hostID, termID, map[string]interface{}{
			"current":   strings.TrimSpace(current),
			"installed": strings.Fields(out),
		})

	case "remove_kernels":
		pkgsRaw, _ := payload["packages"].([]interface{})
		if len(pkgsRaw) == 0 {
			return
		}
		var pkgs []string
		for _, p := range pkgsRaw {
			pkgs = append(pkgs, p.(string))
		}
		pkgStr := strings.Join(pkgs, " ")
		// Use sh -c to handle env vars with sudo
		isDebian, _ := runSingleCommand(client, "[ -f /etc/debian_version ] && echo yes")
		isRpm, _ := runSingleCommand(client, "[ -f /etc/redhat-release ] || [ -f /etc/fedora-release ] && echo yes")

		var cmd string
		if strings.TrimSpace(isDebian) == "yes" {
			cmd = fmt.Sprintf("%s sh -c 'DEBIAN_FRONTEND=noninteractive apt-get purge -y %s'", cmdPrefix, pkgStr)
		} else if strings.TrimSpace(isRpm) == "yes" {
			hasDnf, _ := runSingleCommand(client, "command -v dnf")
			if strings.TrimSpace(hasDnf) != "" {
				cmd = fmt.Sprintf("%sdnf remove -y %s", cmdPrefix, pkgStr)
			} else {
				cmd = fmt.Sprintf("%syum remove -y %s", cmdPrefix, pkgStr)
			}
		} else {
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Package manager not found"})
			return
		}

		out, err := runSingleCommand(client, cmd)
		if err != nil {
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Errore rimozione: " + out})
		} else {
			// Update grub if needed (Debian usually needs it, RHEL does it automatically or via grub2-mkconfig)
			runSingleCommand(client, fmt.Sprintf("%supdate-grub || %sgrub2-mkconfig -o /boot/grub2/grub.cfg", cmdPrefix, cmdPrefix))
			sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Kernel rimossi. Update-grub eseguito."})
		}

	// --- ENVIRONMENT ---

	case "get_env":
		out, _ := runSingleCommand(client, "cat /etc/environment")
		sendToHQ("admin_env_content", hostID, termID, out)

	case "save_env":
		content, _ := payload["content"].(string)
		b64 := base64.StdEncoding.EncodeToString([]byte(content))
		runSingleCommand(client, fmt.Sprintf("echo '%s' | base64 -d | %stee /etc/environment", b64, cmdPrefix))
		sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Environment salvato"})

	// --- PASSWD FILE ---

	case "get_passwd_info":
		content, _ := runSingleCommand(client, "cat /etc/passwd")
		mtime, _ := runSingleCommand(client, "stat -c %y /etc/passwd")
		sendToHQ("admin_passwd_info", hostID, termID, map[string]string{
			"content": content,
			"mtime":   strings.TrimSpace(mtime),
		})

	case "save_passwd":
		content, _ := payload["content"].(string)
		b64 := base64.StdEncoding.EncodeToString([]byte(content))
		// Backup
		runSingleCommand(client, fmt.Sprintf("%scp /etc/passwd /etc/passwd.bak.$(date +%%s)", cmdPrefix))
		tmpFile := fmt.Sprintf("/tmp/passwd_save_%d", time.Now().UnixNano())
		runSingleCommand(client, fmt.Sprintf("echo '%s' | base64 -d > %s", b64, tmpFile))
		runSingleCommand(client, fmt.Sprintf("%smv %s /etc/passwd && %schmod 644 /etc/passwd && %schown root:root /etc/passwd", cmdPrefix, tmpFile, cmdPrefix, cmdPrefix))
		sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "success", "msg": "/etc/passwd salvato"})

	// --- DNS & CACHE ---

	case "get_dns_services_status":
		// Check status of various DNS related services
		services := []string{"systemd-resolved", "dnsmasq", "named", "bind9", "nscd"}
		statusMap := make(map[string]bool)

		for _, s := range services {
			out, _ := runSingleCommand(client, fmt.Sprintf("systemctl is-active %s", s))
			statusMap[s] = strings.TrimSpace(out) == "active"
		}

		// Normalize named/bind9
		if statusMap["bind9"] {
			statusMap["named"] = true
		}

		sendToHQ("dns_services_status", hostID, termID, statusMap)

	case "dns_op":
		op, _ := payload["op"].(string)
		var cmd string
		switch op {
		case "flush_systemd":
			cmd = fmt.Sprintf("%sresolvectl flush-caches 2>/dev/null || %ssystemd-resolve --flush-caches", cmdPrefix, cmdPrefix)
		case "restart_dnsmasq":
			cmd = fmt.Sprintf("%ssystemctl restart dnsmasq", cmdPrefix)
		case "reload_dnsmasq":
			cmd = fmt.Sprintf("%skillall -HUP dnsmasq", cmdPrefix)
		case "restart_named":
			cmd = fmt.Sprintf("%ssystemctl restart named 2>/dev/null || %ssystemctl restart bind9", cmdPrefix, cmdPrefix)
		case "restart_nscd":
			cmd = fmt.Sprintf("%ssystemctl restart nscd", cmdPrefix)
		}

		if cmd != "" {
			out, err := runSingleCommand(client, cmd)
			if err != nil {
				sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Errore: " + out + " " + err.Error()})
			} else {
				sendToHQ("admin_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Comando eseguito"})
			}
		}
	}
}

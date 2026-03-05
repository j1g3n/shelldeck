package bridge

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

func handleNetworkCommand(hostID int, termID string, payload map[string]interface{}) {
	sess, err := ensureSession(hostID, GetSessionIDWithPrefix(termID, "main"))
	if err != nil {
		sendToHQ("tool_output", hostID, termID, "Connection Error: "+err.Error())
		return
	}
	action, _ := payload["action"].(string)
	cmdPrefix := fmt.Sprintf("echo '%s' | sudo -S -p '' ", sess.Password)
	client := sess.Client
	wsID := GetWorkspaceID(termID)

	switch action {
	case "get_interfaces":
		out, _ := runSingleCommand(client, "ip -j -d addr show")
		var rawData interface{}
		json.Unmarshal([]byte(out), &rawData)
		sendToHQ("net_ifaces", hostID, termID, rawData)

	case "get_routes":
		out4, _ := runSingleCommand(client, "ip -j -4 route show")
		out6, _ := runSingleCommand(client, "ip -j -6 route show")
		var r4, r6 []interface{}
		json.Unmarshal([]byte(out4), &r4)
		json.Unmarshal([]byte(out6), &r6)
		combined := append(r4, r6...)
		sendToHQ("net_routes", hostID, termID, combined)

	case "get_dns":
		// Try resolvectl (systemd), then nmcli (NetworkManager - Fedora/RHEL), then resolv.conf
		out, _ := runSingleCommand(client, "resolvectl status --no-pager || nmcli dev show || cat /etc/resolv.conf")
		sendToHQ("net_dns_info", hostID, termID, out)

	case "get_hostname":
		out, _ := runSingleCommand(client, "hostname")
		sendToHQ("hostname", hostID, termID, strings.TrimSpace(out))

	case "set_hostname":
		hostname, _ := payload["hostname"].(string)
		if hostname != "" {
			runSingleCommand(client, fmt.Sprintf("%shostnamectl set-hostname %s", cmdPrefix, hostname))
			runSingleCommand(client, fmt.Sprintf("echo '%s' | %stee /etc/hostname", hostname, cmdPrefix))
			runSingleCommand(client, fmt.Sprintf("%shostname %s", cmdPrefix, hostname))
			out, _ := runSingleCommand(client, "hostname")
			sendToHQ("hostname", hostID, termID, strings.TrimSpace(out))
		}

	case "mon_get_ss":
		out, _ := runSingleCommand(client, cmdPrefix+"ss -tunap")
		sendToHQ("mon_ss", hostID, termID, out)

	case "mon_get_routes":
		out, _ := runSingleCommand(client, "ip route show")
		sendToHQ("mon_routes", hostID, termID, out)

	case "mon_get_netdev":
		out, _ := runSingleCommand(client, "cat /proc/net/dev")
		sendToHQ("mon_netdev", hostID, termID, out)

	case "mon_get_iplink":
		out, _ := runSingleCommand(client, "ip -s link")
		sendToHQ("mon_iplink", hostID, termID, out)

	case "get_iface_stats":
		iface, _ := payload["iface"].(string)
		// Legge rx_bytes e tx_bytes da sysfs per monitoraggio live modale
		rx, _ := runSingleCommand(client, fmt.Sprintf("cat /sys/class/net/%s/statistics/rx_bytes", iface))
		tx, _ := runSingleCommand(client, fmt.Sprintf("cat /sys/class/net/%s/statistics/tx_bytes", iface))
		sendToHQ("iface_stats", hostID, termID, map[string]string{
			"iface": iface,
			"rx":    strings.TrimSpace(rx),
			"tx":    strings.TrimSpace(tx),
		})

	case "mon_stream_start_ip":
		go startStream(hostID, wsID, "ipmonitor", "ip monitor all", "stream_ipmonitor", client)

	case "mon_stream_start_tcpdump":
		filter, _ := payload["filter"].(string)
		cmd := fmt.Sprintf("%stcpdump -l -n %s", cmdPrefix, filter)
		go startStream(hostID, wsID, "tcpdump", cmd, "stream_tcpdump", client)

	case "mon_stream_stop_tcpdump":
		stopStream(hostID, wsID, "tcpdump")

	case "if_up":
		iface, _ := payload["iface"].(string)
		runSingleCommand(client, fmt.Sprintf("%sip link set %s up", cmdPrefix, iface))
		sendToHQ("net_op_res", hostID, termID, map[string]string{"status": "success"})

	case "if_down":
		iface, _ := payload["iface"].(string)
		runSingleCommand(client, fmt.Sprintf("%sip link set %s down", cmdPrefix, iface))
		sendToHQ("net_op_res", hostID, termID, map[string]string{"status": "success"})

	case "delete_iface":
		iface, _ := payload["iface"].(string)
		runSingleCommand(client, fmt.Sprintf("%sip link delete %s", cmdPrefix, iface))
		sendToHQ("net_op_res", hostID, termID, map[string]string{"status": "success"})

	case "configure_iface":
		iface, _ := payload["iface"].(string)
		ip, _ := payload["ip"].(string)
		ip6, _ := payload["ip6"].(string)
		gw, _ := payload["gateway"].(string)
		dns, _ := payload["dns"].(string)

		var errs []string
		if ip != "" {
			if out, err := runSingleCommand(client, fmt.Sprintf("%sip addr add %s dev %s", cmdPrefix, ip, iface)); err != nil {
				// Ignora errore se l'IP esiste già (per permettere di cambiare solo GW senza errori)
				if !strings.Contains(strings.ToLower(out), "file exists") {
					errs = append(errs, fmt.Sprintf("IP Add: %s", out))
				}
			}
		}
		if ip6 != "" {
			if out, err := runSingleCommand(client, fmt.Sprintf("%sip -6 addr add %s dev %s", cmdPrefix, ip6, iface)); err != nil {
				if !strings.Contains(strings.ToLower(out), "file exists") {
					errs = append(errs, fmt.Sprintf("IPv6 Add: %s", out))
				}
			}
		}
		if gw != "" {
			if out, err := runSingleCommand(client, fmt.Sprintf("%sip route replace default via %s dev %s", cmdPrefix, gw, iface)); err != nil {
				errs = append(errs, fmt.Sprintf("GW Set: %s", out))
			}
		}
		if dns != "" {
			// Tenta di impostare i DNS via resolvectl (systemd)
			if out, err := runSingleCommand(client, fmt.Sprintf("%sresolvectl dns %s %s", cmdPrefix, iface, dns)); err != nil {
				errs = append(errs, fmt.Sprintf("DNS Set: %s", out))
			}
		}

		if len(errs) > 0 {
			sendToHQ("net_op_res", hostID, termID, map[string]string{"status": "error", "msg": strings.Join(errs, "; ")})
		} else {
			sendToHQ("net_op_res", hostID, termID, map[string]string{"status": "success"})
		}

	case "create_bridge":
		name, _ := payload["name"].(string)
		out, err := runSingleCommand(client, fmt.Sprintf("%sip link add name %s type bridge", cmdPrefix, name))
		if err != nil || (out != "" && strings.Contains(strings.ToLower(out), "error")) {
			sendToHQ("net_op_res", hostID, termID, map[string]string{"status": "error", "msg": out})
		} else {
			runSingleCommand(client, fmt.Sprintf("%sip link set %s up", cmdPrefix, name))
			sendToHQ("net_op_res", hostID, termID, map[string]string{"status": "success"})
		}

	case "create_vlan":
		link, _ := payload["link"].(string)
		id, _ := payload["id"].(string)
		name := fmt.Sprintf("%s.%s", link, id)
		out, err := runSingleCommand(client, fmt.Sprintf("%sip link add link %s name %s type vlan id %s", cmdPrefix, link, name, id))
		if err != nil || (out != "" && strings.Contains(strings.ToLower(out), "error")) {
			sendToHQ("net_op_res", hostID, termID, map[string]string{"status": "error", "msg": out})
		} else {
			runSingleCommand(client, fmt.Sprintf("%sip link set %s up", cmdPrefix, name))
			sendToHQ("net_op_res", hostID, termID, map[string]string{"status": "success"})
		}

	case "get_netplan_files":
		out, _ := runSingleCommand(client, "ls /etc/netplan/*.yaml")
		files := strings.Fields(out)
		sendToHQ("netplan_files", hostID, termID, files)

	case "read_netplan":
		file, _ := payload["file"].(string)
		out, _ := runSingleCommand(client, cmdPrefix+"cat "+file)
		sendToHQ("netplan_content", hostID, termID, out)

	case "save_netplan":
		file, _ := payload["file"].(string)
		content, _ := payload["content"].(string)
		b64 := base64.StdEncoding.EncodeToString([]byte(content))

		// Robust save: write b64 to temp, decode to temp, move
		tmpB64 := fmt.Sprintf("/tmp/netplan_save_%d.b64", time.Now().UnixNano())
		tmpFile := fmt.Sprintf("/tmp/netplan_save_%d.yaml", time.Now().UnixNano())
		runSingleCommand(client, fmt.Sprintf("echo '%s' > %s", b64, tmpB64))
		runSingleCommand(client, fmt.Sprintf("base64 -d -i %s > %s", tmpB64, tmpFile))
		runSingleCommand(client, "rm -f "+tmpB64)
		runSingleCommand(client, fmt.Sprintf("%smv -f %s %s", cmdPrefix, tmpFile, file))

		res, err := runSingleCommand(client, cmdPrefix+"netplan apply")
		if err != nil {
			sendToHQ("net_op_res", hostID, termID, map[string]string{"status": "error", "msg": res})
		} else {
			sendToHQ("net_op_res", hostID, termID, map[string]string{"status": "success"})
		}

	case "read_hosts":
		out, _ := runSingleCommand(client, "cat /etc/hosts")
		sendToHQ("hosts_content", hostID, termID, out)

	case "save_hosts":
		content, _ := payload["content"].(string)
		b64 := base64.StdEncoding.EncodeToString([]byte(content))
		tmpB64 := fmt.Sprintf("/tmp/hosts_save_%d.b64", time.Now().UnixNano())
		tmpFile := fmt.Sprintf("/tmp/hosts_save_%d.tmp", time.Now().UnixNano())
		runSingleCommand(client, fmt.Sprintf("echo '%s' > %s", b64, tmpB64))
		runSingleCommand(client, fmt.Sprintf("base64 -d -i %s > %s", tmpB64, tmpFile))
		runSingleCommand(client, "rm -f "+tmpB64)
		runSingleCommand(client, fmt.Sprintf("%smv -f %s /etc/hosts", cmdPrefix, tmpFile))
		sendToHQ("net_op_res", hostID, termID, map[string]string{"status": "success"})

	case "exec_net_tool":
		tool, _ := payload["tool"].(string)
		args, _ := payload["args"].(string)
		iface, _ := payload["iface"].(string)
		validTools := map[string]bool{"ping": true, "traceroute": true, "dig": true, "nslookup": true, "curl": true, "whois": true, "mtr": true}
		if !validTools[tool] {
			sendToHQ("tool_output", hostID, termID, "Tool non consentito.")
			return
		}
		if tool == "ping" && !strings.Contains(args, "-c") {
			args = "-c 4 " + args
		}

		// Add interface binding if specified
		if iface != "" && iface != "default" {
			if tool == "ping" {
				args = "-I " + iface + " " + args
			} else if tool == "traceroute" {
				args = "-i " + iface + " " + args
			} else if tool == "curl" {
				args = "--interface " + iface + " " + args
			}
		}

		useSudo := ""
		if tool == "traceroute" || tool == "mtr" {
			useSudo = cmdPrefix
		}
		cmd := fmt.Sprintf("%s%s %s", useSudo, tool, args)
		out, err := runSingleCommand(client, cmd)
		if err != nil {
			out += fmt.Sprintf("\n[Error] %v", err)
		}
		sendToHQ("tool_output", hostID, termID, out)

	case "get_iptables":
		outFilter, _ := runSingleCommand(client, cmdPrefix+"iptables -L -n -v --line-numbers")
		outNat, _ := runSingleCommand(client, cmdPrefix+"iptables -t nat -L -n -v --line-numbers")
		sendToHQ("iptables_list", hostID, termID, "=== FILTER TABLE ===\n"+outFilter+"\n\n=== NAT TABLE ===\n"+outNat)

	case "add_iptables":
		table, _ := payload["table"].(string)
		if table == "" {
			table = "filter"
		}
		chain, _ := payload["chain"].(string)
		target, _ := payload["target"].(string)
		proto, _ := payload["proto"].(string)
		port, _ := payload["port"].(string)
		src, _ := payload["src"].(string)
		to, _ := payload["to"].(string)

		cmd := fmt.Sprintf("%siptables -t %s -A %s -j %s", cmdPrefix, table, chain, target)
		if proto != "all" {
			cmd += fmt.Sprintf(" -p %s", proto)
		}
		if port != "" {
			cmd += fmt.Sprintf(" --dport %s", port)
		}
		if src != "" {
			cmd += fmt.Sprintf(" -s %s", src)
		}
		if target == "SNAT" && to != "" {
			cmd += fmt.Sprintf(" --to-source %s", to)
		}
		if target == "DNAT" && to != "" {
			cmd += fmt.Sprintf(" --to-destination %s", to)
		}
		out, err := runSingleCommand(client, cmd)
		if err != nil {
			sendToHQ("net_op_res", hostID, termID, map[string]string{"status": "error", "msg": out})
		} else {
			sendToHQ("net_op_res", hostID, termID, map[string]string{"status": "success"})
		}

	case "del_iptables":
		table, _ := payload["table"].(string)
		if table == "" {
			table = "filter"
		}
		chain, _ := payload["chain"].(string)
		num, _ := payload["num"].(string)
		cmd := fmt.Sprintf("%siptables -t %s -D %s %s", cmdPrefix, table, chain, num)
		out, err := runSingleCommand(client, cmd)
		if err != nil {
			sendToHQ("net_op_res", hostID, termID, map[string]string{"status": "error", "msg": out})
		} else {
			sendToHQ("net_op_res", hostID, termID, map[string]string{"status": "success"})
		}

	case "iptables_export":
		out, _ := runSingleCommand(client, cmdPrefix+"iptables-save")
		sendToHQ("iptables_export_data", hostID, termID, out)

	case "iptables_import":
		content, _ := payload["content"].(string)
		b64 := base64.StdEncoding.EncodeToString([]byte(content))
		tmpFile := fmt.Sprintf("/tmp/ipt_restore_%d", time.Now().UnixNano())
		runSingleCommand(client, fmt.Sprintf("echo '%s' | base64 -d > %s", b64, tmpFile))
		out, err := runSingleCommand(client, fmt.Sprintf("%siptables-restore < %s", cmdPrefix, tmpFile))
		runSingleCommand(client, "rm "+tmpFile)
		if err != nil {
			sendToHQ("net_op_res", hostID, termID, map[string]string{"status": "error", "msg": out})
		} else {
			sendToHQ("net_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Rules restored."})
		}

	case "iptables_save_file":
		path, _ := payload["path"].(string)
		if path == "" {
			path = "/etc/iptables/rules.v4"
		}
		dir := filepath.Dir(path)
		runSingleCommand(client, fmt.Sprintf("%smkdir -p %s", cmdPrefix, dir))
		out, err := runSingleCommand(client, fmt.Sprintf("%siptables-save | %stee %s", cmdPrefix, cmdPrefix, path))
		if err != nil {
			sendToHQ("net_op_res", hostID, termID, map[string]string{"status": "error", "msg": out})
		} else {
			sendToHQ("net_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Saved to " + path})
		}

	// --- ETC NETWORK EDITOR ---
	case "list_etc_network":
		out, _ := runSingleCommand(client, fmt.Sprintf("%sfind /etc/network -maxdepth 2", cmdPrefix))
		// Aggiungi anche /etc/networks se esiste
		if _, err := runSingleCommand(client, "[ -f /etc/networks ] && echo yes"); err == nil {
			out += "\n/etc/networks"
		}
		// Aggiungi cartelle if-up.d, if-down.d etc se non trovate da find (find potrebbe non avere permessi su tutto)
		extraDirs, _ := runSingleCommand(client, fmt.Sprintf("%sls -d /etc/network/if-*.d 2>/dev/null", cmdPrefix))
		out += "\n" + extraDirs

		sendToHQ("etc_net_list", hostID, termID, strings.Fields(out))

	case "read_root_file":
		f, _ := payload["file"].(string)
		out, _ := runSingleCommand(client, fmt.Sprintf("%scat \"%s\"", cmdPrefix, f))
		sendToHQ("root_file_content", hostID, termID, map[string]string{"file": f, "content": out})

	case "save_root_file":
		f, _ := payload["file"].(string)
		c, _ := payload["content"].(string)
		b64 := base64.StdEncoding.EncodeToString([]byte(c))
		tmpB64 := fmt.Sprintf("/tmp/root_save_%d.b64", time.Now().UnixNano())
		tmpFile := fmt.Sprintf("/tmp/root_save_%d.tmp", time.Now().UnixNano())
		runSingleCommand(client, fmt.Sprintf("echo '%s' > %s", b64, tmpB64))
		runSingleCommand(client, fmt.Sprintf("base64 -d -i %s > %s", tmpB64, tmpFile))
		runSingleCommand(client, "rm -f "+tmpB64)
		runSingleCommand(client, fmt.Sprintf("%smv -f %s \"%s\"", cmdPrefix, tmpFile, f))

		// Se è uno script in if-*.d, rendilo eseguibile
		if strings.Contains(f, "/if-") && strings.Contains(f, ".d/") {
			runSingleCommand(client, fmt.Sprintf("%schmod +x \"%s\"", cmdPrefix, f))
		}
		sendToHQ("net_op_res", hostID, termID, map[string]string{"status": "success", "msg": "File saved."})

	case "create_root_file":
		path, _ := payload["path"].(string)
		name, _ := payload["name"].(string)
		fullPath := filepath.Join(path, name)
		runSingleCommand(client, fmt.Sprintf("%stouch \"%s\"", cmdPrefix, fullPath))
		sendToHQ("net_op_res", hostID, termID, map[string]string{"status": "success", "msg": "File created."})

	// --- NETWORK MANAGER ---
	case "list_nm_connections":
		out, _ := runSingleCommand(client, fmt.Sprintf("%sls /etc/NetworkManager/system-connections/", cmdPrefix))
		sendToHQ("nm_conn_list", hostID, termID, strings.Fields(out))

	// --- IPTABLES CONFIG MANAGER ---
	case "list_iptables_configs":
		// Cerca file rules in /etc/iptables e /etc/ (comuni: rules.v4, iptables.rules, o file con 'iptable' nel nome)
		out, _ := runSingleCommand(client, fmt.Sprintf("%sfind /etc/iptables /etc -maxdepth 1 \\( -name '*rules*' -o -name '*iptable*' -o -name '*.v4' -o -name '*.v6' \\) 2>/dev/null", cmdPrefix))
		sendToHQ("iptables_config_list", hostID, termID, strings.Fields(out))

	case "read_iptables_config":
		f, _ := payload["file"].(string)
		out, _ := runSingleCommand(client, fmt.Sprintf("%scat \"%s\"", cmdPrefix, f))
		sendToHQ("iptables_config_content", hostID, termID, map[string]string{"file": f, "content": out})

	case "save_iptables_config":
		f, _ := payload["file"].(string)
		c, _ := payload["content"].(string)
		b64 := base64.StdEncoding.EncodeToString([]byte(c))
		tmpB64 := fmt.Sprintf("/tmp/ipt_save_%d.b64", time.Now().UnixNano())
		tmpFile := fmt.Sprintf("/tmp/ipt_save_%d.tmp", time.Now().UnixNano())
		runSingleCommand(client, fmt.Sprintf("echo '%s' > %s", b64, tmpB64))
		runSingleCommand(client, fmt.Sprintf("base64 -d -i %s > %s", tmpB64, tmpFile))
		runSingleCommand(client, "rm -f "+tmpB64)
		runSingleCommand(client, fmt.Sprintf("%smv -f %s \"%s\"", cmdPrefix, tmpFile, f))
		sendToHQ("net_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Config saved."})

	case "delete_iptables_config":
		f, _ := payload["file"].(string)
		runSingleCommand(client, fmt.Sprintf("%srm \"%s\"", cmdPrefix, f))
		sendToHQ("net_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Config deleted."})

	case "restore_iptables_config":
		f, _ := payload["file"].(string)
		out, err := runSingleCommand(client, fmt.Sprintf("%siptables-restore < \"%s\"", cmdPrefix, f))
		if err != nil {
			sendToHQ("net_op_res", hostID, termID, map[string]string{"status": "error", "msg": out})
		} else {
			sendToHQ("net_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Rules restored from " + f})
		}

	// --- UFW ---
	case "ufw_status":
		// Check if ufw exists first to avoid confusion
		check, _ := runSingleCommand(client, "command -v ufw || echo 'missing'")
		if strings.TrimSpace(check) == "missing" {
			sendToHQ("ufw_status_info", hostID, termID, "Status: not_installed")
		} else {
			out, _ := runSingleCommand(client, fmt.Sprintf("%sufw status verbose", cmdPrefix))
			sendToHQ("ufw_status_info", hostID, termID, out)
		}

	case "ufw_rules":
		out, _ := runSingleCommand(client, fmt.Sprintf("%sufw status numbered", cmdPrefix))
		sendToHQ("ufw_rules_list", hostID, termID, out)

	case "ufw_op":
		op, _ := payload["op"].(string) // enable, disable, reload
		out, err := runSingleCommand(client, fmt.Sprintf("%sufw %s", cmdPrefix, op))
		if err != nil {
			sendToHQ("net_op_res", hostID, termID, map[string]string{"status": "error", "msg": out})
		} else {
			sendToHQ("net_op_res", hostID, termID, map[string]string{"status": "success", "msg": "UFW " + op + " executed"})
		}

	case "ufw_add_rule":
		rule, _ := payload["rule"].(string) // e.g. "allow 22/tcp"
		out, err := runSingleCommand(client, fmt.Sprintf("%sufw %s", cmdPrefix, rule))
		if err != nil {
			sendToHQ("net_op_res", hostID, termID, map[string]string{"status": "error", "msg": out})
		} else {
			sendToHQ("net_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Rule added"})
		}

	case "ufw_delete_rule":
		num, _ := payload["num"].(string)
		// ufw delete <num> requires confirmation, use echo y
		out, err := runSingleCommand(client, fmt.Sprintf("echo 'y' | %sufw delete %s", cmdPrefix, num))
		if err != nil {
			sendToHQ("net_op_res", hostID, termID, map[string]string{"status": "error", "msg": out})
		} else {
			sendToHQ("net_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Rule deleted"})
		}

	// --- SELINUX ---
	case "selinux_status":
		// Use cmdPrefix to ensure we find sestatus in sbin and have permissions
		sestatus, _ := runSingleCommand(client, fmt.Sprintf("%ssestatus", cmdPrefix))
		sendToHQ("selinux_info", hostID, termID, sestatus)

	case "selinux_set_mode":
		mode, _ := payload["mode"].(string) // Enforcing, Permissive, Disabled
		// 1. Runtime change (if not Disabled)
		if mode != "Disabled" {
			val := "0"
			if mode == "Enforcing" {
				val = "1"
			}
			runSingleCommand(client, fmt.Sprintf("%ssetenforce %s", cmdPrefix, val))
		}
		// 2. Persistent change in /etc/selinux/config
		targetState := strings.ToLower(mode)
		cmd := fmt.Sprintf("%ssed -i 's/^SELINUX=.*/SELINUX=%s/' /etc/selinux/config", cmdPrefix, targetState)
		out, err := runSingleCommand(client, cmd)
		if err != nil {
			sendToHQ("net_op_res", hostID, termID, map[string]string{"status": "error", "msg": out})
		} else {
			msg := "SELinux mode set to " + mode + " (Config updated)"
			if mode == "Disabled" {
				msg += ". Reboot required to apply."
			}
			sendToHQ("net_op_res", hostID, termID, map[string]string{"status": "success", "msg": msg})
		}

	case "selinux_get_booleans":
		out, _ := runSingleCommand(client, fmt.Sprintf("%sgetsebool -a", cmdPrefix))
		sendToHQ("selinux_booleans", hostID, termID, out)

	case "selinux_set_bool":
		key, _ := payload["key"].(string)
		val, _ := payload["value"].(string) // on/off
		out, err := runSingleCommand(client, fmt.Sprintf("%ssetsebool -P %s %s", cmdPrefix, key, val))
		if err != nil {
			sendToHQ("net_op_res", hostID, termID, map[string]string{"status": "error", "msg": out})
		} else {
			sendToHQ("net_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Boolean updated"})
		}
	case "list_logs":
		// Use sudo to list logs
		out, _ := runSingleCommand(client, fmt.Sprintf("%sls -lAF --time-style=long-iso /var/log/nginx/", cmdPrefix))
		sendToHQ("nginx_log_list", hostID, termID, out)
	}
}

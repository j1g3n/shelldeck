package main

import (
	"bufio"
	"fmt"
	"strings"
)

func handlePkgCommand(hostID int, termID string, payload map[string]interface{}) {
	sess, err := ensureSession(hostID, GetSessionIDWithPrefix(termID, "main"))
	if err != nil {
		sendToHQ("pkg_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Connection Error: " + err.Error()})
		return
	}
	action, _ := payload["action"].(string)
	// FIX: Escape single quotes in password to prevent shell syntax errors
	safePass := strings.ReplaceAll(sess.Password, "'", "'\\''")
	cmdPrefix := fmt.Sprintf("echo '%s' | sudo -S -p '' env DEBIAN_FRONTEND=noninteractive ", safePass)
	client := sess.Client
	wsID := GetWorkspaceID(termID)

	// Rileva il gestore pacchetti una volta sola per questa richiesta
	out, _ := runSingleCommand(client, "if command -v dnf >/dev/null 2>&1; then echo dnf; else echo apt; fi")
	pkgMgr := strings.TrimSpace(out)

	switch action {
	case "detect_manager":
		sendToHQ("pkg_manager_info", hostID, termID, pkgMgr)

	case "list_installed":
		var cmd string
		if pkgMgr == "dnf" {
			cmd = "rpm -qa --queryformat '%{NAME} %{VERSION} installed\n'"
		} else {
			cmd = "dpkg-query -W -f='${Package} ${Version} ${Status}\n'"
		}
		out, _ := runSingleCommand(client, cmd)

		var list []map[string]string
		scanner := bufio.NewScanner(strings.NewReader(out))
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) >= 3 {
				// Normalizza output
				name := fields[0]
				ver := fields[1]
				status := strings.Join(fields[2:], " ")
				list = append(list, map[string]string{"name": name, "version": ver, "status": status})
			}
		}
		sendToHQ("pkg_installed_list", hostID, termID, list)

	// --- OPERAZIONI DI SISTEMA (Update, Upgrade, ecc) ---
	case "update":
		var cmd string
		if pkgMgr == "dnf" {
			cmd = fmt.Sprintf("%sdnf check-update", cmdPrefix)
		} else {
			cmd = fmt.Sprintf("%sapt-get update -y", cmdPrefix)
		}
		go startStream(hostID, wsID, "pkg_op", cmd, "pkg_output_stream", client)

	case "upgrade":
		var cmd string
		if pkgMgr == "dnf" {
			cmd = fmt.Sprintf("%sdnf upgrade -y", cmdPrefix)
		} else {
			cmd = fmt.Sprintf("%sapt-get upgrade -y", cmdPrefix)
		}
		go startStream(hostID, wsID, "pkg_op", cmd, "pkg_output_stream", client)

	case "dist_upgrade":
		var cmd string
		if pkgMgr == "dnf" {
			cmd = fmt.Sprintf("%sdnf upgrade --refresh -y", cmdPrefix)
		} else {
			cmd = fmt.Sprintf("%sapt-get dist-upgrade -y", cmdPrefix)
		}
		go startStream(hostID, wsID, "pkg_op", cmd, "pkg_output_stream", client)

	case "clean":
		var cmd string
		if pkgMgr == "dnf" {
			cmd = fmt.Sprintf("%sdnf clean all && %sdnf autoremove -y", cmdPrefix, cmdPrefix)
		} else {
			cmd = fmt.Sprintf("%sapt-get autoremove -y && %sapt-get clean", cmdPrefix, cmdPrefix)
		}
		go startStream(hostID, wsID, "pkg_op", cmd, "pkg_output_stream", client)

	case "unlock_apt":
		// Rimuove lock sia per apt che per rpm/dnf
		cmd := fmt.Sprintf("%srm -f /var/lib/apt/lists/lock /var/cache/apt/archives/lock /var/lib/dpkg/lock* /var/lib/rpm/.dbenv.lock", cmdPrefix)
		go startStream(hostID, wsID, "pkg_op", cmd, "pkg_output_stream", client)

	case "fix_broken":
		var cmd string
		if pkgMgr == "dnf" {
			cmd = "echo 'DNF gestisce le dipendenze automaticamente.'"
		} else {
			cmd = fmt.Sprintf("%sapt-get install -f -y", cmdPrefix)
		}
		go startStream(hostID, wsID, "pkg_op", cmd, "pkg_output_stream", client)

	// --- OPERAZIONI SUI PACCHETTI ---
	case "install":
		pkg, _ := payload["package"].(string)
		if pkg != "" {
			var cmd string
			if pkgMgr == "dnf" {
				cmd = fmt.Sprintf("%sdnf install -y %s", cmdPrefix, pkg)
			} else {
				cmd = fmt.Sprintf("%sapt-get install -y %s", cmdPrefix, pkg)
			}
			go startStream(hostID, wsID, "pkg_op", cmd, "pkg_output_stream", client)
		}

	case "remove":
		pkg, _ := payload["package"].(string)
		if pkg != "" {
			var cmd string
			if pkgMgr == "dnf" {
				cmd = fmt.Sprintf("%sdnf remove -y %s", cmdPrefix, pkg)
			} else {
				cmd = fmt.Sprintf("%sapt-get remove -y %s", cmdPrefix, pkg)
			}
			go startStream(hostID, wsID, "pkg_op", cmd, "pkg_output_stream", client)
		}

	case "reinstall":
		pkg, _ := payload["package"].(string)
		if pkg != "" {
			var cmd string
			if pkgMgr == "dnf" {
				cmd = fmt.Sprintf("%sdnf reinstall -y %s", cmdPrefix, pkg)
			} else {
				cmd = fmt.Sprintf("%sapt-get install --reinstall -y %s", cmdPrefix, pkg)
			}
			go startStream(hostID, wsID, "pkg_op", cmd, "pkg_output_stream", client)
		}

	case "hold":
		pkg, _ := payload["package"].(string)
		if pkg != "" {
			var cmd string
			if pkgMgr == "dnf" {
				cmd = fmt.Sprintf("%sdnf versionlock add %s", cmdPrefix, pkg)
			} else {
				cmd = fmt.Sprintf("%sapt-mark hold %s", cmdPrefix, pkg)
			}
			go startStream(hostID, wsID, "pkg_op", cmd, "pkg_output_stream", client)
		}

	case "unhold":
		pkg, _ := payload["package"].(string)
		if pkg != "" {
			var cmd string
			if pkgMgr == "dnf" {
				cmd = fmt.Sprintf("%sdnf versionlock delete %s", cmdPrefix, pkg)
			} else {
				cmd = fmt.Sprintf("%sapt-mark unhold %s", cmdPrefix, pkg)
			}
			go startStream(hostID, wsID, "pkg_op", cmd, "pkg_output_stream", client)
		}

	case "search":
		query, _ := payload["query"].(string)
		if query != "" {
			var cmd string
			if pkgMgr == "dnf" {
				cmd = fmt.Sprintf("dnf search -q '%s' | tail -n +2", query)
			} else {
				cmd = fmt.Sprintf("apt-cache search '%s'", query)
			}
			out, _ := runSingleCommand(client, cmd)

			var list []map[string]string
			scanner := bufio.NewScanner(strings.NewReader(out))
			for scanner.Scan() {
				line := scanner.Text()
				// Parsing euristico
				if strings.Contains(line, " : ") { // DNF style sometimes
					parts := strings.SplitN(line, " : ", 2)
					list = append(list, map[string]string{"name": strings.TrimSpace(parts[0]), "desc": strings.TrimSpace(parts[1])})
				} else if strings.Contains(line, " - ") { // APT style
					parts := strings.SplitN(line, " - ", 2)
					list = append(list, map[string]string{"name": strings.TrimSpace(parts[0]), "desc": strings.TrimSpace(parts[1])})
				} else {
					// Fallback
					parts := strings.Fields(line)
					if len(parts) > 0 {
						list = append(list, map[string]string{"name": parts[0], "desc": strings.Join(parts[1:], " ")})
					}
				}
			}
			sendToHQ("pkg_search_results", hostID, termID, list)
		}

	case "list_repos":
		var cmd string
		if pkgMgr == "dnf" {
			cmd = "dnf repolist"
		} else {
			cmd = "grep -r ^ /etc/apt/sources.list /etc/apt/sources.list.d/"
		}
		out, _ := runSingleCommand(client, cmd)
		sendToHQ("pkg_repo_list", hostID, termID, out)

	case "add_repo":
		repo, _ := payload["package"].(string)
		if repo != "" {
			var cmd string
			if pkgMgr == "dnf" {
				cmd = fmt.Sprintf("%sdnf config-manager --add-repo '%s'", cmdPrefix, repo)
			} else {
				cmd = fmt.Sprintf("%sadd-apt-repository -y '%s' && %sapt-get update", cmdPrefix, repo, cmdPrefix)
			}
			go startStream(hostID, wsID, "pkg_op", cmd, "pkg_output_stream", client)
		}

	case "install_url":
		url, _ := payload["url"].(string)
		if url != "" {
			var cmd string
			if pkgMgr == "dnf" {
				cmd = fmt.Sprintf("echo 'Detected DNF. Installing RPM from URL...' && %sdnf install -y '%s'", cmdPrefix, url)
			} else {
				cmd = fmt.Sprintf("tmp_file=\"/tmp/pkg_install_$(date +%%s).deb\" && wget -O \"$tmp_file\" '%s' && %sdpkg -i \"$tmp_file\" || %sapt-get install -f -y; rm -f \"$tmp_file\"", url, cmdPrefix, cmdPrefix)
			}
			go startStream(hostID, wsID, "pkg_op", cmd, "pkg_output_stream", client)
		}
	}
}

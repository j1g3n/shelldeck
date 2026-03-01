package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

func cidrToMask(prefix int) string {
	if prefix < 0 || prefix > 32 {
		return ""
	}
	mask := net.CIDRMask(prefix, 32)
	return fmt.Sprintf("%d.%d.%d.%d", mask[0], mask[1], mask[2], mask[3])
}

// --- MONITORAGGIO ---
func handleDashboardStartMonitoring(hostID int, workspaceID int, termID string) {
	// This function is now triggered on-demand by the dashboard.
	// It needs a client and a lifecycle marker in the session map.
	sess, err := ensureSession(hostID, termID)
	if err != nil || sess == nil {
		log.Printf("[DASH-MONITOR] Error ensuring session for Host %d: %v", hostID, err)
		return
	}
	client := sess.Client

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	// Execute immediately, then wait for ticker
	for ; ; <-ticker.C {
		if !isSessionActive(hostID, termID) {
			log.Printf("[DASH-MONITOR] Stopping monitoring for %s as session is no longer active.", termID)
			return
		}

		// Optimization: Combine commands to reduce SSH round-trips
		cmd := `
hostname
echo "#####"
grep PRETTY_NAME /etc/os-release 2>/dev/null | cut -d'"' -f2
echo "#####"
uname -r
echo "#####"
uname -m
echo "#####"
uptime -p
echo "#####"
top -bn1 2>/dev/null | grep 'Cpu(s)' | awk '{print $2}'
echo "#####"
cat /proc/loadavg 2>/dev/null | awk '{print $1}'
echo "#####"
free -m 2>/dev/null | grep -E 'Mem|Swap'
echo "#####"
df / --output=pcent 2>/dev/null | tail -1
echo "#####"
lsblk -nbio NAME,SIZE,MOUNTPOINT,FSUSE% -e7 2>/dev/null || lsblk -nbio NAME,SIZE,MOUNTPOINT,FSUSE% 2>/dev/null | grep -v 'loop'
echo "#####"
iface=$(ip route get 1.1.1.1 2>/dev/null | awk '{print $5; exit}')
echo "$iface"
echo "#####"
if [ -n "$iface" ]; then
    cat /proc/net/dev 2>/dev/null | grep "$iface" | awk '{print $2 " " $10}'
    echo "#####"
    hostname -I 2>/dev/null | awk '{print $1}'
    echo "#####"
    ip -o -f inet addr show "$iface" 2>/dev/null | awk '{print $4}'
    echo "#####"
    cat /sys/class/net/"$iface"/address 2>/dev/null
else
    echo ""
    echo "#####"
    echo ""
    echo "#####"
    echo ""
    echo "#####"
    echo ""
fi
echo "#####"
ip route 2>/dev/null | grep default | awk '{print $3}'
echo "#####"
resolvectl status 2>/dev/null | grep 'DNS Servers' | sed 's/.*DNS Servers: //' | tr -d '\n' || grep '^nameserver' /etc/resolv.conf 2>/dev/null | awk '{print $2}' | tr '\n' ' '
echo ""
echo "#####"
for s in apache2 nginx mariadb mysql docker; do
    st=$(systemctl is-active $s 2>/dev/null)
    if [ "$st" = "active" ]; then
        echo "$s|present|active"
    elif [ "$st" = "inactive" ] || [ "$st" = "failed" ] || [ "$st" = "activating" ] || [ "$st" = "deactivating" ]; then
        echo "$s|present|$st"
    else
        echo "$s|not_present|-"
    fi
done
echo "#####"
upd_cnt="?"
if [ -x /usr/lib/update-notifier/apt-check ]; then
    upd_cnt=$(/usr/lib/update-notifier/apt-check 2>&1 | cut -d';' -f1)
elif command -v dnf >/dev/null 2>&1; then
    cnt=$(dnf check-update -q | grep -v '^$' | wc -l)
    upd_cnt=$cnt
elif command -v yum >/dev/null 2>&1; then
    cnt=$(yum check-update -q | grep -v '^$' | wc -l)
    upd_cnt=$cnt
fi
reboot="0"
if [ -f /var/run/reboot-required ]; then
    reboot="1"
elif command -v dnf >/dev/null 2>&1; then
    if dnf needs-restarting -r >/dev/null 2>&1; then
        : 
    else
        if [ $? -eq 1 ]; then reboot="1"; fi
    fi
fi
echo "$upd_cnt|$reboot"
echo "#####"
ps -eo pid,pcpu,pmem,comm --sort=-pcpu 2>/dev/null | head -n 8 | tail -n 7
echo "#####"
systemd-cgtop -b -n 10 --order=cpu 2>/dev/null || echo ""
`

		// Stream execution to update UI progressively
		session, err := client.NewSession()
		if err != nil {
			log.Printf("[DASH-MONITOR] Session creation error: %v", err)
			continue
		}

		stdout, err := session.StdoutPipe()
		if err != nil {
			session.Close()
			continue
		}

		if err := session.Start(cmd); err != nil {
			session.Close()
			continue
		}

		scanner := bufio.NewScanner(stdout)
		var buffer bytes.Buffer
		sectionIndex := 0

		// Helper variables for network stats calculation across sections
		var ifName string
		var rxKB, txKB float64
		var activityPerc int
		netInfo := make(map[string]string)

		for scanner.Scan() {
			line := scanner.Text()
			if strings.TrimSpace(line) == "#####" {
				// Process section
				part := buffer.String()
				stats := make(map[string]interface{})

				switch sectionIndex {
				case 0:
					stats["hostname"] = strings.TrimSpace(part)
				case 1:
					stats["os_distro"] = strings.TrimSpace(part)
				case 2:
					stats["kernel"] = strings.TrimSpace(part)
				case 3:
					stats["arch"] = strings.TrimSpace(part)
				case 4:
					stats["uptime"] = strings.TrimSpace(strings.Replace(part, "up ", "", 1))
				case 5:
					stats["cpu_perc"] = strings.TrimSpace(part)
				case 6:
					stats["load"] = strings.TrimSpace(part)
				case 7:
					lines := strings.Split(strings.TrimSpace(part), "\n")
					for _, line := range lines {
						f := strings.Fields(line)
						if len(f) < 3 {
							continue
						}
						if strings.HasPrefix(line, "Mem:") {
							stats["ram"] = map[string]interface{}{"total": f[1] + "MB", "used": f[2] + "MB", "perc": calculatePerc(f[2], f[1])}
						} else if strings.HasPrefix(line, "Swap:") {
							stats["swap"] = map[string]interface{}{"total": f[1] + "MB", "used": f[2] + "MB", "perc": calculatePerc(f[2], f[1])}
						}
					}
				case 8:
					stats["disk"] = map[string]interface{}{"perc": strings.Trim(strings.TrimSpace(part), "%")}
				case 9:
					// Disks Tree
					var devices []map[string]interface{}
					s := bufio.NewScanner(strings.NewReader(part))
					for s.Scan() {
						fields := strings.Fields(s.Text())
						if len(fields) >= 2 {
							dev := map[string]interface{}{"name": fields[0], "size": fields[1]}
							if len(fields) >= 4 {
								dev["mount"] = fields[2]
								dev["usage"] = fields[3]
							}
							devices = append(devices, dev)
						}
					}
					stats["disks_tree"] = devices
				case 10:
					ifName = strings.TrimSpace(part)
				case 11:
					// Net Stats
					if ifName != "" {
						netFields := strings.Fields(part)
						if len(netFields) >= 2 {
							var currentIn, currentOut uint64
							fmt.Sscanf(netFields[0], "%d", &currentIn)
							fmt.Sscanf(netFields[1], "%d", &currentOut)
							currentTime := time.Now()
							netKey := fmt.Sprintf("%d:%d", workspaceID, hostID)
							if lastTime, ok := prevNetTime[netKey]; ok && !lastTime.IsZero() {
								duration := currentTime.Sub(lastTime).Seconds()
								if duration > 0 {
									rxKB = float64(currentIn-prevNetIn[netKey]) / 1024 / duration
									txKB = float64(currentOut-prevNetOut[netKey]) / 1024 / duration
									activityPerc = int((rxKB + txKB) / 10)
									if activityPerc > 100 {
										activityPerc = 100
									}
									if activityPerc < 2 && (rxKB+txKB) > 0 {
										activityPerc = 2
									}
								}
							}
							prevNetIn[netKey], prevNetOut[netKey], prevNetTime[netKey] = currentIn, currentOut, currentTime
						}
					}
				case 12:
					netInfo["ip"] = strings.TrimSpace(part)
				case 13:
					netInfo["subnet"] = strings.TrimSpace(part)
				case 14:
					netInfo["mac"] = strings.TrimSpace(part)
				case 15:
					netInfo["gateway"] = strings.TrimSpace(part)
				case 16:
					netInfo["dns"] = strings.TrimSpace(part)
					// Construct Network Object
					subnetTrimmed := netInfo["subnet"]
					subnetParts := strings.Split(subnetTrimmed, "/")
					var subnetMask string
					if len(subnetParts) == 2 {
						prefix, err := strconv.Atoi(subnetParts[1])
						if err == nil {
							subnetMask = cidrToMask(prefix)
						}
					}
					stats["network"] = map[string]interface{}{
						"ip":            netInfo["ip"],
						"subnet":        subnetTrimmed,
						"subnet_mask":   subnetMask,
						"gateway":       netInfo["gateway"],
						"mac":           netInfo["mac"],
						"dns":           netInfo["dns"],
						"in":            fmt.Sprintf("%.1f KB/s", rxKB),
						"out":           fmt.Sprintf("%.1f KB/s", txKB),
						"activity_perc": activityPerc,
					}
				case 17:
					// Core Services
					var coreServices []map[string]string
					svcScanner := bufio.NewScanner(strings.NewReader(part))
					for svcScanner.Scan() {
						line := svcScanner.Text()
						sp := strings.Split(line, "|")
						if len(sp) >= 3 {
							coreServices = append(coreServices, map[string]string{"name": sp[0], "present": sp[1], "status": sp[2]})
						}
					}
					stats["core_services"] = coreServices
				case 18:
					// Updates
					parts := strings.Split(strings.TrimSpace(part), "|")
					if len(parts) >= 2 {
						stats["updates"] = map[string]string{
							"count":  strings.TrimSpace(parts[0]),
							"reboot": strings.TrimSpace(parts[1]),
						}
					}
				case 19:
					// Processes
					var procs []map[string]string
					psScanner := bufio.NewScanner(strings.NewReader(part))
					for psScanner.Scan() {
						fields := strings.Fields(psScanner.Text())
						if len(fields) >= 4 {
							procs = append(procs, map[string]string{"pid": fields[0], "cpu": fields[1], "mem": fields[2], "cmd": fields[3]})
						}
					}
					stats["processes"] = procs
				}

				if len(stats) > 0 {
					sendToHQ("sys_stats", hostID, termID, stats)
				}

				buffer.Reset()
				sectionIndex++
			} else {
				buffer.WriteString(line + "\n")
			}
		}

		// Handle the last section (20: services)
		if buffer.Len() > 0 && sectionIndex == 20 {
			stats := make(map[string]interface{})
			var services []map[string]string
			svcsScanner := bufio.NewScanner(strings.NewReader(buffer.String()))
			isFirstLine := true
			for svcsScanner.Scan() {
				if len(services) >= 5 {
					break
				}
				if isFirstLine {
					isFirstLine = false
					continue
				}
				line := svcsScanner.Text()
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					fullPath := fields[0]
					cpuUsage := fields[1]
					parts := strings.Split(fullPath, "/")
					serviceName := parts[len(parts)-1]
					if strings.HasSuffix(serviceName, ".service") {
						services = append(services, map[string]string{"name": serviceName, "cpu": cpuUsage})
					}
				}
			}
			stats["services"] = services
			sendToHQ("sys_stats", hostID, termID, stats)
		}

		session.Wait()
		session.Close()
	}
}

func handleDashboardStartLogs(hostID int, workspaceID int, termID string) {
	// This function is now triggered on-demand by the dashboard.
	// It runs in its own session channel.
	sess, err := ensureSession(hostID, termID)
	if err != nil || sess == nil {
		log.Printf("[DASH-LOGS] Error ensuring session for Host %d, TermID %s: %v", hostID, termID, err)
		return
	}

	// Create a dedicated session for logs (since ensureSession skips it for dashboard-logs)
	session, err := sess.Client.NewSession()
	if err != nil {
		log.Printf("[DASH-LOGS] Error creating SSH session: %v", err)
		return
	}
	defer session.Close()

	stdout, err := session.StdoutPipe()
	if err != nil {
		log.Printf("[DASH-LOGS] Error creating stdout pipe for %s: %v", termID, err)
		return
	}

	cmd := "tail -n 5 -f /var/log/syslog 2>/dev/null || journalctl -f -n 5"
	if sess.Password != "" {
		safePass := strings.ReplaceAll(sess.Password, "'", "'\\''")
		cmd = fmt.Sprintf("echo '%s' | sudo -S -p '' sh -c 'tail -n 5 -f /var/log/syslog 2>/dev/null || journalctl -f -n 5'", safePass)
	}

	if err := session.Start(cmd); err != nil {
		log.Printf("[DASH-LOGS] Error starting log command for %s: %v", termID, err)
		return
	}

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		if !isSessionActive(hostID, termID) {
			log.Printf("[DASH-LOGS] Stopping log stream for %s as session is no longer active.", termID)
			return
		}
		sendToHQ("sys_logs", hostID, termID, scanner.Text())
	}
}

// --- SERVIZI ---
func handleServiceCommand(hostID int, termID string, payload map[string]interface{}) {
	sess, err := ensureSession(hostID, GetSessionIDWithPrefix(termID, "main"))
	if err != nil {
		log.Println("Error ensuring session for services:", err)
		return
	}
	client := sess.Client
	safePass := strings.ReplaceAll(sess.Password, "'", "'\\''")
	cmdPrefix := fmt.Sprintf("echo '%s' | sudo -S -p '' ", safePass)

	action, _ := payload["action"].(string)

	switch action {
	case "list":
		cmd1 := fmt.Sprintf("%ssystemctl list-units --type=service,timer --all --no-pager --no-legend --plain | awk '{print $1 \" \" $3 \" \" $4}'", cmdPrefix)
		out1, _ := runSingleCommand(client, cmd1)

		cmd2 := fmt.Sprintf("%ssystemctl list-unit-files --type=service,timer --no-pager --no-legend | awk '{print $1 \" \" $2}'", cmdPrefix)
		out2, _ := runSingleCommand(client, cmd2)

		services := make(map[string]*ServiceItem)
		scanner1 := bufio.NewScanner(strings.NewReader(out1))
		for scanner1.Scan() {
			fields := strings.Fields(scanner1.Text())
			if len(fields) >= 3 {
				name := fields[0]
				services[name] = &ServiceItem{Name: name, Active: fields[1], Sub: fields[2], Enabled: "unknown"}
			}
		}

		scanner2 := bufio.NewScanner(strings.NewReader(out2))
		for scanner2.Scan() {
			fields := strings.Fields(scanner2.Text())
			if len(fields) >= 2 {
				name := fields[0]
				if item, ok := services[name]; ok {
					item.Enabled = fields[1]
				} else {
					services[name] = &ServiceItem{Name: name, Active: "inactive", Sub: "dead", Enabled: fields[1]}
				}
			}
		}

		var list []*ServiceItem
		for _, v := range services {
			list = append(list, v)
		}
		sendToHQ("svc_list", hostID, termID, list)

	case "control":
		svc, _ := payload["service"].(string)
		cmd, _ := payload["cmd"].(string)
		fullCmd := fmt.Sprintf("%ssystemctl %s %s", cmdPrefix, cmd, svc)
		out, err := runSingleCommand(client, fullCmd)
		if err != nil {
			sendToHQ("svc_op_res", hostID, termID, map[string]string{"status": "error", "msg": out})
		} else {
			sendToHQ("svc_op_res", hostID, termID, map[string]string{"status": "success"})
		}

	case "status":
		svc, _ := payload["service"].(string)
		fullCmd := fmt.Sprintf("systemctl status %s -n 50 --no-pager", svc)
		out, _ := runSingleCommand(client, fullCmd)
		sendToHQ("svc_status_out", hostID, termID, out)

	case "get_file":
		svc, _ := payload["service"].(string)
		pathCmd := fmt.Sprintf("systemctl show -p FragmentPath %s | cut -d= -f2", svc)
		path, _ := runSingleCommand(client, pathCmd)
		path = strings.TrimSpace(path)
		if path == "" || path == "/dev/null" {
			path = "/etc/systemd/system/" + svc
		}
		out, _ := runSingleCommand(client, cmdPrefix+"cat "+path)
		sendToHQ("svc_file_content", hostID, termID, map[string]string{"filename": svc, "content": out})

	case "save_file":
		svc, _ := payload["service"].(string)
		content, _ := payload["content"].(string)
		pathCmd := fmt.Sprintf("systemctl show -p FragmentPath %s | cut -d= -f2", svc)
		path, _ := runSingleCommand(client, pathCmd)
		path = strings.TrimSpace(path)
		if path == "" {
			path = "/etc/systemd/system/" + svc
		}

		// Usa streaming su file temporaneo per evitare problemi di pipe/escaping
		tmpFile := fmt.Sprintf("/tmp/svc_save_%d", time.Now().UnixNano())
		s, err := client.NewSession()
		if err != nil {
			sendToHQ("svc_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Session error: " + err.Error()})
			return
		}
		stdin, _ := s.StdinPipe()
		if err := s.Start("cat > " + tmpFile); err != nil {
			s.Close()
			sendToHQ("svc_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Start cat error: " + err.Error()})
			return
		}
		if _, err := stdin.Write([]byte(content)); err != nil {
			stdin.Close()
			s.Close()
			sendToHQ("svc_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Write content error: " + err.Error()})
			return
		}
		stdin.Close()
		if err := s.Wait(); err != nil {
			s.Close()
			sendToHQ("svc_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Wait cat error: " + err.Error()})
			return
		}
		s.Close()

		// Sposta il file temporaneo nella destinazione finale con sudo
		mvCmd := fmt.Sprintf("%smv -f \"%s\" \"%s\"", cmdPrefix, tmpFile, path)
		out, err := runSingleCommand(client, mvCmd)
		if err != nil {
			sendToHQ("svc_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Save failed: " + err.Error() + " " + out})
			return
		}
		runSingleCommand(client, cmdPrefix+"systemctl daemon-reload")
		sendToHQ("svc_op_res", hostID, termID, map[string]string{"status": "success"})

	case "create_service":
		svc, _ := payload["service"].(string)
		content, _ := payload["content"].(string)
		svc = filepath.Base(svc) // Sanitize filename
		path := "/etc/systemd/system/" + svc

		// Usa streaming su file temporaneo
		tmpFile := fmt.Sprintf("/tmp/svc_create_%d", time.Now().UnixNano())
		s, err := client.NewSession()
		if err != nil {
			sendToHQ("svc_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Session error: " + err.Error()})
			return
		}
		stdin, _ := s.StdinPipe()
		if err := s.Start("cat > " + tmpFile); err != nil {
			s.Close()
			sendToHQ("svc_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Start cat error: " + err.Error()})
			return
		}
		if _, err := stdin.Write([]byte(content)); err != nil {
			stdin.Close()
			s.Close()
			sendToHQ("svc_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Write content error: " + err.Error()})
			return
		}
		stdin.Close()
		if err := s.Wait(); err != nil {
			s.Close()
			sendToHQ("svc_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Wait cat error: " + err.Error()})
			return
		}
		s.Close()

		mvCmd := fmt.Sprintf("%smv -f \"%s\" \"%s\"", cmdPrefix, tmpFile, path)
		out, err := runSingleCommand(client, mvCmd)
		if err != nil {
			runSingleCommand(client, "rm -f "+tmpFile)
			sendToHQ("svc_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Create failed: " + err.Error() + " " + out})
			return
		}

		runSingleCommand(client, fmt.Sprintf("%schown root:root \"%s\"", cmdPrefix, path))
		runSingleCommand(client, fmt.Sprintf("%schmod 644 \"%s\"", cmdPrefix, path))

		// Verifica esistenza file
		if _, err := runSingleCommand(client, fmt.Sprintf("%sls \"%s\"", cmdPrefix, path)); err != nil {
			sendToHQ("svc_op_res", hostID, termID, map[string]string{"status": "error", "msg": "File created but verification failed. Check permissions."})
			return
		}

		runSingleCommand(client, cmdPrefix+"systemctl daemon-reload")
		sendToHQ("svc_op_res", hostID, termID, map[string]string{"status": "success"})

	case "monitor_start":
		// Placeholder: il frontend usa il polling con get_stats
	case "monitor_stop":
		// Placeholder

	case "get_stats":
		// 1. Ottieni PID mappati per Unit (ps -eo unit,pid)
		psOut, _ := runSingleCommand(client, "ps -eo unit,pid | grep '.service'")
		pidMap := make(map[string]string)
		scannerPs := bufio.NewScanner(strings.NewReader(psOut))
		for scannerPs.Scan() {
			fields := strings.Fields(scannerPs.Text())
			if len(fields) >= 2 {
				pidMap[fields[0]] = fields[1]
			}
		}

		// 2. Ottieni Risorse (systemd-cgtop)
		// Usa cmdPrefix per vedere tutti i cgroups
		cgOut, _ := runSingleCommand(client, fmt.Sprintf("%ssystemd-cgtop -b -n 1", cmdPrefix))
		var stats []map[string]interface{}
		scannerCg := bufio.NewScanner(strings.NewReader(cgOut))
		for scannerCg.Scan() {
			line := scannerCg.Text()
			if strings.HasPrefix(line, "Path") || line == "" {
				continue
			}
			fields := strings.Fields(line)
			// Format: Path Tasks %CPU Memory Input/s Output/s
			if len(fields) >= 1 {
				path := fields[0]
				if !strings.HasSuffix(path, ".service") {
					continue
				}
				name := filepath.Base(path)
				cpu := "0%"
				mem := "0B"
				if len(fields) >= 3 && fields[2] != "-" {
					cpu = fields[2] + "%"
				}
				if len(fields) >= 4 && fields[3] != "-" {
					mem = fields[3]
				}
				pid := pidMap[name]
				if pid == "" {
					pid = "-"
				}
				stats = append(stats, map[string]interface{}{"name": name, "pid": pid, "cpu": cpu, "mem": mem})
			}
		}
		sendToHQ("svc_stats", hostID, termID, stats)

	case "delete_service":
		svc, _ := payload["service"].(string)
		runSingleCommand(client, fmt.Sprintf("%ssystemctl stop %s", cmdPrefix, svc))
		runSingleCommand(client, fmt.Sprintf("%ssystemctl disable %s", cmdPrefix, svc))
		pathCmd := fmt.Sprintf("systemctl show -p FragmentPath %s | cut -d= -f2", svc)
		path, _ := runSingleCommand(client, pathCmd)
		path = strings.TrimSpace(path)
		if path != "" && path != "/dev/null" {
			runSingleCommand(client, fmt.Sprintf("%srm \"%s\"", cmdPrefix, path))
		}
		runSingleCommand(client, cmdPrefix+"systemctl daemon-reload")
		sendToHQ("svc_op_res", hostID, termID, map[string]string{"status": "success"})

	case "cron_list":
		asRoot, _ := payload["as_root"].(bool)
		c := "crontab -l"
		if asRoot {
			c = cmdPrefix + "crontab -l"
		}
		out, err := runSingleCommand(client, c)
		if err != nil && (strings.Contains(strings.ToLower(out), "no crontab") || strings.Contains(strings.ToLower(err.Error()), "exit status")) {
			out = ""
		}
		sendToHQ("cron_list", hostID, termID, map[string]interface{}{"payload": out, "is_root": asRoot})

	case "cron_save":
		content, _ := payload["content"].(string)
		asRoot, _ := payload["as_root"].(bool)
		b64 := base64.StdEncoding.EncodeToString([]byte(content))

		// Usa file temporaneo per evitare conflitti di stdin con sudo
		tmpFile := fmt.Sprintf("/tmp/cron_save_%d", time.Now().UnixNano())
		_, err := runSingleCommand(client, fmt.Sprintf("echo '%s' | base64 -d > %s", b64, tmpFile))
		if err != nil {
			sendToHQ("svc_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Write temp failed: " + err.Error()})
			return
		}

		var cmd string
		if asRoot {
			cmd = fmt.Sprintf("%scrontab %s", cmdPrefix, tmpFile)
		} else {
			cmd = fmt.Sprintf("crontab %s", tmpFile)
		}

		out, err := runSingleCommand(client, cmd)
		runSingleCommand(client, "rm "+tmpFile) // Cleanup

		if err != nil {
			sendToHQ("svc_op_res", hostID, termID, map[string]string{"status": "error", "msg": out})
		} else {
			sendToHQ("svc_op_res", hostID, termID, map[string]string{"status": "success"})
		}

	case "rc_read":
		check, _ := runSingleCommand(client, "[ -f /etc/rc.local ] && echo 'yes' || echo 'no'")
		exists := strings.TrimSpace(check) == "yes"
		content := ""
		if exists {
			content, _ = runSingleCommand(client, "cat /etc/rc.local")
		}
		sendToHQ("rclocal_content", hostID, termID, map[string]interface{}{"content": content, "exists": exists})

	case "rc_save":
		content, _ := payload["content"].(string)
		b64 := base64.StdEncoding.EncodeToString([]byte(content))
		runSingleCommand(client, fmt.Sprintf("echo '%s' | base64 -d | %stee /etc/rc.local", b64, cmdPrefix))
		runSingleCommand(client, cmdPrefix+"chmod +x /etc/rc.local")
		sendToHQ("svc_op_res", hostID, termID, map[string]string{"status": "success"})

	case "rc_delete":
		runSingleCommand(client, cmdPrefix+"rm /etc/rc.local")
		sendToHQ("svc_op_res", hostID, termID, map[string]string{"status": "success"})
	}
}

// --- LOG VIEWER ---
func handleLogCommand(hostID int, termID string, payload map[string]interface{}) {
	sess, err := ensureSession(hostID, GetSessionIDWithPrefix(termID, "main"))
	if err != nil {
		return
	}
	client := sess.Client
	safePass := strings.ReplaceAll(sess.Password, "'", "'\\''")
	cmdPrefix := fmt.Sprintf("echo '%s' | sudo -S -p '' ", safePass)

	action, _ := payload["action"].(string)

	switch action {
	case "get_journal":
		lines, _ := payload["lines"].(string)
		if lines == "" {
			lines = "100"
		}
		prio, _ := payload["prio"].(string)
		unit, _ := payload["unit"].(string)
		boot, _ := payload["boot"].(string)
		cmd := fmt.Sprintf("%sjournalctl -n %s --no-pager", cmdPrefix, lines)
		if prio != "" {
			cmd += " -p " + prio
		}
		if unit != "" {
			cmd += " -u " + unit
		}
		if boot != "" && boot != "0" {
			cmd += " -b " + boot
		}
		out, _ := runSingleCommand(client, cmd)
		sendToHQ("log_journal", hostID, termID, out)

	case "list_files":
		// Usa stat per ottenere nome e dimensione in bytes separati da pipe, gestisce spazi nei nomi
		cmd := fmt.Sprintf("%sfind /var/log -maxdepth 3 -type f -exec stat -c '%%n|%%s' {} \\;", cmdPrefix)
		out, _ := runSingleCommand(client, cmd)
		var files []LogFileItem
		scanner := bufio.NewScanner(strings.NewReader(out))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			parts := strings.Split(line, "|")
			if len(parts) >= 2 {
				sizeBytes, _ := strconv.ParseInt(parts[1], 10, 64)
				sizeStr := formatBytes(sizeBytes)
				files = append(files, LogFileItem{Path: parts[0], Size: sizeStr})
			}
		}
		sendToHQ("log_file_list", hostID, termID, files)

	case "read_file":
		path, _ := payload["path"].(string)
		lines, _ := payload["lines"].(string)
		if lines == "" {
			lines = "500"
		}
		if !strings.HasPrefix(path, "/var/log") {
			return
		}
		cmd := ""
		if strings.HasSuffix(path, ".gz") {
			cmd = fmt.Sprintf("%szcat %s | tail -n %s", cmdPrefix, path, lines)
		} else {
			cmd = fmt.Sprintf("%stail -n %s %s", cmdPrefix, lines, path)
		}
		out, err := runSingleCommand(client, cmd)
		if err != nil {
			sendToHQ("log_op_res", hostID, termID, map[string]string{"status": "error", "msg": err.Error()})
		} else {
			sendToHQ("log_file_content", hostID, termID, map[string]string{"filename": path, "content": out})
		}

	case "stream_start":
		path, _ := payload["path"].(string)
		wsID := GetWorkspaceID(termID)
		stopStream(hostID, wsID, termID)
		cmd := fmt.Sprintf("%stail -f -n 10 %s", cmdPrefix, path)
		go startStream(hostID, wsID, termID, cmd, "log_stream_data", client)

	case "stream_stop":
		wsID := GetWorkspaceID(termID)
		stopStream(hostID, wsID, termID)
	}
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

// --- APACHE2 ---
type ApacheSite struct {
	File    string `json:"file"`
	Domain  string `json:"domain"`
	Port    string `json:"port"`
	Root    string `json:"root"`
	Enabled bool   `json:"enabled"`
}

type ApacheModule struct {
	Name    string `json:"name"`
	File    string `json:"file"`
	Enabled bool   `json:"enabled"`
}

func handleApacheCommand(hostID int, termID string, payload map[string]interface{}) {
	log.Printf("🔥 [APACHE] Inizio comando per HostID: %d", hostID)

	sess, err := ensureSession(hostID, GetSessionIDWithPrefix(termID, "main"))
	if err != nil {
		log.Printf("❌ [APACHE] Errore sessione SSH per HostID %d: %v", hostID, err)
		// Dobbiamo SEMPRE rispondere al frontend per sbloccare il LOADING
		sendToHQ("apache_status", hostID, termID, map[string]interface{}{
			"active":         false,
			"version":        "Errore SSH",
			"pid":            "-",
			"uptime":         "-",
			"active_sites":   "0",
			"active_modules": "0",
		})
		return
	}

	client := sess.Client
	cmdPrefix := fmt.Sprintf("echo '%s' | sudo -S -p '' ", sess.Password)

	action, _ := payload["action"].(string)
	log.Printf("🔥 [APACHE] Azione richiesta: %s", action)

	switch action {
	case "get_status":
		// Controlla se è attivo (supporta sia apache2 che httpd per CentOS/RHEL)
		active, _ := runSingleCommand(client, "systemctl is-active apache2 || systemctl is-active httpd")
		isActive := strings.TrimSpace(active) == "active"

		// Version parsing robusto
		verRaw, _ := runSingleCommand(client, fmt.Sprintf("%sapache2 -v 2>&1 || %shttpd -v 2>&1", cmdPrefix, cmdPrefix))
		version := strings.TrimSpace(verRaw)
		if idx := strings.Index(version, "Server version: "); idx != -1 {
			version = version[idx+16:]
			if slash := strings.Index(version, "/"); slash != -1 {
				version = version[slash+1:]
			}
			if space := strings.Index(version, " "); space != -1 {
				version = version[:space]
			}
		}

		pid, _ := runSingleCommand(client, "pgrep -x apache2 | head -n 1 || pgrep -x httpd | head -n 1")
		cleanPid := strings.TrimSpace(pid)

		uptime := "-"
		if cleanPid != "" {
			up, _ := runSingleCommand(client, fmt.Sprintf("ps -p %s -o etime= 2>/dev/null", cleanPid))
			uptime = strings.TrimSpace(up)
		}

		sitesEnabled, _ := runSingleCommand(client, cmdPrefix+"ls -1 /etc/apache2/sites-enabled/ 2>/dev/null | wc -l")
		modsEnabled, _ := runSingleCommand(client, cmdPrefix+"ls -1 /etc/apache2/mods-enabled/ 2>/dev/null | grep '\\.load$' | wc -l")

		sendToHQ("apache_status", hostID, termID, map[string]interface{}{
			"active":         isActive,
			"version":        strings.TrimSpace(version),
			"pid":            cleanPid,
			"uptime":         uptime,
			"active_sites":   strings.TrimSpace(sitesEnabled),
			"active_modules": strings.TrimSpace(modsEnabled),
		})

		if isActive {
			go apacheStreamLogChunk(hostID, termID, "/var/log/apache2/access.log", "access", client, cmdPrefix)
			go apacheStreamLogChunk(hostID, termID, "/var/log/apache2/error.log", "error", client, cmdPrefix)
		}
	case "service_op":
		op, _ := payload["op"].(string)
		cmd := "systemctl " + op + " apache2"
		if op == "test" {
			cmd = "apache2ctl configtest"
		}
		out, err := runSingleCommand(client, cmdPrefix+cmd)
		st := "success"
		if err != nil {
			st = "error"
		}
		sendToHQ("apache_op_res", hostID, termID, map[string]string{"status": st, "msg": out})
	case "list_sites":
		// Available
		outAvail, _ := runSingleCommand(client, cmdPrefix+"ls -1 /etc/apache2/sites-available/")
		files := strings.Fields(outAvail)
		var sitesAvail []ApacheSite
		for _, f := range files {
			c, _ := runSingleCommand(client, fmt.Sprintf("%scat /etc/apache2/sites-available/%s", cmdPrefix, f))
			// Check enabled
			checkEn, _ := runSingleCommand(client, fmt.Sprintf("%s[ -e /etc/apache2/sites-enabled/%s ] && echo yes", cmdPrefix, f))
			isEnabled := strings.TrimSpace(checkEn) == "yes"
			sitesAvail = append(sitesAvail, ApacheSite{File: f, Domain: extractApacheVal(c, "ServerName"), Port: extractApacheVal(c, "<VirtualHost"), Root: extractApacheVal(c, "DocumentRoot"), Enabled: isEnabled})
		}

		// Enabled
		outEn, _ := runSingleCommand(client, cmdPrefix+"ls -1 /etc/apache2/sites-enabled/")
		filesEn := strings.Fields(outEn)
		var sitesEn []ApacheSite
		for _, f := range filesEn {
			c, _ := runSingleCommand(client, fmt.Sprintf("%scat /etc/apache2/sites-enabled/%s", cmdPrefix, f))
			sitesEn = append(sitesEn, ApacheSite{File: f, Domain: extractApacheVal(c, "ServerName"), Port: extractApacheVal(c, "<VirtualHost"), Root: extractApacheVal(c, "DocumentRoot"), Enabled: true})
		}

		sendToHQ("apache_sites_list", hostID, termID, map[string]interface{}{"available": sitesAvail, "enabled": sitesEn})

	case "enable_site":
		f, _ := payload["file"].(string)
		runSingleCommand(client, fmt.Sprintf("%sa2ensite %s", cmdPrefix, f))
		runSingleCommand(client, cmdPrefix+"systemctl reload apache2")
		sendToHQ("apache_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Enabled"})
	case "disable_site":
		f, _ := payload["file"].(string)
		runSingleCommand(client, fmt.Sprintf("%sa2dissite %s", cmdPrefix, f))
		runSingleCommand(client, cmdPrefix+"systemctl reload apache2")
		sendToHQ("apache_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Disabled"})

	case "delete_site":
		f, _ := payload["file"].(string)
		runSingleCommand(client, fmt.Sprintf("%sa2dissite %s", cmdPrefix, f))
		runSingleCommand(client, fmt.Sprintf("%srm /etc/apache2/sites-available/%s", cmdPrefix, f))
		runSingleCommand(client, cmdPrefix+"systemctl reload apache2")
		sendToHQ("apache_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Deleted"})

	case "create_site":
		d, _ := payload["domain"].(string)
		port, _ := payload["port"].(string)
		root, _ := payload["root"].(string)
		tpl, _ := payload["template"].(string)
		px, _ := payload["proxy_target"].(string)
		en, _ := payload["enable_now"].(bool)

		cfg := fmt.Sprintf("<VirtualHost *:%s>\n    ServerName %s\n    ServerAdmin webmaster@localhost\n", port, d)
		if tpl == "proxy" {
			cfg += fmt.Sprintf("    ProxyPreserveHost On\n    ProxyPass / %s\n    ProxyPassReverse / %s\n", px, px)
		} else {
			cfg += fmt.Sprintf("    DocumentRoot %s\n    <Directory %s>\n        Options Indexes FollowSymLinks\n        AllowOverride All\n        Require all granted\n    </Directory>\n", root, root)
		}
		cfg += fmt.Sprintf("    ErrorLog ${APACHE_LOG_DIR}/error.log\n    CustomLog ${APACHE_LOG_DIR}/access.log combined\n</VirtualHost>\n")

		b64 := base64.StdEncoding.EncodeToString([]byte(cfg))
		tmpFile := fmt.Sprintf("/tmp/apache_create_%d", time.Now().UnixNano())
		runSingleCommand(client, fmt.Sprintf("echo '%s' | base64 -d > %s", b64, tmpFile))
		runSingleCommand(client, fmt.Sprintf("%smv %s /etc/apache2/sites-available/%s.conf", cmdPrefix, tmpFile, d))

		if en {
			runSingleCommand(client, fmt.Sprintf("%sa2ensite %s.conf", cmdPrefix, d))
			runSingleCommand(client, cmdPrefix+"systemctl reload apache2")
		}
		sendToHQ("apache_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Created"})

	case "list_config_files":
		out, _ := runSingleCommand(client, fmt.Sprintf("%sfind /etc/apache2 -maxdepth 3 -type f \\( -name '*.conf' -o -path '*/sites-available/*' -o -path '*/sites-enabled/*' \\)", cmdPrefix))
		sendToHQ("apache_file_list", hostID, termID, strings.Fields(out))

	case "read_config":
		f, _ := payload["file"].(string)
		if !strings.HasPrefix(f, "/") {
			f = "/etc/apache2/" + f
		}
		out, _ := runSingleCommand(client, cmdPrefix+"cat "+f)
		sendToHQ("apache_file_content", hostID, termID, out)

	case "save_config":
		f, _ := payload["file"].(string)
		c, _ := payload["content"].(string)
		if !strings.HasPrefix(f, "/") {
			f = "/etc/apache2/" + f
		}
		b64 := base64.StdEncoding.EncodeToString([]byte(c))
		tmpFile := fmt.Sprintf("/tmp/apache_save_%d", time.Now().UnixNano())
		runSingleCommand(client, fmt.Sprintf("echo '%s' | base64 -d > %s", b64, tmpFile))

		mvOut, mvErr := runSingleCommand(client, fmt.Sprintf("%smv %s %s", cmdPrefix, tmpFile, f))
		if mvErr != nil {
			sendToHQ("apache_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Save failed (mv): " + mvOut})
			return
		}

		out, err := runSingleCommand(client, fmt.Sprintf("%sapache2ctl configtest", cmdPrefix))
		if err == nil {
			runSingleCommand(client, cmdPrefix+"systemctl reload apache2")
			sendToHQ("apache_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Saved"})
		} else {
			sendToHQ("apache_op_res", hostID, termID, map[string]string{"status": "error", "msg": out})
		}

	case "list_logs":
		out, _ := runSingleCommand(client, fmt.Sprintf("%sLC_ALL=C ls -1 /var/log/apache2/", cmdPrefix))
		sendToHQ("apache_log_list", hostID, termID, out)

	case "list_modules":
		// List available .load files
		outAvail, _ := runSingleCommand(client, fmt.Sprintf("%sfind /etc/apache2/mods-available -name '*.load'", cmdPrefix))
		// List enabled .load files
		outEn, _ := runSingleCommand(client, fmt.Sprintf("%sfind /etc/apache2/mods-enabled -name '*.load'", cmdPrefix))

		enabledMap := make(map[string]bool)
		for _, path := range strings.Fields(outEn) {
			filename := filepath.Base(path)
			modName := strings.TrimSuffix(filename, ".load")
			enabledMap[modName] = true
		}

		var modules []ApacheModule
		for _, path := range strings.Fields(outAvail) {
			filename := filepath.Base(path)
			modName := strings.TrimSuffix(filename, ".load")
			modules = append(modules, ApacheModule{
				Name:    modName,
				File:    filename,
				Enabled: enabledMap[modName],
			})
		}
		sendToHQ("apache_modules_list", hostID, termID, modules)

	case "enable_module":
		mod, _ := payload["module"].(string)
		runSingleCommand(client, fmt.Sprintf("%sa2enmod %s", cmdPrefix, mod))
		runSingleCommand(client, cmdPrefix+"systemctl reload apache2")
		sendToHQ("apache_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Module enabled"})

	case "disable_module":
		mod, _ := payload["module"].(string)
		runSingleCommand(client, fmt.Sprintf("%sa2dismod %s", cmdPrefix, mod))
		runSingleCommand(client, cmdPrefix+"systemctl reload apache2")
		sendToHQ("apache_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Module disabled"})
	}
}

// Estrae valori dalla sintassi apache (es. "ServerName example.com" o "<VirtualHost *:80>")
func extractApacheVal(content, key string) string {
	lines := strings.Split(content, "\n")
	for _, l := range lines {
		l = strings.TrimSpace(l)
		parts := strings.Fields(l)
		if len(parts) >= 2 && strings.EqualFold(parts[0], key) {
			val := strings.TrimSpace(parts[1])
			// Se è un blocco VirtualHost pulisce i caratteri superflui
			if strings.EqualFold(key, "<VirtualHost") {
				val = strings.TrimSuffix(val, ">")
				if strings.Contains(val, ":") {
					val = strings.Split(val, ":")[1]
				}
			}
			return val
		}
	}
	return "-"
}

// Log streaming specifico per apache per inviare il tipo "apache_log_chunk"
func apacheStreamLogChunk(hostID int, termID string, path, logType string, client *ssh.Client, prefix string) {
	out, _ := runSingleCommand(client, fmt.Sprintf("%stail -n 1 %s 2>/dev/null", prefix, path))
	if out != "" {
		sendToHQ("apache_log_chunk", hostID, termID, map[string]string{"type": logType, "line": strings.TrimSpace(out)})
	}
}

// --- NGINX ---
func handleNginxCommand(hostID int, termID string, payload map[string]interface{}) {
	sess, err := ensureSession(hostID, GetSessionIDWithPrefix(termID, "main"))
	if err != nil {
		// Invia uno stato di errore per sbloccare la UI
		sendToHQ("nginx_status", hostID, termID, map[string]interface{}{
			"active":       false,
			"version":      "Connection Error: " + err.Error(),
			"pid":          "-",
			"uptime":       "-",
			"active_sites": "0",
			"connections":  "0",
		})
		return
	}
	client := sess.Client
	cmdPrefix := fmt.Sprintf("echo '%s' | sudo -S -p '' ", sess.Password)

	action, _ := payload["action"].(string)

	switch action {
	case "get_status":
		active, _ := runSingleCommand(client, "systemctl is-active nginx")
		// Usa cmdPrefix (sudo) per garantire l'accesso al comando nginx e parsa l'output
		verRaw, _ := runSingleCommand(client, fmt.Sprintf("%snginx -v 2>&1", cmdPrefix))
		version := strings.TrimSpace(verRaw)
		// Output tipico: "nginx version: nginx/1.18.0 (Ubuntu)"
		if idx := strings.Index(version, "/"); idx != -1 {
			version = version[idx+1:]
			if spaceIdx := strings.Index(version, " "); spaceIdx != -1 {
				version = version[:spaceIdx]
			}
		}

		pid, _ := runSingleCommand(client, "pgrep -x nginx | head -n 1")
		uptime, _ := runSingleCommand(client, "ps -p $(pgrep -x nginx | head -n 1) -o etime=")
		sitesEnabled, _ := runSingleCommand(client, cmdPrefix+"ls -1 /etc/nginx/sites-enabled/ | wc -l")
		conns, _ := runSingleCommand(client, "netstat -an | grep :80 | grep ESTABLISHED | wc -l")
		isActive := strings.TrimSpace(active) == "active"
		sendToHQ("nginx_status", hostID, termID, map[string]interface{}{"active": isActive, "version": version, "pid": strings.TrimSpace(pid), "uptime": strings.TrimSpace(uptime), "active_sites": strings.TrimSpace(sitesEnabled), "connections": strings.TrimSpace(conns)})
		if isActive {
			go streamLogChunk(hostID, termID, "/var/log/nginx/access.log", "access", client, cmdPrefix)
			go streamLogChunk(hostID, termID, "/var/log/nginx/error.log", "error", client, cmdPrefix)
		}
	case "service_op":
		op, _ := payload["op"].(string)
		cmd := "systemctl " + op + " nginx"
		if op == "test" {
			cmd = "nginx -t"
		}
		out, err := runSingleCommand(client, cmdPrefix+cmd)
		st := "success"
		if err != nil {
			st = "error"
		}
		sendToHQ("nginx_op_res", hostID, termID, map[string]string{"status": st, "msg": out})
	case "list_sites":
		// Available
		outAvail, _ := runSingleCommand(client, cmdPrefix+"ls -1 /etc/nginx/sites-available/")
		files := strings.Fields(outAvail)
		var sitesAvail []NginxSite
		for _, f := range files {
			c, _ := runSingleCommand(client, fmt.Sprintf("%scat /etc/nginx/sites-available/%s", cmdPrefix, f))
			// Check if enabled (symlink exists in sites-enabled)
			checkEn, _ := runSingleCommand(client, fmt.Sprintf("%s[ -e /etc/nginx/sites-enabled/%s ] && echo yes", cmdPrefix, f))
			isEnabled := strings.TrimSpace(checkEn) == "yes"
			sitesAvail = append(sitesAvail, NginxSite{File: f, Domain: extractNginxVal(c, "server_name"), Port: extractNginxVal(c, "listen"), Root: extractNginxVal(c, "root"), Enabled: isEnabled})
		}

		// Enabled (files directly in enabled or symlinks)
		outEn, _ := runSingleCommand(client, cmdPrefix+"ls -1 /etc/nginx/sites-enabled/")
		filesEn := strings.Fields(outEn)
		var sitesEn []NginxSite
		for _, f := range filesEn {
			c, _ := runSingleCommand(client, fmt.Sprintf("%scat /etc/nginx/sites-enabled/%s", cmdPrefix, f))
			sitesEn = append(sitesEn, NginxSite{File: f, Domain: extractNginxVal(c, "server_name"), Port: extractNginxVal(c, "listen"), Root: extractNginxVal(c, "root"), Enabled: true})
		}

		sendToHQ("nginx_sites_list", hostID, termID, map[string]interface{}{"available": sitesAvail, "enabled": sitesEn})

	case "enable_site":
		f, _ := payload["file"].(string)
		runSingleCommand(client, fmt.Sprintf("%sln -s /etc/nginx/sites-available/%s /etc/nginx/sites-enabled/", cmdPrefix, f))
		runSingleCommand(client, cmdPrefix+"systemctl reload nginx")
		sendToHQ("nginx_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Enabled"})
	case "disable_site":
		f, _ := payload["file"].(string)
		runSingleCommand(client, fmt.Sprintf("%srm /etc/nginx/sites-enabled/%s", cmdPrefix, f))
		runSingleCommand(client, cmdPrefix+"systemctl reload nginx")
		sendToHQ("nginx_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Disabled"})
	case "delete_site":
		f, _ := payload["file"].(string)
		runSingleCommand(client, fmt.Sprintf("%srm /etc/nginx/sites-enabled/%s", cmdPrefix, f))
		runSingleCommand(client, fmt.Sprintf("%srm /etc/nginx/sites-available/%s", cmdPrefix, f))
		runSingleCommand(client, cmdPrefix+"systemctl reload nginx")
		sendToHQ("nginx_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Deleted"})
	case "create_site":
		d, _ := payload["domain"].(string)
		port, _ := payload["port"].(string)
		root, _ := payload["root"].(string)
		tpl, _ := payload["template"].(string)
		px, _ := payload["proxy_target"].(string)
		en, _ := payload["enable_now"].(bool)
		cfg := fmt.Sprintf("server {\n    listen %s;\n    server_name %s;\n", port, d)
		if tpl == "proxy" {
			cfg += fmt.Sprintf("    location / {\n        proxy_pass %s;\n        proxy_set_header Host $host;\n    }\n", px)
		} else {
			cfg += fmt.Sprintf("    root %s;\n    index index.html;\n    location / {\n        try_files $uri $uri/ =404;\n    }\n", root)
			if tpl == "php" {
				cfg += "\n    location ~ \\.php$ {\n        include snippets/fastcgi-php.conf;\n        fastcgi_pass unix:/var/run/php/php-fpm.sock;\n    }\n"
			}
		}
		cfg += "}\n"
		b64 := base64.StdEncoding.EncodeToString([]byte(cfg))
		tmpFile := fmt.Sprintf("/tmp/nginx_create_%d", time.Now().UnixNano())
		runSingleCommand(client, fmt.Sprintf("echo '%s' | base64 -d > %s", b64, tmpFile))
		runSingleCommand(client, fmt.Sprintf("%smv %s /etc/nginx/sites-available/%s", cmdPrefix, tmpFile, d))
		if en {
			runSingleCommand(client, fmt.Sprintf("%sln -s /etc/nginx/sites-available/%s /etc/nginx/sites-enabled/", cmdPrefix, d))
			runSingleCommand(client, cmdPrefix+"systemctl reload nginx")
		}
		sendToHQ("nginx_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Created"})
	case "list_config_files":
		// List conf files and everything in sites-*
		out, _ := runSingleCommand(client, fmt.Sprintf("%sfind /etc/nginx -maxdepth 3 -type f \\( -name '*.conf' -o -path '*/sites-available/*' -o -path '*/sites-enabled/*' \\)", cmdPrefix))
		sendToHQ("nginx_file_list", hostID, termID, strings.Fields(out))
	case "read_config":
		f, _ := payload["file"].(string)
		if !strings.HasPrefix(f, "/") {
			f = "/etc/nginx/" + f
		}
		out, _ := runSingleCommand(client, fmt.Sprintf("%scat \"%s\"", cmdPrefix, f))
		sendToHQ("nginx_file_content", hostID, termID, out)
	case "save_config":
		f, _ := payload["file"].(string)
		c, _ := payload["content"].(string)
		if !strings.HasPrefix(f, "/") {
			f = "/etc/nginx/" + f
		}
		b64 := base64.StdEncoding.EncodeToString([]byte(c))
		tmpFile := fmt.Sprintf("/tmp/nginx_save_%d", time.Now().UnixNano())
		runSingleCommand(client, fmt.Sprintf("echo '%s' | base64 -d > %s", b64, tmpFile))
		runSingleCommand(client, fmt.Sprintf("%smv %s %s", cmdPrefix, tmpFile, f))
		out, err := runSingleCommand(client, fmt.Sprintf("%snginx -t", cmdPrefix))
		if err == nil {
			runSingleCommand(client, cmdPrefix+"systemctl reload nginx")
			sendToHQ("nginx_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Saved"})
		} else {
			sendToHQ("nginx_op_res", hostID, termID, map[string]string{"status": "error", "msg": out})
		}
	case "list_logs":
		// Use sudo to list logs. Using ls -1 for simpler parsing and LC_ALL=C for standard output.
		out, _ := runSingleCommand(client, fmt.Sprintf("%sLC_ALL=C ls -1 /var/log/nginx/", cmdPrefix))
		sendToHQ("nginx_log_list", hostID, termID, out)
	}
}

func extractNginxVal(content, key string) string {
	lines := strings.Split(content, "\n")
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, key) {
			v := strings.TrimPrefix(l, key)
			return strings.TrimSpace(strings.TrimSuffix(v, ";"))
		}
	}
	return "-"
}

func streamLogChunk(hostID int, termID string, path, logType string, client *ssh.Client, prefix string) {
	out, _ := runSingleCommand(client, fmt.Sprintf("%stail -n 1 %s", prefix, path))
	if out != "" {
		sendToHQ("nginx_log_chunk", hostID, termID, map[string]string{"type": logType, "line": strings.TrimSpace(out)})
	}
}

func handleSystemCommand(hostID int, termID string, payload map[string]interface{}) {
	sess, err := ensureSession(hostID, GetSessionIDWithPrefix(termID, "main"))
	if err != nil {
		log.Println("Error ensuring session for system command:", err)
		sendToHQ("system_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Could not establish SSH session."})
		return
	}
	client := sess.Client
	cmdPrefix := fmt.Sprintf("echo '%s' | sudo -S -p '' ", sess.Password)

	action, _ := payload["action"].(string)
	log.Printf("[SYSTEM-CMD] Received '%s' for host %d", action, hostID)

	var cmd string
	switch action {
	case "shutdown":
		// Using "shutdown -h now" for immediate shutdown.
		// The command is run in the background with '&' so we don't wait for a response that will never come.
		cmd = cmdPrefix + "shutdown -h now &"
	case "reboot":
		cmd = cmdPrefix + "reboot &"
	case "reset":
		// For reset, 'reboot -f' (force) is a good option.
		cmd = cmdPrefix + "reboot -f &"
	default:
		sendToHQ("system_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Unknown system command."})
		return
	}

	// We run the command but don't care about the output, as the machine will likely disconnect.
	runSingleCommand(client, cmd)

	// Send a confirmation back to the client.
	sendToHQ("system_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Command '" + action + "' sent successfully."})
}

// --- SSHD CONFIG ---
func handleSSHCommand(hostID int, termID string, payload map[string]interface{}) {
	sess, err := ensureSession(hostID, GetSessionIDWithPrefix(termID, "main"))
	if err != nil {
		return
	}
	client := sess.Client
	cmdPrefix := fmt.Sprintf("echo '%s' | sudo -S -p '' ", sess.Password)

	action, _ := payload["action"].(string)

	switch action {
	case "get_status":
		active, _ := runSingleCommand(client, "systemctl is-active ssh || systemctl is-active sshd")
		version, _ := runSingleCommand(client, "ssh -V 2>&1")
		pid, _ := runSingleCommand(client, "pgrep sshd")
		logs, _ := runSingleCommand(client, "last -n 20")

		res := map[string]string{
			"active":  strings.TrimSpace(active),
			"version": strings.TrimSpace(version),
			"pid":     strings.TrimSpace(pid),
			"logs":    strings.TrimSpace(logs),
		}
		sendToHQ("ssh_status", hostID, termID, res)

	case "get_config":
		file, _ := payload["file"].(string)
		path := "/etc/ssh/sshd_config"
		if file == "ssh" {
			path = "/etc/ssh/ssh_config"
		}
		content, _ := runSingleCommand(client, cmdPrefix+"cat "+path)
		sendToHQ("ssh_config_data", hostID, termID, map[string]string{"file": file, "content": content})

	case "save_config":
		file, _ := payload["file"].(string)
		content, _ := payload["content"].(string)
		path := "/etc/ssh/sshd_config"
		if file == "ssh" {
			path = "/etc/ssh/ssh_config"
		}

		b64 := base64.StdEncoding.EncodeToString([]byte(content))
		writeCmd := fmt.Sprintf("echo '%s' | base64 -d | %stee %s > /dev/null", b64, cmdPrefix, path)
		_, err := runSingleCommand(client, writeCmd)

		if err != nil {
			sendToHQ("ssh_op_res", hostID, termID, map[string]string{"status": "error", "msg": err.Error()})
		} else {
			if file == "sshd" {
				testOut, testErr := runSingleCommand(client, cmdPrefix+"sshd -t")
				if testErr != nil {
					sendToHQ("ssh_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Config Error: " + testOut})
					return
				}
			}
			sendToHQ("ssh_op_res", hostID, termID, map[string]string{"status": "success"})
		}

	case "service_op":
		op, _ := payload["op"].(string)
		var cmd string
		if op == "toggle" {
			active, _ := runSingleCommand(client, "systemctl is-active ssh || systemctl is-active sshd")
			if strings.TrimSpace(active) == "active" {
				cmd = "stop"
			} else {
				cmd = "start"
			}
		} else {
			cmd = op
		}
		runSingleCommand(client, fmt.Sprintf("%ssystemctl %s ssh 2>/dev/null || %ssystemctl %s sshd", cmdPrefix, cmd, cmdPrefix, cmd))
		sendToHQ("ssh_op_res", hostID, termID, map[string]string{"status": "success"})
	}
}

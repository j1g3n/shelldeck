package bridge

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Process struct {
	PID   int     `json:"pid"`
	PPID  int     `json:"ppid"`
	User  string  `json:"user"`
	Nice  int     `json:"nice"`
	CPU   float64 `json:"cpu"`
	Mem   float64 `json:"mem"`
	Virt  int     `json:"virt"`   // KB
	DiskR uint64  `json:"disk_r"` // Total Bytes Read
	DiskW uint64  `json:"disk_w"` // Total Bytes Written
	Net   bool    `json:"net"`    // Has active network connection
	Start string  `json:"start"`  // Start Time
	Time  string  `json:"time"`   // Elapsed Time
	State string  `json:"state"`
	Comm  string  `json:"comm"`
	Args  string  `json:"args"`
}

type DiskStat struct {
	Name       string `json:"name"`
	ReadSpeed  string `json:"read_speed"`
	WriteSpeed string `json:"write_speed"`
	Total      uint64 `json:"total"`
	Used       uint64 `json:"used"`
	Free       uint64 `json:"free"`
	Perc       int    `json:"perc"`
}

type SysStats struct {
	CPU_Perc   float64    `json:"cpu_perc"`
	CPU_Cores  []float64  `json:"cpu_cores"` // Usage per core
	CPU_Count  int        `json:"cpu_count"`
	CPU_Freq   string     `json:"cpu_freq"` // CPU Frequency
	Mem_Used   int        `json:"mem_used"`
	Mem_Total  int        `json:"mem_total"`
	Mem_Free   int        `json:"mem_free"`
	Mem_Shared int        `json:"mem_shared"`
	Mem_Buff   int        `json:"mem_buff"`
	Mem_Avail  int        `json:"mem_avail"`
	Mem_Perc   float64    `json:"mem_perc"`
	Swp_Used   int        `json:"swp_used"`
	Swp_Total  int        `json:"swp_total"`
	Swp_Perc   float64    `json:"swp_perc"`
	Load_Avg   string     `json:"load_avg"`
	Uptime     string     `json:"uptime"` // NUOVO CAMPO UPTIME
	Zombies    int        `json:"zombies"`
	Procs      int        `json:"procs"`
	Disk_Read  string     `json:"disk_read_speed"`
	Disk_Write string     `json:"disk_write_speed"`
	Disks      []DiskStat `json:"disks"` // Per-disk stats
	Disk_Total uint64     `json:"disk_total"`
	Disk_Free  uint64     `json:"disk_free"`
	Disk_Perc  int        `json:"disk_perc"`
}

var (
	prevDiskRead  = make(map[string]uint64) // key: "ws:host:dev"
	prevDiskWrite = make(map[string]uint64) // key: "ws:host:dev"
	prevDiskTime  = make(map[string]time.Time)
	diskStatsMu   sync.Mutex
)

func handleTaskMgrCommand(hostID int, termID string, payload map[string]interface{}) {
	sess, err := ensureSession(hostID, GetSessionIDWithPrefix(termID, "main"))
	if err != nil {
		sendToHQ("tool_output", hostID, termID, "Connection Error: "+err.Error())
		return
	}
	action, _ := payload["action"].(string)
	cmdPrefix := fmt.Sprintf("echo '%s' | sudo -S -p '' ", sess.Password)
	client := sess.Client

	switch action {
	case "get_all":
		wsID := GetWorkspaceID(termID)
		stats := SysStats{}

		// COMBINED COMMAND: Free, Uptime, DF, DiskStats, CPU Stat (Sample 1 & 2)
		// Eseguiamo tutto in una volta per ridurre RTT
		// Aggiungiamo anche IO stats (grep) e Net stats (ss) per i processi
		// Added: CPU Freq (grep cpuinfo)
		multiCmd := "free -m; echo '###'; uptime; echo '###'; df -P -B1; echo '###'; cat /proc/diskstats; echo '###'; cat /proc/stat; sleep 0.2; cat /proc/stat; echo '###'; grep -H 'bytes' /proc/[0-9]*/io 2>/dev/null; echo '###'; ss -npt 2>/dev/null; echo '###'; freq=$(cat /sys/devices/system/cpu/cpu*/cpufreq/scaling_cur_freq 2>/dev/null | awk '{sum+=$1; n++} END {if(n>0) printf \"%.2f\", sum/n/1000}'); if [ -z \"$freq\" ]; then grep 'cpu MHz' /proc/cpuinfo | head -1 | awk '{print $4}'; else echo \"$freq\"; fi; echo '###'; ls -l /dev/mapper 2>/dev/null; echo '###'; ls -l /dev/md/ 2>/dev/null"
		outMulti, _ := runSingleCommand(client, multiCmd)
		parts := strings.Split(outMulti, "###")

		// 1. MEMORY & SWAP
		if len(parts) > 0 {
			linesFree := strings.Split(strings.TrimSpace(parts[0]), "\n")
			if len(linesFree) >= 3 {
				fieldsMem := strings.Fields(linesFree[1])
				if len(fieldsMem) >= 7 {
					stats.Mem_Total, _ = strconv.Atoi(fieldsMem[1])
					stats.Mem_Used, _ = strconv.Atoi(fieldsMem[2])
					stats.Mem_Free, _ = strconv.Atoi(fieldsMem[3])
					stats.Mem_Shared, _ = strconv.Atoi(fieldsMem[4])
					stats.Mem_Buff, _ = strconv.Atoi(fieldsMem[5])
					stats.Mem_Avail, _ = strconv.Atoi(fieldsMem[6])
					if stats.Mem_Total > 0 {
						stats.Mem_Perc = float64(stats.Mem_Used) / float64(stats.Mem_Total) * 100
					}
				}
				fieldsSwp := strings.Fields(linesFree[2])
				if len(fieldsSwp) >= 3 {
					stats.Swp_Total, _ = strconv.Atoi(fieldsSwp[1])
					stats.Swp_Used, _ = strconv.Atoi(fieldsSwp[2])
					if stats.Swp_Total > 0 {
						stats.Swp_Perc = float64(stats.Swp_Used) / float64(stats.Swp_Total) * 100
					}
				}
			}
		}

		// 2. LOAD & UPTIME
		if len(parts) > 1 {
			outUp := strings.TrimSpace(parts[1])
			if idx := strings.Index(outUp, "load average:"); idx != -1 {
				stats.Load_Avg = strings.TrimSpace(outUp[idx+13:])
			}
			if upIdx := strings.Index(outUp, "up "); upIdx != -1 {
				str := outUp[upIdx+3:]
				userIdx := strings.Index(str, " user")
				if userIdx != -1 {
					str = str[:userIdx]
					lastComma := strings.LastIndex(str, ",")
					if lastComma != -1 {
						stats.Uptime = strings.TrimSpace(str[:lastComma])
					} else {
						stats.Uptime = strings.TrimSpace(str)
					}
				} else {
					parts := strings.Split(str, ",")
					if len(parts) > 0 {
						stats.Uptime = strings.TrimSpace(parts[0])
					}
				}
			}
		}

		// PRE-PROCESSING: LVM MAPPING (Part 8)
		// Costruiamo la mappa dm-X -> FriendlyName prima di processare dischi e df
		dmMap := make(map[string]string)
		if len(parts) > 8 {
			linesMap := strings.Split(strings.TrimSpace(parts[8]), "\n")
			for _, line := range linesMap {
				// lrwxrwxrwx 1 root root 7 Aug 25 10:00 ubuntu--vg-ubuntu--lv -> ../dm-0
				if strings.Contains(line, "->") {
					fields := strings.Fields(line)
					if len(fields) >= 3 {
						target := fields[len(fields)-1] // ../dm-0
						arrow := fields[len(fields)-2]  // ->
						name := fields[len(fields)-3]   // ubuntu--vg-ubuntu--lv

						if arrow == "->" && strings.Contains(target, "dm-") {
							dmID := filepath.Base(target) // dm-0
							dmMap[dmID] = name
						}
					}
				}
			}
		}

		// PRE-PROCESSING: RAID MAPPING (Part 9)
		// Mappa md127 -> FriendlyName (es. md0 o myraid)
		mdMap := make(map[string]string)
		if len(parts) > 9 {
			linesMd := strings.Split(strings.TrimSpace(parts[9]), "\n")
			for _, line := range linesMd {
				if strings.Contains(line, "->") {
					fields := strings.Fields(line)
					if len(fields) >= 3 {
						target := filepath.Base(fields[len(fields)-1]) // ../md127 -> md127
						name := fields[len(fields)-3]                  // myraid
						mdMap[target] = name
					}
				}
			}
		}

		// 3. DISK SPACE (Aggregated per physical disk or LVM)
		diskUsageMap := make(map[string]struct{ total, used, free uint64 })
		if len(parts) > 2 {
			linesDf := strings.Split(strings.TrimSpace(parts[2]), "\n")
			for i, line := range linesDf {
				if i == 0 {
					continue
				} // Skip header
				fields := strings.Fields(line)
				if len(fields) >= 6 {
					fs := fields[0]
					if !strings.HasPrefix(fs, "/dev/") {
						continue
					}

					// Map partition to disk
					base := strings.TrimPrefix(fs, "/dev/")
					diskName := base

					if strings.HasPrefix(base, "mapper/") {
						// LVM: /dev/mapper/vg-lv -> vg-lv
						diskName = strings.TrimPrefix(base, "mapper/")
					} else if strings.HasPrefix(base, "dm-") {
						// LVM raw: /dev/dm-0 -> check map -> vg-lv
						if name, ok := dmMap[base]; ok {
							diskName = name
						}
					} else if strings.HasPrefix(base, "md") {
						// RAID: /dev/md0 -> md0 (don't strip digits)
						// Check if mapped to friendly name
						if name, ok := mdMap[base]; ok {
							diskName = name
						} else {
							diskName = base
						}
					} else if strings.HasPrefix(base, "nvme") || strings.HasPrefix(base, "mmcblk") {
						if idx := strings.LastIndex(base, "p"); idx != -1 {
							diskName = base[:idx]
						}
					} else {
						// Remove trailing digits (sda1 -> sda)
						diskName = strings.TrimRight(base, "0123456789")
					}

					total, _ := strconv.ParseUint(fields[1], 10, 64)
					used, _ := strconv.ParseUint(fields[2], 10, 64)
					free, _ := strconv.ParseUint(fields[3], 10, 64)

					// Aggregate
					cur := diskUsageMap[diskName]
					cur.total += total
					cur.used += used
					cur.free += free
					diskUsageMap[diskName] = cur

					if fields[5] == "/" { // Keep root stats for legacy/summary
						stats.Disk_Total = total
						stats.Disk_Free = free
						if total > 0 {
							stats.Disk_Perc = int((float64(used) / float64(total)) * 100)
						}
					}
				}
			}
		}

		// 4. DISK I/O (Global)
		if len(parts) > 3 {
			linesDisk := strings.Split(strings.TrimSpace(parts[3]), "\n")
			stats.Disks = []DiskStat{}
			var currRead, currWrite uint64

			diskStatsMu.Lock()
			now := time.Now()
			hostKey := fmt.Sprintf("%d:%d", wsID, hostID)

			for _, line := range linesDisk {
				f := strings.Fields(line)
				if len(f) >= 14 {
					dev := f[2]
					// Filter loop, ram, and partitions (prende solo dischi fisici approssimativamente)
					if strings.HasPrefix(dev, "loop") || strings.HasPrefix(dev, "ram") || strings.HasPrefix(dev, "sr") {
						continue
					}
					isPart := false
					if len(dev) > 0 && dev[len(dev)-1] >= '0' && dev[len(dev)-1] <= '9' {
						// Exclude partitions (sda1) but keep base devices (nvme0n1, mmcblk0, dm-0, md0)
						if !strings.HasPrefix(dev, "nvme") && !strings.HasPrefix(dev, "mmcblk") && !strings.HasPrefix(dev, "dm-") && !strings.HasPrefix(dev, "md") {
							isPart = true
						} // sda1
						if strings.Contains(dev, "p") && (strings.HasPrefix(dev, "nvme") || strings.HasPrefix(dev, "mmcblk")) {
							isPart = true
						} // nvme0n1p1
					}
					if isPart {
						continue
					}

					// Resolve LVM name for display and aggregation
					displayName := dev
					if name, ok := dmMap[dev]; ok {
						displayName = name
					}
					if name, ok := mdMap[dev]; ok {
						displayName = name
					}

					r, _ := strconv.ParseUint(f[5], 10, 64) // Sectors read
					w, _ := strconv.ParseUint(f[9], 10, 64) // Sectors written

					// Global Sum
					currRead += r
					currWrite += w

					// Per Disk Stat
					devKey := fmt.Sprintf("%s:%s", hostKey, dev)
					ds := DiskStat{Name: displayName}
					if lastTime, ok := prevDiskTime[hostKey]; ok {
						diff := now.Sub(lastTime).Seconds()
						if diff > 0 {
							ds.ReadSpeed = formatBytes(int64(float64(r-prevDiskRead[devKey])*512/diff)) + "/s"
							ds.WriteSpeed = formatBytes(int64(float64(w-prevDiskWrite[devKey])*512/diff)) + "/s"
						}
					}

					// Attach Space Usage if available
					if usage, ok := diskUsageMap[displayName]; ok {
						ds.Total = usage.total
						ds.Used = usage.used
						ds.Free = usage.free
						if usage.total > 0 {
							ds.Perc = int((float64(usage.used) / float64(usage.total)) * 100)
						}
					}
					stats.Disks = append(stats.Disks, ds)

					// Update prev
					prevDiskRead[devKey] = r
					prevDiskWrite[devKey] = w
				}
			}

			prevDiskTime[hostKey] = now
			diskStatsMu.Unlock()
		}

		// 5. CPU DETAILED
		if len(parts) > 4 {
			outStat := strings.TrimSpace(parts[4])
			statLines := strings.Split(outStat, "\n")

			type cpuSample struct{ idle, total float64 }
			samples1 := make(map[string]cpuSample)
			samples2 := make(map[string]cpuSample)
			parseStatLine := func(line string) (string, cpuSample, bool) {
				fields := strings.Fields(line)
				if len(fields) < 5 || !strings.HasPrefix(fields[0], "cpu") {
					return "", cpuSample{}, false
				}
				name := fields[0]
				var total, idle float64
				for i, v := range fields[1:] {
					val, _ := strconv.ParseFloat(v, 64)
					total += val
					if i == 3 {
						idle = val
					}
				}
				return name, cpuSample{idle, total}, true
			}
			halfIndex := len(statLines) / 2
			for i := 0; i < len(statLines); i++ {
				name, s, ok := parseStatLine(statLines[i])
				if !ok {
					continue
				}
				if i < halfIndex {
					samples1[name] = s
				} else {
					samples2[name] = s
				}
			}
			stats.CPU_Cores = []float64{}
			for name, s2 := range samples2 {
				if s1, ok := samples1[name]; ok {
					deltaTotal := s2.total - s1.total
					deltaIdle := s2.idle - s1.idle
					usage := 0.0
					if deltaTotal > 0 {
						usage = (1.0 - (deltaIdle / deltaTotal)) * 100.0
					}
					if name == "cpu" {
						stats.CPU_Perc = usage
					} else {
						stats.CPU_Cores = append(stats.CPU_Cores, usage)
					}
				}
			}
			stats.CPU_Count = len(stats.CPU_Cores)
		}

		// 6. PROCESS IO STATS (from grep /proc/.../io)
		procIO := make(map[int][2]uint64) // pid -> [read, write]
		if len(parts) > 5 {
			linesIO := strings.Split(strings.TrimSpace(parts[5]), "\n")
			for _, line := range linesIO {
				// Format: /proc/123/io:read_bytes: 100
				if strings.Contains(line, ":") {
					p := strings.Split(line, ":")
					if len(p) >= 3 {
						// Extract PID from /proc/PID/io
						pathParts := strings.Split(p[0], "/")
						if len(pathParts) >= 3 {
							pid, _ := strconv.Atoi(pathParts[2])
							val, _ := strconv.ParseUint(strings.TrimSpace(p[2]), 10, 64)

							stats := procIO[pid]
							if strings.Contains(p[1], "read_bytes") {
								stats[0] = val
							}
							if strings.Contains(p[1], "write_bytes") {
								stats[1] = val
							}
							procIO[pid] = stats
						}
					}
				}
			}
		}

		// 7. PROCESS NET STATS (from ss)
		procNet := make(map[int]bool)
		if len(parts) > 6 {
			// Output ss: users:(("chrome",pid=123,fd=4))
			// Simple parsing: look for pid=...
			ssOut := parts[6]
			for _, token := range strings.Split(ssOut, ",") {
				if strings.HasPrefix(token, "pid=") {
					pid, _ := strconv.Atoi(strings.TrimPrefix(token, "pid="))
					procNet[pid] = true
				}
			}
		}

		// --- PROCESS LIST ---
		// Added etime (elapsed), lstart (start time)
		// lstart format is "Day Mon DD HH:MM:SS YYYY" (5 fields)
		psCmd := "ps -axwwo pid,ppid,user,ni,pcpu,pmem,vsz,stat,etime,lstart,comm,args --sort=-pcpu | head -n 150"
		outPs, _ := runSingleCommand(client, psCmd)

		var procs []Process
		psLines := strings.Split(outPs, "\n")
		for i, line := range psLines {
			if i == 0 || line == "" {
				continue
			}
			f := strings.Fields(line)
			// PID(0) PPID(1) USER(2) NI(3) CPU(4) MEM(5) VSZ(6) STAT(7) ETIME(8) LSTART(9-13) COMM(14) ARGS(15+)
			if len(f) < 15 {
				continue
			}
			p := Process{}
			p.PID, _ = strconv.Atoi(f[0])
			p.PPID, _ = strconv.Atoi(f[1])
			p.User = f[2]
			p.Nice, _ = strconv.Atoi(f[3])
			p.CPU, _ = strconv.ParseFloat(f[4], 64)
			p.Mem, _ = strconv.ParseFloat(f[5], 64)
			p.Virt, _ = strconv.Atoi(f[6]) // VSZ in KB
			p.State = f[7]
			p.Time = f[8] // Elapsed

			// Reconstruct LSTART (5 fields)
			p.Start = fmt.Sprintf("%s %s %s %s", f[9], f[10], f[11], f[12]) // Mon Feb 27 14:00

			p.Comm = f[14]
			p.Args = strings.Join(f[15:], " ")

			// Enrich with IO and Net
			if ioStats, ok := procIO[p.PID]; ok {
				p.DiskR = ioStats[0]
				p.DiskW = ioStats[1]
			}
			if _, ok := procNet[p.PID]; ok {
				p.Net = true
			}

			procs = append(procs, p)

			if strings.HasPrefix(p.State, "Z") {
				stats.Zombies++
			}
		}
		stats.Procs = len(procs)

		// 8. CPU FREQ
		if len(parts) > 7 {
			stats.CPU_Freq = strings.TrimSpace(parts[7]) + " MHz"
		}

		sendToHQ("tm_update", hostID, termID, map[string]interface{}{
			"stats": stats,
			"procs": procs,
		})

	case "exec_raw":
		cmd, _ := payload["cmd"].(string)
		if !strings.HasPrefix(cmd, "kill") && !strings.HasPrefix(cmd, "renice") && !strings.HasPrefix(cmd, "ionice") {
			sendToHQ("tm_op_res", hostID, termID, map[string]string{"status": "error", "msg": "Comando non consentito"})
			return
		}
		out, err := runSingleCommand(client, cmdPrefix+cmd)
		if err != nil {
			sendToHQ("tm_op_res", hostID, termID, map[string]string{"status": "error", "msg": out})
		} else {
			sendToHQ("tm_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Eseguito"})
		}

	case "get_details":
		var pid string
		if p, ok := payload["pid"].(float64); ok {
			pid = strconv.Itoa(int(p))
		} else {
			pid = fmt.Sprintf("%v", payload["pid"])
		}
		dtype, _ := payload["type"].(string)

		var cmd string
		if dtype == "files" {
			cmd = fmt.Sprintf("%slsof -n -p %s", cmdPrefix, pid)
		} else if dtype == "net" {
			cmd = fmt.Sprintf("%slsof -n -i -a -p %s", cmdPrefix, pid)
		} else if dtype == "env" {
			cmd = fmt.Sprintf("%scat /proc/%s/environ | tr '\\0' '\\n'", cmdPrefix, pid)
		}

		out, _ := runSingleCommand(client, cmd)
		sendToHQ("tm_details", hostID, termID, out)
	}
}

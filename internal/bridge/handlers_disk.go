package bridge

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

func handleDiskCommand(hostID int, termID string, payload map[string]interface{}) {
	sess, err := ensureSession(hostID, GetSessionIDWithPrefix(termID, "main"))
	if err != nil {
		sendToHQ("tool_output", hostID, termID, "Connection Error: "+err.Error())
		return
	}
	action, _ := payload["action"].(string)
	cmdPrefix := fmt.Sprintf("echo '%s' | sudo -S -p '' ", sess.Password)
	client := sess.Client

	switch action {
	case "get_full_info":
		// 1. LSBLK (JSON) - Info dettagliate su dischi e partizioni
		// Aggiunte START (per calcoli resize), FSUSED/FSUSE% (usage), PARTFLAGS (flags)
		// TENTATIVO 1: Colonne estese (util-linux recente)
		lsblkCmd := fmt.Sprintf("%slsblk -J -b -o NAME,SIZE,TYPE,FSTYPE,MOUNTPOINT,LABEL,UUID,PTTYPE,MODEL,SERIAL,START,FSUSED,FSUSE%%,PARTFLAGS", cmdPrefix)
		lsblkOut, err := runSingleCommand(client, lsblkCmd)
		// TENTATIVO 2: Fallback colonne base (util-linux vecchio) se il primo fallisce o non ritorna JSON
		if err != nil || !strings.Contains(lsblkOut, "blockdevices") {
			lsblkCmd = fmt.Sprintf("%slsblk -J -b -o NAME,SIZE,TYPE,FSTYPE,MOUNTPOINT,LABEL,UUID,PTTYPE,MODEL,SERIAL", cmdPrefix)
			lsblkOut, err = runSingleCommand(client, lsblkCmd)
		}

		// Pulizia output JSON (rimuove eventuali warning sudo prima del JSON)
		if idx := strings.Index(lsblkOut, "{"); idx > -1 {
			lsblkOut = lsblkOut[idx:]
		} else {
			// Se non trova JSON, forza errore per il frontend
			lsblkOut = ""
		}

		// 2. RAID Info (mdadm)
		mdstatOut, _ := runSingleCommand(client, "cat /proc/mdstat")
		mdDetailOut, _ := runSingleCommand(client, cmdPrefix+"mdadm --detail --scan --verbose")

		// Parse mdstat for structured data
		raidArrays := parseMdstat(mdstatOut)

		// 3. LVM Info
		pvsCmd := fmt.Sprintf("%spvs --noheadings --nosuffix --units b --separator ';' -o pv_name,vg_name,pv_size,pv_free,pv_used", cmdPrefix)
		pvsOut, _ := runSingleCommand(client, pvsCmd)

		vgsCmd := fmt.Sprintf("%svgs --noheadings --nosuffix --units b --separator ';' -o vg_name,vg_size,vg_free,lv_count,vg_uuid", cmdPrefix)
		vgsOut, _ := runSingleCommand(client, vgsCmd)

		lvsCmd := fmt.Sprintf("%slvs --noheadings --nosuffix --units b --separator ';' -o lv_name,vg_name,lv_size,lv_attr,pool_lv,origin,move_pv,copy_percent,convert_lv", cmdPrefix)
		lvsOut, _ := runSingleCommand(client, lvsCmd)

		var lsblkData interface{}
		if err == nil && strings.HasPrefix(strings.TrimSpace(lsblkOut), "{") {
			json.Unmarshal([]byte(lsblkOut), &lsblkData)
		} else {
			lsblkData = map[string]string{"error": "lsblk failed: " + lsblkOut}
		}

		sendToHQ("disk_full_info", hostID, termID, map[string]interface{}{
			"lsblk": lsblkData,
			"raid": map[string]interface{}{
				"mdstat": mdstatOut,
				"detail": mdDetailOut,
				"arrays": raidArrays,
			},
			"lvm": map[string]interface{}{
				"pvs": parseLVMOutput(pvsOut, []string{"name", "vg_name", "size", "free", "used"}),
				"vgs": parseLVMOutput(vgsOut, []string{"name", "size", "free", "lv_count", "uuid"}),
				"lvs": parseLVMOutput(lvsOut, []string{"name", "vg_name", "size", "attr", "pool", "origin", "move", "copy", "convert"}),
			},
		})

	case "create_swap":
		path, _ := payload["path"].(string)
		size, _ := payload["size"].(string)
		cmd := fmt.Sprintf("%sdd if=/dev/zero of=%s bs=1M count=%s status=none && %schmod 600 %s && %smkswap %s && %sswapon %s",
			cmdPrefix, path, size, cmdPrefix, path, cmdPrefix, path, cmdPrefix, path)
		out, err := runSingleCommand(client, cmd)
		if err != nil {
			sendToHQ("disk_op_res", hostID, termID, map[string]string{"status": "error", "msg": out})
		} else {
			sendToHQ("disk_op_res", hostID, termID, map[string]string{"status": "success"})
		}

	case "swap_off":
		path, _ := payload["path"].(string)
		out, err := runSingleCommand(client, fmt.Sprintf("%sswapoff %s", cmdPrefix, path))
		if err != nil {
			sendToHQ("disk_op_res", hostID, termID, map[string]string{"status": "error", "msg": out})
		} else {
			sendToHQ("disk_op_res", hostID, termID, map[string]string{"status": "success"})
		}

	case "swap_on":
		path, _ := payload["path"].(string)
		out, err := runSingleCommand(client, fmt.Sprintf("%sswapon %s", cmdPrefix, path))
		if err != nil {
			sendToHQ("disk_op_res", hostID, termID, map[string]string{"status": "error", "msg": out})
		} else {
			sendToHQ("disk_op_res", hostID, termID, map[string]string{"status": "success"})
		}

	case "get_swap":
		// 1. Active Swaps
		outSwaps, _ := runSingleCommand(client, "cat /proc/swaps")
		activeSwaps := make(map[string]bool)
		var result []map[string]interface{}

		lines := strings.Split(outSwaps, "\n")
		for i, line := range lines {
			if i == 0 || strings.TrimSpace(line) == "" {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) >= 5 {
				path := fields[0]
				activeSwaps[path] = true
				result = append(result, map[string]interface{}{
					"path":     path,
					"type":     fields[1],
					"size":     fields[2],
					"used":     fields[3],
					"priority": fields[4],
					"active":   true,
				})
			}
		}

		// 2. Configured Swaps (fstab)
		outFstab, _ := runSingleCommand(client, "cat /etc/fstab")
		fstabLines := strings.Split(outFstab, "\n")
		for _, line := range fstabLines {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				dev := fields[0]
				fstype := fields[2]
				if fstype == "swap" {
					if !activeSwaps[dev] {
						result = append(result, map[string]interface{}{
							"path":     dev,
							"type":     "file/partition",
							"size":     "-",
							"used":     "-",
							"priority": "-",
							"active":   false,
						})
						activeSwaps[dev] = true
					}
				}
			}
		}

		sendToHQ("disk_swap", hostID, termID, result)

	case "delete_swap":
		path, _ := payload["path"].(string)
		// Tenta prima di disattivare lo swap (ignora errori se già disattivo)
		runSingleCommand(client, fmt.Sprintf("%sswapoff %s", cmdPrefix, path))
		// Rimuove il file
		out, err := runSingleCommand(client, fmt.Sprintf("%srm -f %s", cmdPrefix, path))
		if err != nil {
			sendToHQ("disk_op_res", hostID, termID, map[string]string{"status": "error", "msg": out})
		} else {
			sendToHQ("disk_op_res", hostID, termID, map[string]string{"status": "success"})
		}

	case "get_ramdisks":
		// Usa df per avere anche l'utilizzo
		out, _ := runSingleCommand(client, "df -hT | grep tmpfs")
		var rams []map[string]string
		lines := strings.Split(out, "\n")
		for _, line := range lines {
			fields := strings.Fields(line)
			if len(fields) >= 7 {
				rams = append(rams, map[string]string{
					"filesystem": fields[0],
					"type":       fields[1],
					"size":       fields[2],
					"used":       fields[3],
					"avail":      fields[4],
					"use":        fields[5],
					"mount":      fields[6],
				})
			}
		}
		sendToHQ("disk_ramdisk", hostID, termID, rams)

	case "create_ramdisk":
		mount, _ := payload["mount"].(string)
		size, _ := payload["size"].(string)
		runSingleCommand(client, fmt.Sprintf("%smkdir -p %s", cmdPrefix, mount))
		out, err := runSingleCommand(client, fmt.Sprintf("%smount -t tmpfs -o size=%s tmpfs %s", cmdPrefix, size, mount))
		if err != nil {
			sendToHQ("disk_op_res", hostID, termID, map[string]string{"status": "error", "msg": out})
		} else {
			sendToHQ("disk_op_res", hostID, termID, map[string]string{"status": "success"})
		}

	case "umount":
		path, _ := payload["path"].(string)
		out, err := runSingleCommand(client, fmt.Sprintf("%sumount %s", cmdPrefix, path))
		if err != nil {
			sendToHQ("disk_op_res", hostID, termID, map[string]string{"status": "error", "msg": out})
		} else {
			sendToHQ("disk_op_res", hostID, termID, map[string]string{"status": "success"})
		}

	case "part_op":
		subAction, _ := payload["sub"].(string)
		dev, _ := payload["device"].(string)

		var cmd string
		switch subAction {
		case "create_table":
			tableType, _ := payload["type"].(string)
			cmd = fmt.Sprintf("%sparted -s %s mklabel %s", cmdPrefix, dev, tableType)
		case "create_part":
			start, _ := payload["start"].(string)
			end, _ := payload["end"].(string)
			partType, _ := payload["type"].(string)
			fsType, _ := payload["fs"].(string)
			if fsType == "" {
				fsType = "ext4"
			}
			cmd = fmt.Sprintf("%sparted -s %s mkpart %s %s %s %s", cmdPrefix, dev, partType, fsType, start, end)
		case "delete_part":
			num, _ := payload["number"].(string)
			cmd = fmt.Sprintf("%sparted -s %s rm %s", cmdPrefix, dev, num)
		case "resize_part":
			num, _ := payload["number"].(string)
			startBytes, _ := payload["start"].(float64) // Start in bytes from lsblk
			currentSizeBytes, _ := payload["current_size"].(float64)
			targetSize, _ := payload["target_size"].(string) // e.g. "20G", "100%"
			partPath, _ := payload["partition"].(string)
			fs, _ := payload["fs"].(string)
			mount, _ := payload["mount"].(string)
			force, _ := payload["force"].(bool)

			// 0. Fix GPT Table (sposta il backup header alla fine del disco se necessario)
			// Tenta prima con sgdisk (più affidabile), poi con parted hack interattivo
			runSingleCommand(client, fmt.Sprintf("%ssgdisk -e %s", cmdPrefix, dev))
			// Hack per parted: invia "Fix" allo stdin per rispondere al prompt di correzione GPT
			// Usa ---pretend-input-tty (flag non documentato ma standard per scripting parted)
			runSingleCommand(client, fmt.Sprintf("%ssh -c 'printf \"Fix\\n\" | parted ---pretend-input-tty %s print'", cmdPrefix, dev))

			// Logica Estendi vs Riduci
			isShrink := false
			isMax := targetSize == "100%" || targetSize == "max"

			// Calcolo nuova fine (End) per parted
			var newEndCmd string
			if isMax {
				newEndCmd = "100%"
			} else {
				// Usa numfmt sul server per convertire targetSize (es. "20G") in bytes
				bytesOut, _ := runSingleCommand(client, fmt.Sprintf("numfmt --from=iec %s", targetSize))
				targetBytesStr := strings.TrimSpace(bytesOut)
				// Se fallisce numfmt, assumiamo sia raw bytes o falliamo
				if targetBytesStr == "" {
					targetBytesStr = targetSize
				}

				// Verifica se è shrink (confronto stringhe approssimativo o logica client side preferibile,
				// ma qui usiamo shell arithmetic per sicurezza)
				checkShrinkCmd := fmt.Sprintf("[ %s -lt %.0f ] && echo yes || echo no", targetBytesStr, currentSizeBytes)
				shrinkCheck, _ := runSingleCommand(client, checkShrinkCmd)
				isShrink = strings.TrimSpace(shrinkCheck) == "yes"

				// Calcola End = Start + TargetSize
				newEndCmd = fmt.Sprintf("$((%.0f + %s))B", startBytes, targetBytesStr)
			}

			if isShrink {
				// SHRINK: 1. Unmount -> 2. Check -> 3. Resize FS -> 4. Resize Part -> 5. Mount
				if strings.HasPrefix(fs, "ext") {
					// Resize2fs richiede unmount per shrink
					cmd = fmt.Sprintf("%sumount %s; ", cmdPrefix, partPath) // Ignora errore se già smontato
					cmd += fmt.Sprintf("%se2fsck -f -p %s && ", cmdPrefix, partPath)
					cmd += fmt.Sprintf("%sresize2fs %s %s && ", cmdPrefix, partPath, targetSize)
					cmd += fmt.Sprintf("%sparted -s %s resizepart %s %s", cmdPrefix, dev, num, newEndCmd)
					if mount != "" {
						cmd += fmt.Sprintf(" && %smount %s %s", cmdPrefix, partPath, mount)
					}
				} else {
					// Altri FS (XFS non supporta shrink, BTRFS supporta online)
					cmd = fmt.Sprintf("echo 'Shrink non supportato o non sicuro per %s via web UI'", fs)
				}
			} else {
				// EXTEND: 1. Resize Part -> 2. Resize FS (Online)
				partedCmd := fmt.Sprintf("%sparted -s %s resizepart %s %s", cmdPrefix, dev, num, newEndCmd)
				if force {
					// Se force è true, usiamo il trick per rispondere "Yes" al prompt interattivo "Partition being used"
					partedCmd = fmt.Sprintf("%ssh -c 'printf \"Yes\\n\" | parted ---pretend-input-tty %s resizepart %s %s'", cmdPrefix, dev, num, newEndCmd)
				}
				cmd = partedCmd

				if strings.Contains(strings.ToLower(fs), "lvm") {
					cmd += fmt.Sprintf(" && %spvresize %s", cmdPrefix, partPath)
				} else if strings.HasPrefix(fs, "ext") {
					cmd += fmt.Sprintf(" && %sresize2fs %s", cmdPrefix, partPath)
				} else if fs == "xfs" && mount != "" {
					cmd += fmt.Sprintf(" && %sxfs_growfs %s", cmdPrefix, mount)
				} else if fs == "btrfs" && mount != "" {
					cmd += fmt.Sprintf(" && %sbtrfs filesystem resize max %s", cmdPrefix, mount)
				}
			}
		case "format":
			part, _ := payload["partition"].(string)
			fs, _ := payload["fs"].(string)
			label, _ := payload["label"].(string)
			switch fs {
			case "swap":
				cmd = fmt.Sprintf("%smkswap %s", cmdPrefix, part)
			case "xfs":
				cmd = fmt.Sprintf("%smkfs.xfs -f %s", cmdPrefix, part)
			case "ntfs":
				cmd = fmt.Sprintf("%smkfs.ntfs -f %s", cmdPrefix, part)
			default:
				cmd = fmt.Sprintf("%smkfs.%s -F %s", cmdPrefix, fs, part)
			}
			if label != "" && fs != "swap" {
				if strings.HasPrefix(fs, "ext") {
					cmd += fmt.Sprintf(" && %se2label %s \"%s\"", cmdPrefix, part, label)
				} else if fs == "xfs" {
					cmd += fmt.Sprintf(" && %sxfs_admin -L \"%s\" %s", cmdPrefix, label, part)
				}
			}
		}
		out, err := runSingleCommand(client, cmd)
		if err != nil {
			sendToHQ("disk_op_res", hostID, termID, map[string]string{"status": "error", "msg": out + " " + err.Error()})
		} else {
			sendToHQ("disk_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Operazione completata"})
		}

	case "fs_op":
		sub, _ := payload["sub"].(string)
		partition, _ := payload["partition"].(string)
		fs, _ := payload["fs"].(string)
		mount, _ := payload["mount"].(string)

		var cmd string
		switch sub {
		case "check":
			// Esegue un check in sola lettura (-n) per sicurezza
			if strings.HasPrefix(fs, "ext") {
				cmd = fmt.Sprintf("%se2fsck -n %s", cmdPrefix, partition)
			} else if fs == "xfs" {
				cmd = fmt.Sprintf("%sxfs_repair -n %s", cmdPrefix, partition)
			} else if fs == "vfat" || fs == "fat" {
				cmd = fmt.Sprintf("%sfsck.vfat -n %s", cmdPrefix, partition)
			} else {
				cmd = "echo 'Check non supportato per questo filesystem via web'"
			}
		case "resize":
			// Resize filesystem (online se supportato)
			if strings.HasPrefix(fs, "ext") {
				cmd = fmt.Sprintf("%sresize2fs %s", cmdPrefix, partition)
			} else if fs == "xfs" && mount != "" {
				cmd = fmt.Sprintf("%sxfs_growfs %s", cmdPrefix, mount)
			} else {
				cmd = "echo 'Resize FS non supportato o richiede mount'"
			}
		}
		out, err := runSingleCommand(client, cmd)
		if err != nil {
			sendToHQ("disk_op_res", hostID, termID, map[string]string{"status": "error", "msg": out + " " + err.Error()})
		} else {
			sendToHQ("disk_op_res", hostID, termID, map[string]string{"status": "success", "msg": out})
		}

	case "raid_op":
		sub, _ := payload["sub"].(string)
		var cmd string
		switch sub {
		case "create":
			name, _ := payload["name"].(string)
			level, _ := payload["level"].(string)
			devices, _ := payload["devices"].(string)
			devCount, _ := payload["dev_count"].(string)
			cmd = fmt.Sprintf("%smdadm --create %s --level=%s --raid-devices=%s %s --force", cmdPrefix, name, level, devCount, devices)
		case "stop":
			dev, _ := payload["device"].(string)
			cmd = fmt.Sprintf("%smdadm --stop %s", cmdPrefix, dev)
		case "remove":
			dev, _ := payload["device"].(string)
			cmd = fmt.Sprintf("%smdadm --remove %s", cmdPrefix, dev)
		case "detail":
			dev, _ := payload["device"].(string)
			cmd = fmt.Sprintf("%smdadm --detail %s", cmdPrefix, dev)
			// Output will be sent as disk_op_res error/msg for simplicity or a new type
		}
		out, err := runSingleCommand(client, cmd)
		if err != nil {
			sendToHQ("disk_op_res", hostID, termID, map[string]string{"status": "error", "msg": out})
		} else {
			sendToHQ("disk_op_res", hostID, termID, map[string]string{"status": "success"})
		}

	case "lvm_op":
		subAction, _ := payload["sub"].(string)
		var cmd string
		switch subAction {
		case "create_pv":
			dev, _ := payload["device"].(string)
			cmd = fmt.Sprintf("%spvcreate -f %s", cmdPrefix, dev)
		case "remove_pv":
			dev, _ := payload["device"].(string)
			cmd = fmt.Sprintf("%spvremove -f %s", cmdPrefix, dev)
		case "create_vg":
			name, _ := payload["name"].(string)
			devs, _ := payload["devices"].(string)
			cmd = fmt.Sprintf("%svgcreate %s %s", cmdPrefix, name, devs)
		case "remove_vg":
			name, _ := payload["name"].(string)
			cmd = fmt.Sprintf("%svgremove -f %s", cmdPrefix, name)
		case "extend_vg":
			name, _ := payload["name"].(string)
			dev, _ := payload["device"].(string)
			cmd = fmt.Sprintf("%svgextend %s %s", cmdPrefix, name, dev)
		case "reduce_vg":
			name, _ := payload["name"].(string)
			dev, _ := payload["device"].(string)
			cmd = fmt.Sprintf("%svgreduce -f %s %s", cmdPrefix, name, dev)
		case "reduce_vg_missing":
			name, _ := payload["name"].(string)
			cmd = fmt.Sprintf("%svgreduce --removemissing --force %s", cmdPrefix, name)
		case "resize_pv":
			dev, _ := payload["device"].(string)
			cmd = fmt.Sprintf("%spvresize %s", cmdPrefix, dev)
		case "create_lv":
			vg, _ := payload["vg"].(string)
			name, _ := payload["name"].(string)
			size, _ := payload["size"].(string)
			cmd = fmt.Sprintf("%slvcreate -L %s -n %s %s", cmdPrefix, size, name, vg)
		case "remove_lv":
			path, _ := payload["path"].(string)
			cmd = fmt.Sprintf("%slvremove -f %s", cmdPrefix, path)
		case "resize_lv":
			path, _ := payload["path"].(string)
			size, _ := payload["size"].(string)

			// Rileva se è una dimensione assoluta (-L) o estensioni/percentuale (-l)
			flag := "-L"
			if strings.Contains(size, "%") {
				flag = "-l"
			}
			// Usa --resizefs per ridimensionare anche il filesystem se supportato
			cmd = fmt.Sprintf("%slvresize --resizefs -L %s %s", cmdPrefix, size, path)
			// Aggiunto --force e -y per rispondere "Yes" ai prompt di unmount/check
			cmd = fmt.Sprintf("%slvresize --resizefs --force -y %s %s %s", cmdPrefix, flag, size, path)
		}
		out, err := runSingleCommand(client, cmd)
		if err != nil {
			sendToHQ("disk_op_res", hostID, termID, map[string]string{"status": "error", "msg": out})
		} else {
			sendToHQ("disk_op_res", hostID, termID, map[string]string{"status": "success"})
		}

	case "mount_dev":
		src, _ := payload["src"].(string)
		dst, _ := payload["dst"].(string)
		typ, _ := payload["type"].(string)
		opts, _ := payload["opts"].(string)
		user, _ := payload["user"].(string)
		pass, _ := payload["pass"].(string)
		isIso, _ := payload["is_iso"].(bool)
		addToFstab, _ := payload["add_fstab"].(bool)

		cmd := fmt.Sprintf("%smount", cmdPrefix)
		if typ != "" {
			cmd += " -t " + typ
		}

		// Gestione Opzioni Extra
		var optList []string
		if opts != "" {
			optList = append(optList, opts)
		}
		if isIso {
			optList = append(optList, "loop")
		}
		if user != "" {
			optList = append(optList, fmt.Sprintf("username=%s", user))
		}
		if pass != "" {
			optList = append(optList, fmt.Sprintf("password=%s", pass))
		}

		finalOpts := strings.Join(optList, ",")
		if finalOpts != "" {
			cmd += " -o " + finalOpts
		}

		cmd += fmt.Sprintf(" %s %s", src, dst)
		runSingleCommand(client, fmt.Sprintf("%smkdir -p %s", cmdPrefix, dst))
		out, err := runSingleCommand(client, cmd)
		if err != nil {
			sendToHQ("disk_op_res", hostID, termID, map[string]string{"status": "error", "msg": out})
		} else {
			sendToHQ("disk_op_res", hostID, termID, map[string]string{"status": "success"})

			// Aggiunta a Fstab se richiesto e se il mount ha avuto successo
			if addToFstab {
				// Formato: device mountpoint type options dump pass
				// Default dump=0 pass=0
				fstabLine := fmt.Sprintf("%s %s %s %s 0 0", src, dst, typ, finalOpts)
				runSingleCommand(client, fmt.Sprintf("echo '%s' | %stee -a /etc/fstab", fstabLine, cmdPrefix))
			}
		}

	case "du_scan":
		path, _ := payload["path"].(string)
		depth, _ := payload["depth"].(string)
		if depth == "" {
			depth = "1"
		}
		cmd := fmt.Sprintf("%sdu -h --max-depth=%s %s 2>/dev/null | sort -hr | head -n 50", cmdPrefix, depth, path)
		out, _ := runSingleCommand(client, cmd)
		sendToHQ("du_result", hostID, termID, out)

	case "analyze_space":
		path, _ := payload["path"].(string)
		// 1. Ottieni dimensioni (bytes) di file e cartelle
		duCmd := fmt.Sprintf("%sdu -ab --max-depth=1 --exclude=/proc --exclude=/sys --exclude=/dev %s 2>/dev/null | sort -nr", cmdPrefix, path)
		duOut, _ := runSingleCommand(client, duCmd)

		// 2. Ottieni metadati (Time, Type) per identificare directory e data modifica
		// Usiamo find -exec stat per robustezza. Format: Timestamp|Type|Path
		statCmd := fmt.Sprintf("%sfind %s -maxdepth 1 -exec stat -c '%%Y|%%F|%%n' {} + 2>/dev/null", cmdPrefix, path)
		statOut, _ := runSingleCommand(client, statCmd)

		metaMap := make(map[string]struct {
			mtime int64
			isDir bool
		})

		for _, line := range strings.Split(statOut, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			parts := strings.SplitN(line, "|", 3)
			if len(parts) < 3 {
				continue
			}
			ts, _ := strconv.ParseInt(parts[0], 10, 64)
			typeStr := strings.ToLower(parts[1])
			fPath := parts[2]
			isDir := strings.Contains(typeStr, "directory")
			metaMap[fPath] = struct {
				mtime int64
				isDir bool
			}{ts, isDir}
		}

		var items []map[string]interface{}
		lines := strings.Split(duOut, "\n")
		var totalSize int64 = 0

		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}

			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}

			sizeStr := fields[0]
			size, err := strconv.ParseInt(sizeStr, 10, 64)
			if err != nil {
				continue
			}

			itemPath := strings.TrimSpace(line[len(sizeStr):])
			name := filepath.Base(itemPath)

			if itemPath == path || itemPath == path+"/" {
				totalSize = size
				continue
			}

			meta, ok := metaMap[itemPath]
			isDir := false
			var mtime int64 = 0
			if ok {
				isDir = meta.isDir
				mtime = meta.mtime
			}

			items = append(items, map[string]interface{}{
				"name":   name,
				"path":   itemPath,
				"size":   size,
				"is_dir": isDir,
				"mtime":  mtime,
			})
		}

		sendToHQ("disk_analysis", hostID, termID, map[string]interface{}{
			"path":  path,
			"total": totalSize,
			"items": items,
		})

	case "delete_item":
		path, _ := payload["path"].(string)
		if path != "" && path != "/" {
			cmd := fmt.Sprintf("%srm -rf \"%s\"", cmdPrefix, path)
			out, err := runSingleCommand(client, cmd)
			if err != nil {
				sendToHQ("disk_op_res", hostID, termID, map[string]string{"status": "error", "msg": out})
			} else {
				sendToHQ("disk_op_res", hostID, termID, map[string]string{"status": "success", "msg": "Eliminato: " + path})
			}
		}

	case "read_fstab":
		out, _ := runSingleCommand(client, "cat /etc/fstab")
		sendToHQ("fstab_content", hostID, termID, out)

	case "save_fstab":
		content, _ := payload["content"].(string)
		b64 := base64.StdEncoding.EncodeToString([]byte(content))
		// Crea Backup prima di salvare
		runSingleCommand(client, fmt.Sprintf("%scp /etc/fstab /etc/fstab.bak.$(date +%%s)", cmdPrefix))
		runSingleCommand(client, fmt.Sprintf("echo '%s' | base64 -d | %stee /etc/fstab", b64, cmdPrefix))
		sendToHQ("disk_op_res", hostID, termID, map[string]string{"status": "success"})

	case "get_fstab_entries":
		out, _ := runSingleCommand(client, "cat /etc/fstab")
		var entries []map[string]string
		scanner := bufio.NewScanner(strings.NewReader(out))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) >= 4 {
				entries = append(entries, map[string]string{
					"device": fields[0],
					"mount":  fields[1],
					"type":   fields[2],
					"opts":   fields[3],
					"dump": func() string {
						if len(fields) > 4 {
							return fields[4]
						} else {
							return "0"
						}
					}(),
					"pass": func() string {
						if len(fields) > 5 {
							return fields[5]
						} else {
							return "0"
						}
					}(),
					"raw": line,
				})
			}
		}
		sendToHQ("fstab_entries", hostID, termID, entries)

	case "add_fstab_entry":
		line, _ := payload["line"].(string)
		if line != "" {
			runSingleCommand(client, fmt.Sprintf("echo '%s' | %stee -a /etc/fstab", line, cmdPrefix))
			sendToHQ("disk_op_res", hostID, termID, map[string]string{"status": "success"})
		}

	case "remove_fstab_entry":
		rawLine, _ := payload["raw"].(string)
		// Usa grep -v per rimuovere la linea esatta (escape caratteri speciali se necessario, qui semplificato)
		// Creiamo un file temporaneo e poi sovrascriviamo
		if rawLine != "" {
			// Backup
			runSingleCommand(client, fmt.Sprintf("%scp /etc/fstab /etc/fstab.bak.$(date +%%s)", cmdPrefix))
			runSingleCommand(client, fmt.Sprintf("%sgrep -F -v '%s' /etc/fstab | %stee /etc/fstab.tmp && %smv /etc/fstab.tmp /etc/fstab", cmdPrefix, rawLine, cmdPrefix, cmdPrefix))
			sendToHQ("disk_op_res", hostID, termID, map[string]string{"status": "success"})
		}

	case "get_mounts":
		out, _ := runSingleCommand(client, "cat /proc/mounts")
		var mounts []map[string]string
		scanner := bufio.NewScanner(strings.NewReader(out))
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) >= 4 {
				mounts = append(mounts, map[string]string{
					"device": fields[0],
					"mount":  fields[1],
					"type":   fields[2],
					"opts":   fields[3],
				})
			}
		}
		sendToHQ("disk_mounts", hostID, termID, mounts)
	}
}

func parseLVMOutput(output string, keys []string) []map[string]string {
	var result []map[string]string
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.Split(line, ";")
		if len(parts) < len(keys) {
			continue
		}
		item := make(map[string]string)
		for i, key := range keys {
			item[key] = strings.TrimSpace(parts[i])
		}
		result = append(result, item)
	}
	return result
}

func parseMdstat(output string) []map[string]interface{} {
	var arrays []map[string]interface{}
	lines := strings.Split(output, "\n")
	var currentArray map[string]interface{}

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Personalities") || line == "" {
			continue
		}

		// Start of array definition: "md3 : active raid1 ..."
		if strings.Contains(line, ": active") || strings.Contains(line, ": inactive") {
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				currentArray = make(map[string]interface{})
				currentArray["name"] = parts[0] // md3
				// parts[1] is ":"
				currentArray["state"] = parts[2] // active
				currentArray["level"] = parts[3] // raid1

				var devs []string
				for i := 4; i < len(parts); i++ {
					devs = append(devs, parts[i])
				}
				currentArray["devices"] = devs
				arrays = append(arrays, currentArray)
			}
			continue
		}

		// Details line: "497875968 blocks super 1.2 [2/2] [UU]"
		if currentArray != nil {
			if strings.Contains(line, "blocks") {
				parts := strings.Fields(line)
				if len(parts) > 0 {
					currentArray["size_blocks"] = parts[0]
				}
				// Find [UU]
				for _, p := range parts {
					if strings.HasPrefix(p, "[") && strings.HasSuffix(p, "]") && (strings.Contains(p, "U") || strings.Contains(p, "_")) {
						currentArray["status_graph"] = p
					}
				}
			}
			if strings.Contains(line, "recovery") || strings.Contains(line, "resync") {
				currentArray["sync_action"] = line
			}
		}
	}
	return arrays
}

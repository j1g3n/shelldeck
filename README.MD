# Shelldeck 🚀
**The Zero-Trust, Agentless RMM & OS Configuration Suite.**

![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat&logo=go)
![License](https://img.shields.io/badge/License-AGPLv3-blue.svg)
![Status](https://img.shields.io/badge/Status-Beta-orange)

Shelldeck is an advanced, agentless Remote Monitoring and Management (RMM) platform built for Systems Architects, Platform Engineers, and SysAdmins. It abstracts the complexity of raw SSH access, providing a powerful Web GUI to manage Linux servers, containers, networks, and storage—without ever installing an agent on the target machines.

## 🧠 The Architecture: Network Pivoting & Zero-Trust
Traditional RMMs require agents on every machine, which is a security nightmare and impossible in strictly isolated networks. Traditional web-SSH portals require exposing the central server directly to the target servers. 

Shelldeck uses a **WebSocket Message Broker Architecture** to bypass these limits:
1. **The Server (Gateway):** A lightweight Go server that holds encrypted configurations and routes WebSocket packets. It never initiates an SSH connection.
2. **The Bridge (Agent/Client):** Runs on the SysAdmin's local machine (or a Bastion host) securely inside the VPN. It connects to the Server via WebSockets, receives the commands, and translates them into raw SSH multiplexed sessions towards the target isolated servers.
3. **The Result:** You can manage servers deep inside private, isolated networks directly from a web browser over the internet, without exposing SSH ports or configuring complex VPN routing.

## ✨ Key Features

### 🔌 Advanced Connectivity & Multiplexing
* **Agentless:** 100% SSH-based. No software to install on the target nodes.
* **SSH Multiplexing:** Opens a single TCP socket per host and multiplexes multiple virtual PTY channels (multi-tab terminals, concurrent background tasks) to drastically reduce network overhead.
* **Jump Host (Bastion) Support:** Natively chains SSH connections through bastion servers.

### 🗄️ Deep OS & Storage Management
* **LVM & Parted Integration:** Visual management for Physical Volumes, Volume Groups, and Logical Volumes. Resize partitions and extend filesystems (ext4, xfs, btrfs) on-the-fly.
* **Software RAID:** `mdadm` array creation and status monitoring.
* **Fstab Editor & Mounts:** Manage `/etc/fstab` entries, swap spaces, and tmpfs RAM disks visually.

### 🌐 Network & Security
* **Network Pivoting SFTP:** Transfer files seamlessly from your local browser, through the central server, through the Bridge, to an isolated server—bypassing VPN barriers natively.
* **Firewalling:** Manage `iptables` rules and `UFW` statuses without fear of locking yourself out.
* **Diagnostic Tools:** Execute ping, traceroute, dig, and tcpdump, with the ability to bind traffic to specific network interfaces.
* **SELinux & Services:** Toggle SELinux booleans, manage systemd units, and edit cronjobs.

### 🐳 Docker Orchestration
* **Full Lifecycle:** Start, stop, inspect, and monitor CPU/RAM of containers.
* **Volumes & Images:** Prune, pull, save, and restore named volumes or host-binds.
* **On-the-fly Compose:** Write, edit, and deploy `docker-compose.yml` stacks directly from the web UI.

### 🌍 Web Server Configuration
* **Apache & Nginx:** Visual toggle for `sites-available` to `sites-enabled`.
* **Smart Editors:** Edit `.conf` files with syntax highlighting. Shelldeck automatically runs `nginx -t` or `apache2ctl configtest` before applying and reloading the daemon to prevent crashes.
* **Live Log Streaming:** Stream `access.log` and `error.log` via WebSockets in real-time.

## 🔐 Security Posture
* **AES-GCM Encryption:** Passwords, Passphrases, and PEM Private Keys are never stored in plaintext. They are encrypted at rest using AES-GCM and a Master Key.
* **Multi-Tenant Workspaces:** Logical separation of servers and credentials into different encrypted SQLite databases, with Global Admin and Group Admin Role-Based Access Control (RBAC).
* **Zero-Touch Provisioning:** If the configuration or DB is missing, the server auto-generates them, falling back to a safe port (`9112`) and generating the necessary schema automatically.

## 🚀 Quick Start (Installation)

Shelldeck is a single compiled Go binary. No external dependencies required.

### 1. Build the binaries
```bash
# Clone the repository
git clone [https://github.com/YOUR_USERNAME/shelldeck.git](https://github.com/YOUR_USERNAME/shelldeck.git)
cd shelldeck

# Build the Server (Linux example)
GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o shelldeck-server ./cmd/server

# Build the Bridge (Client for your OS, e.g., Windows)
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o shelldeck-bridge.exe ./cmd/bridge
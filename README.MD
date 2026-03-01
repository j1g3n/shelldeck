# Shelldeck 🚀
**The Zero-Trust, Agentless & Air-Gapped Ready RMM & OS Configuration Suite.**

![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat&logo=go)
![License](https://img.shields.io/badge/License-AGPLv3-blue.svg)
![Status](https://img.shields.io/badge/Status-Beta-orange)

Shelldeck is an advanced, agentless Remote Monitoring and Management (RMM) platform built for Systems Architects, Platform Engineers, and SysAdmins. It abstracts the complexity of raw SSH access, providing a powerful Web GUI to manage Linux servers, containers, networks, and storage—without ever installing an agent on the target machines.

## 🧠 The Architecture: Network Pivoting & Air-Gapped Ready
Traditional RMMs require agents on every machine, which is a security nightmare and impossible in strictly isolated or **air-gapped** networks. Traditional web-SSH portals require exposing the central server directly to the target servers. 

Shelldeck uses a **WebSocket Message Broker Architecture** to bypass these limits:
1. **The Server (Gateway):** A lightweight Go server that holds encrypted configurations and routes WebSocket packets. It never initiates an SSH connection.
2. **The Bridge (Agent/Client):** Runs on the SysAdmin's local machine (or a Bastion host) securely inside the VPN. It connects to the Server via WebSockets, receives the commands, and translates them into raw SSH multiplexed sessions towards the target isolated servers.
3. **The Result:** You can manage servers deep inside private, air-gapped networks directly from a web browser over the internet, without exposing SSH ports or configuring complex VPN routing.

## ✨ Key Features

### 🔌 Advanced Connectivity & Multiplexing
* **Agentless:** 100% SSH-based. No software to install on the target nodes.
* **SSH Multiplexing:** Opens a single TCP socket per host and multiplexes multiple virtual PTY channels (multi-tab terminals, concurrent background tasks) to drastically reduce network overhead.
* **Jump Host (Bastion) Support:** Natively chains SSH connections through bastion servers to reach highly isolated endpoints.
* **NoVNC Proxy:** Intercepts and proxies raw VNC binary data over WebSockets for in-browser remote desktop.

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
git clone https://github.com/YOUR_USERNAME/shelldeck.git
cd shelldeck

# Build the Server (Linux example)
GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o shelldeck-server ./cmd/server

# Build the Bridge (Client for your OS, e.g., Windows)
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o shelldeck-bridge.exe ./cmd/bridge
```

### 2. Run the Server
Simply execute the binary. Shelldeck features a "First Run Experience" (FRE). It will auto-generate the `config.json` and the encrypted databases.
```bash# Shelldeck 🚀
**The Zero-Trust, Agentless RMM & OS Configuration Suite.**

![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat&logo=go)
![License](https://img.shields.io/badge/License-AGPLv3-blue.svg)
![Status](https://img.shields.io/badge/Status-Beta-orange)
![Vibecoding](https://img.shields.io/badge/AI_Code-Vibecoding-8A2BE2?style=flat&logo=openai)
![Airgapped](https://img.shields.io/badge/Security-Totally_Airgapped-green?style=flat&logo=shield)

Shelldeck is an advanced, agentless Remote Monitoring and Management (RMM) platform built for Systems Architects, Platform Engineers, and SysAdmins. It abstracts the complexity of raw SSH access, providing a powerful Web GUI to manage Linux servers, containers, networks, and storage—without ever installing an agent on the target machines.

## 🤖 Disclaimer: SysAdmin Designed, AI Written
> **I am a SysAdmin, not a developer.**
> This entire codebase was written exclusively by Artificial Intelligence ("Vibecoding"). My role was strict System Design and Domain Knowledge mapping—identifying real-world infrastructure bottlenecks and providing the logical architecture, network pivoting strategies, and UI requirements. The AI acted as the execution engine to compile this vision into a working Go application.

## 🧠 The Architecture: Network Pivoting & Zero-Trust
Traditional RMMs require agents on every machine, which is a security nightmare and impossible in strictly isolated networks. Traditional web-SSH portals require exposing the central server directly to the target servers.

Shelldeck uses a **WebSocket Message Broker Architecture** to bypass these limits:

1. **The Server (Gateway):** A lightweight Go server that holds encrypted configurations and routes WebSocket packets. It never initiates an SSH connection.
2. **The Bridge (Agent/Client):** Runs on the SysAdmin's local machine (or a Bastion host) securely inside the VPN. It connects to the Server via WebSockets, receives the commands, and translates them into raw SSH multiplexed sessions towards the target isolated servers.
3. **The Result:** You can manage servers deep inside private, isolated networks directly from a web browser over the internet, without exposing SSH ports or configuring complex VPN routing.

## ✨ Key Features

### 🛡️ TOTALLY AIRGAPPED
Shelldeck is designed to work in completely isolated environments. The **Standalone** build bundles the server, the bridge, and the frontend into a single binary that requires **zero internet access**. No external CDNs, no fonts fetching, no telemetry. Perfect for high-security, air-gapped infrastructure.

### 🔌 Advanced Connectivity & Multiplexing
* **Agentless:** 100% SSH-based. No software to install on the target nodes.
* **SSH Multiplexing:** Opens a single TCP socket per host and multiplexes multiple virtual PTY channels (multi-tab terminals, concurrent background tasks) to drastically reduce network overhead.
* **Jump Host (Bastion) Support:** Natively chains SSH connections through bastion servers.
* **Visual SSH Tunnels:** Easily configure Local Port Forwarding (`-L`), Remote Reverse Forwarding (`-R`), and Dynamic SOCKS5 Proxies (`-D`) directly from the UI without touching the CLI.

### 🗄️ Deep OS & Storage Management
* **Advanced File Explorer:** Full-featured GUI for filesystem traversal, recursive full-text search, visual diffing, file permission management, and in-line file editing.
* **LVM & Parted Integration:** Visual management for Physical Volumes, Volume Groups, and Logical Volumes. Resize partitions and extend filesystems (ext4, xfs, btrfs) on-the-fly.
* **Software RAID:** `mdadm` array creation and status monitoring.
* **Fstab Editor & Mounts:** Manage `/etc/fstab` entries, swap spaces, and tmpfs RAM disks visually.

### 🖥️ Advanced Monitoring & Telemetry
* **Advanced Task Manager:** Live top-style process inspection. View CPU/RAM usage, inspect deeply into process environment variables, view open files (`lsof`), and safely execute `kill` or `renice` commands visually.
* **Advanced Log Viewer:** Real-time, WebSocket-streamed `journalctl` and `/var/log/syslog` monitoring with live tailing.

### 🌐 Network & Security
* **Network Pivoting SFTP:** Transfer files seamlessly from your local browser, through the central server, through the Bridge, to an isolated server—bypassing VPN barriers natively.
* **Firewalling:** Manage `iptables` rules and `UFW` statuses without fear of locking yourself out.
* **Diagnostic Tools:** Execute ping, traceroute, dig, and tcpdump, with the ability to bind traffic to specific network interfaces.
* **SELinux & Services:** Toggle SELinux booleans, manage systemd units, and edit cronjobs.

### ⚙️ Kernel & System Configuration
* **Advanced Kernel Management:** Visually track loaded modules, manage module blacklists, and safely purge old unused kernels to free up boot space.
* **Sysctl Editor:** Edit `/etc/sysctl.conf` parameters with syntax highlighting and safe reloading.

### 🐳 Docker Orchestration
* **Full Lifecycle:** Start, stop, inspect, and monitor CPU/RAM of containers.
* **Volumes & Images:** Prune, pull, save, and restore named volumes or host-binds.
* **On-the-fly Compose:** Write, edit, and deploy `docker-compose.yml` stacks directly from the web UI.

### 🌍 Web Server Configuration
* **Apache & Nginx:** Visual toggle for `sites-available` to `sites-enabled`.
* **Smart Editors:** Edit `.conf` files with syntax highlighting. Shelldeck automatically runs `nginx -t` or `apache2ctl configtest` before applying and reloading the daemon to prevent crashes.

**... And much more!!**

## 🔐 Security Posture
* **AES-GCM Encryption:** Passwords, Passphrases, and PEM Private Keys are never stored in plaintext. They are encrypted at rest using AES-GCM and a Master Key.
* **Multi-Tenant Workspaces:** Logical separation of servers and credentials into different encrypted SQLite databases, with Global Admin and Group Admin Role-Based Access Control (RBAC).
* **Zero-Touch Provisioning:** If the configuration or DB is missing, the server auto-generates them, falling back to a safe port (`9112`) and generating the necessary schema automatically.

## 🚀 Quick Start (Installation)

Shelldeck requires no external dependencies. The application can be compiled in three modes: **Server**, **Bridge**, or **Standalone** (all-in-one).

### 1. Build the binaries
```bash
# Clone the repository
git clone [https://github.com/j1g3n/shelldeck.git](https://github.com/j1g3n/shelldeck.git)
cd shelldeck

# Build the Server (Linux example)
GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o shelldeck-server ./cmd/server

# Build the Bridge (Client for your OS, e.g., Windows)
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o shelldeck-bridge.exe ./cmd/bridge

# Build the Standalone (All-in-one Air-Gapped executable)
GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o shelldeck-standalone ./cmd/standalone
./shelldeck-server
```
*The server will listen on port `9112` by default. Default credentials are `admin` / `admin` (Change them immediately!)*

### 3. Connect the Bridge
Launch the Bridge application on the machine that has VPN/SSH access to your target servers. Enter the Server's URL and authenticate to start the WebSocket link.

## 🛠️ Built With
* **Backend:** [Go (Golang)](https://go.dev/)
* **Database:** SQLite3
* **WebSockets:** [GoFiber WebSockets](https://github.com/gofiber/contrib/websocket)
* **SSH:** `golang.org/x/crypto/ssh`

## 📜 License
This project is licensed under the AGPLv3 License - see the `LICENSE` file for details.

---
*Developed by a SysAdmin, for SysAdmins. Because dealing with infrastructure shouldn't require 50 open terminals.*
# Shelldeck 🚀
**The Zero-Trust, Agentless & Air-Gapped Ready RMM & OS Configuration Suite.**

![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat&logo=go)
![License](https://img.shields.io/badge/License-AGPLv3-blue.svg)
![Status](https://img.shields.io/badge/Status-Beta-orange)

Shelldeck is an advanced, agentless Remote Monitoring and Management (RMM) platform built for Systems Architects, Platform Engineers, and SysAdmins. It abstracts the complexity of raw SSH access, providing a powerful Web GUI to manage Linux servers, containers, networks, and storage—without ever installing an agent on the target machines.

## 🛠️ How It Works (Architecture & Paradigm)

At its core, Shelldeck is a modern, web-based remote management client. You can think of it as a next-generation evolution of traditional tools like PuTTY or WinSCP, but with the UI capabilities of a full server dashboard.

The core innovation of Shelldeck lies in its decoupled architecture. It separates the database (which securely stores credentials, hosts, and configurations) from the execution node (the "Bridge" that actually performs the SSH connections).

Here is why Shelldeck's approach is radically different from the rest of the market:

*   **100% Agentless (Unlike Teleport or Boundary):** Shelldeck requires absolutely zero agents, daemons, or specific configurations on your target machines. If a server has a standard SSH service running, Shelldeck can manage it.

*   **Client-Side Execution (Unlike Apache Guacamole):** Centralized gateways like Guacamole require the server to establish the SSH connection, forcing you to route all your corporate VPNs directly to the central server. Shelldeck flips this paradigm: the Central Server only provides the UI and the encrypted credentials. The actual SSH connection is executed by the Bridge running on the user's PC. This means Shelldeck natively leverages the VPNs and network routes already active on the sysadmin's local machine. (Note: In "Standalone" mode, the Server and Bridge run seamlessly as a single binary on your PC).

*   **Universal Native UI (The power of Cockpit, without the hassle):** Shelldeck parses native Linux commands on the fly, converts the output into structured JSON, and dynamically builds a unified graphical interface on the client side. It gives you the visual power of tools like Cockpit or Webmin, but works Out-Of-The-Box (OOB) across all Linux distributions without installing target software, trusting specific IPs, or opening new firewall ports.

*   **Enterprise-Grade Multi-Tenancy:** Whether the server is deployed locally or hosted remotely, it features complete management of Users, Groups, and Workspaces, allowing strict permission control over your infrastructure.

*   **Bulletproof Local Security:** The Bridge interface is secured locally using a unique, dynamically generated authentication token injected directly into the browser, preventing any unauthorized hijacking of the local execution node.

*(🔜 Coming Soon: Support for deploying headless Remote Bridges (Proxies) to manage air-gapped networks directly from the dashboard, without requiring local VPNs).*

## 🤖 Disclaimer: SysAdmin Designed, AI Written
> **I am a SysAdmin, not a developer.** > This entire codebase was written exclusively by Artificial Intelligence ("Vibecoding"). My role was strict System Design and Domain Knowledge mapping—identifying real-world infrastructure bottlenecks and providing the logical architecture, network pivoting strategies, and UI requirements. The AI acted as the execution engine to compile this vision into a working Go application. 

## 🧠 The Architecture: Network Pivoting & Zero-Trust
Traditional RMMs require agents on every machine, which is a security nightmare and impossible in strictly isolated networks. Traditional web-SSH portals require exposing the central server directly to the target servers.

Shelldeck uses a **WebSocket Message Broker Architecture** to bypass these limits:
1. **The Server (Gateway):** A lightweight Go server that holds encrypted configurations and routes WebSocket packets. It never initiates an SSH connection.
2. **The Bridge (Agent/Client):** Runs on the SysAdmin's local machine (or a Bastion host) securely inside the VPN. It connects to the Server via WebSockets, receives the commands, and translates them into raw SSH multiplexed sessions towards the target isolated servers.
3. **The Result:** You can manage servers deep inside private, isolated networks directly from a web browser over the internet, without exposing SSH ports or configuring complex VPN routing.

## ✨ Key Features

### 🌉 Distributed Edge Bridges (Zero-Trust Network Access) NEW!!!!! ADDED 
The ultimate feature to redefine remote management.
* **Proxy-Mode Bridges:** Install headless Shelldeck Bridges on remote, isolated networks (Edge).
* **Reverse-Tunnel Architecture:** These remote Bridges will connect back to your Central Shelldeck Server via secure WebSockets.
* **VPN-less Management:** As an operator, you will be able to access and execute commands on servers located behind those remote Bridges directly from your web dashboard, completely eliminating the need to establish complex VPNs or configure firewall port-forwarding on the client side.

### 🔌 Advanced Connectivity & Multiplexing
* **Agentless:** 100% SSH-based. No software to install on the target nodes.
* **SSH Multiplexing:** Opens a single TCP socket per host and multiplexes multiple virtual PTY channels (multi-tab terminals, concurrent background tasks) to drastically reduce network overhead.
* **Jump Host (Bastion) Support:** Natively chains SSH connections through bastion servers.
* **Visual SSH Tunnels:** Easily configure Local Port Forwarding (-L), Remote Reverse Forwarding (-R), and Dynamic SOCKS5 Proxies (-D) directly from the UI without touching the CLI.

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
* **Diagnostic Tools:** Execute `ping`, `traceroute`, `dig`, and `tcpdump`, with the ability to bind traffic to specific network interfaces.
* **SELinux & Services:** Toggle SELinux booleans, manage `systemd` units, and edit cronjobs.

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

### 📜 Centralized Script Manager (Zero-Config) NEWW ADDED!!!
A powerful tool to manage your custom automation directly from the UI without rebuilding the server.
* **Dynamic Discovery:** The server will automatically read a structured `scripts/` directory on your file system (e.g., `scripts/bash/`, `scripts/python/`). Any new file added to these folders will instantly appear in the Shelldeck UI.
* **Multi-Mode Execution:** Launch your scripts across connected Bridges with three distinct modes:
  * **Runtime:** Executes the script safely in `/tmp/` and streams the stdout/stderr back to your dashboard, leaving no trace.
  * **Drop:** Copies the script directly into the user's current working directory (`$PWD`) on the target machine.
  * **Cronjob:** Automatically saves the script in a persistent directory and injects the scheduling rule directly into the target user's crontab.
* **Community Sync:** Future support for pulling script bundles directly from GitHub to populate your local toolbox.


## 🔐 Security Posture
* **AES-GCM Encryption:** Passwords, Passphrases, and PEM Private Keys are never stored in plaintext. They are encrypted at rest using AES-GCM and a Master Key.
* **Multi-Tenant Workspaces:** Logical separation of servers and credentials into different encrypted SQLite databases, with Global Admin and Group Admin Role-Based Access Control (RBAC).
* **Zero-Touch Provisioning:** If the configuration or DB is missing, the server auto-generates them, falling back to a safe port (`9112`) and generating the necessary schema automatically.


## 📥 Download (Ready-to-use Binaries) v0.6 UPDATE!!

You don't need to build from source! Download the latest compiled binaries for your operating system directly:

### 🐧 Linux (amd64)
* [⬇️ Download Server (Linux)](https://github.com/j1g3n/shelldeck/releases/download/V0.6/shelldeck-server-linux)
* [⬇️ Download Bridge (Linux)](https://github.com/j1g3n/shelldeck/releases/download/V0.6/shelldeck-bridge-linux)
* [⬇️ Download Bridge-Proxy-headless (Linux)](https://github.com/j1g3n/shelldeck/releases/download/V0.6/shelldeck-bridge-headless)
* [⬇️ Download Standalone (Linux)](https://github.com/j1g3n/shelldeck/releases/download/V0.6/shelldeck-standalone-linux)

### 🪟 Windows (amd64)
* [⬇️ Download Server (Windows)](https://github.com/j1g3n/shelldeck/releases/download/V0.6/shelldeck-server-windows.exe)
* [⬇️ Download Bridge (Windows)](https://github.com/j1g3n/shelldeck/releases/download/V0.6/shelldeck-bridge-windows.exe)
* [⬇️ Download Standalone (Windows)](https://github.com/j1g3n/shelldeck/releases/download/V0.6/shelldeck-standalone-windows.exe)
* [⬇️ Download Bridge (Linux)](https://github.com/j1g3n/shelldeck/releases/download/V0.6/shelldeck-bridge-linux)
*(🍎 Mac OS builds coming very soon!)*

> 📦 **[View all Releases and Changelogs here](https://github.com/j1g3n/shelldeck/releases)**

## 🚀 Quick Start (Installation)

Shelldeck is a single compiled Go binary. No external dependencies required.

### 1. Build the binaries
```bash
# Clone the repository
git clone https://github.com/j1g3n/shelldeck.git
cd shelldeck
chmod +x build.sh
./build.sh
```

### 2. Run the Server
Simply execute the binary. Shelldeck features a "First Run Experience" (FRE). It will auto-generate the `config.json` and the encrypted databases.
```bash# 
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

## 🚀 Upcoming Features (Roadmap to 1.0)

I'm constantly working to stabilize the core and expand Shelldeck's capabilities. Here are the major architectural updates currently in development for the upcoming stable releases:


### 🔒 Automated HTTPS/WSS Setup
Security-by-design right from the first setup. Shelldeck will soon offer a built-in wizard to automatically handle SSL/TLS certificates:
* **Private/Local Networks:** One-click generation of Self-Signed certificates to secure WebSocket traffic (WSS) in air-gapped or VPN-only environments.
* **Public Domains:** Native integration with Let's Encrypt (`acme/autocert`). Simply provide your domain name, and Shelldeck will automatically negotiate, issue, and renew trusted certificates, wrapping your connections in HTTPS/WSS with zero external reverse-proxy configuration needed.



## � License
This project is licensed under the AGPLv3 License - see the `LICENSE` file for details.

---
*Developed by a SysAdmin, for SysAdmins. Because dealing with infrastructure shouldn't require 50 open terminals.*

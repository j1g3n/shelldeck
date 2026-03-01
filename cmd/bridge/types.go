package main

import (
	"io"
	"sync"
	"time"

	"github.com/fasthttp/websocket"
	"golang.org/x/crypto/ssh"
)

// --- CONFIGURAZIONE ---
var (
	CurrentServerURL string
	CurrentMasterKey []byte
	CurrentUsername  string
)

// --- STRUTTURE MESSAGGI ---
type JMessage struct {
	Type    string      `json:"type"`
	HostID  int         `json:"host_id"`
	TermID  string      `json:"term_id,omitempty"`
	Payload interface{} `json:"payload"`
}

type HostCredentials struct {
	IP       string `json:"ip"`
	User     string `json:"user"`
	Protocol string `json:"protocol"`

	// Credenziali Base (ora arrivano in chiaro dal server decriptate)
	Password   string `json:"password"`
	Key        string `json:"key"`
	Passphrase string `json:"passphrase"`

	// Jump Host
	JumpHostID int    `json:"jump_host_id"` // Se usa un host del DB
	JumpIP     string `json:"jump_ip"`      // Se usa un IP custom
	JumpPort   string `json:"jump_port"`
	JumpUser   string `json:"jump_user"`
	JumpPass   string `json:"jump_pass"`

	// Tunnel SSH
	TunnelType  string `json:"tunnel_type"`
	TunnelLPort string `json:"tunnel_lport"`
	TunnelRHost string `json:"tunnel_rhost"`
	TunnelRPort string `json:"tunnel_rport"`

	// RDP Options
	RdpResolution    string `json:"rdp_resolution"`
	RdpColors        string `json:"rdp_colors"`
	RdpWallpaper     bool   `json:"rdp_wallpaper"`
	RdpFontSmoothing bool   `json:"rdp_font_smoothing"`
	RdpComposition   bool   `json:"rdp_composition"`
	RdpMenuAnims     bool   `json:"rdp_menu_anims"`
}

type SSHSessionWrapper struct {
	Session  *ssh.Session
	Stdin    io.WriteCloser
	Password string
	Client   *ssh.Client
}

// --- STRUTTURE DATI SISTEMA ---
type BridgeFileItem struct {
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
	Path  string `json:"path"`
}

type ServiceItem struct {
	Name    string `json:"name"`
	Active  string `json:"active"`
	Sub     string `json:"sub"`
	Enabled string `json:"enabled"`
}

type DFEntry struct {
	Filesystem string `json:"Filesystem"`
	Size       string `json:"Size"`
	Used       string `json:"Used"`
	Avail      string `json:"Avail"`
	Use        string `json:"Use"`
	Mounted    string `json:"Mounted"`
}

type LogFileItem struct {
	Path string `json:"path"`
	Size string `json:"size"`
}

type NginxSite struct {
	File    string `json:"file"`
	Domain  string `json:"domain"`
	Port    string `json:"port"`
	Root    string `json:"root"`
	Enabled bool   `json:"enabled"`
}

type PkgItem struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Status  string `json:"status"`
	Desc    string `json:"desc"`
}

// --- STATO GLOBALE ---
var (
	wsConn      *websocket.Conn
	wsMu        sync.Mutex
	sshSessions = make(map[string]*SSHSessionWrapper) // Key: "hostId:termId"

	prevNetIn   = make(map[string]uint64)
	prevNetOut  = make(map[string]uint64)
	prevNetTime = make(map[string]time.Time)

	streamMu       sync.Mutex
	streamSessions = make(map[string]*ssh.Session) // key: "hostID:streamType"
)

// AgentCommand represents a structured command sent to the host agent
type AgentCommand struct {
	Command string      `json:"command"`
	Payload interface{} `json:"payload"`
}

// SftpcConnectPayload is the payload for the 'sftpc_connect' agent command
type SftpcConnectPayload struct {
	IP             string `json:"ip"`
	User           string `json:"user"`
	Pass           string `json:"pass"`
	ConnectionType string `json:"connection_type"`
}

// SftpcListPayload is the payload for the 'sftpc_list' agent command
type SftpcListPayload struct {
	Path string `json:"path"`
}

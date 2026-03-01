package server

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/contrib/websocket"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
)

// --- CONFIGURAZIONE ---
var MasterKey []byte

// NUOVA STRUCT PER CONFIG
type Config struct {
	ListenAddress string `json:"listen_address"`
}

var serverConfig Config // Variabile globale per la config

// --- STATO GLOBALE (ROUTING) ---
var (
	db                 *sql.DB
	usersDB            *sql.DB
	currentWorkspaceID int
	activeBridge       *websocket.Conn
	bridgeMu           sync.Mutex
	activeBridgeUser   string
	activeBridgeToken  string
	// Mappa: HostID -> Lista di client browser connessi
	browserClients = make(map[int][]*websocket.Conn)
	clientsMu      sync.Mutex
	// Mappa: ConnID -> WebSocket del client noVNC
	vncClients   = make(map[string]*websocket.Conn)
	vncClientsMu sync.Mutex
)

// --- STRUTTURE DATI ---
type JMessage struct {
	Type    string      `json:"type"` // "ssh_input", "ssh_output", "sys_stats", "sys_logs", "subscribe", "setup_ssh_key", "save_generated_key"
	HostID  int         `json:"host_id"`
	TermID  string      `json:"term_id,omitempty"`
	Payload interface{} `json:"payload"`
}

type Group struct {
	ID        int    `json:"id"`
	ParentID  *int   `json:"parent_id"`
	Name      string `json:"name"`
	BgColor   string `json:"bg_color"`
	TextColor string `json:"text_color"`

	// Campi credenziali (per JSON binding e logica)
	User       string `json:"user,omitempty"`
	Password   string `json:"password,omitempty"`
	Key        string `json:"key,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`

	// Campi DB (cifrati)
	EncPassword   string `json:"-"`
	EncKey        string `json:"-"`
	EncPassphrase string `json:"-"`

	// Jump Host di Gruppo (Futuro uso nel bridge)
	JumpHostID  *int   `json:"jump_host_id"`
	JumpIP      string `json:"jump_ip"`
	JumpPort    string `json:"jump_port"`
	JumpUser    string `json:"jump_user"`
	JumpPass    string `json:"jump_pass,omitempty"`
	EncJumpPass string `json:"-"`
}

type Host struct {
	ID       int    `json:"id"`
	GroupID  *int   `json:"group_id"`
	Name     string `json:"name"`
	IP       string `json:"ip"`
	User     string `json:"user"`
	Protocol string `json:"protocol"`
	Favorite int    `json:"favorite"`
	IsTemp   int    `json:"is_temp"` // 1 per i Quick Connect

	// Campi input dal JSON (Frontend)
	Password   string `json:"password,omitempty"`
	Key        string `json:"key,omitempty"`        // PEM Private Key
	Passphrase string `json:"passphrase,omitempty"` // Passphrase per la Key

	JumpHostID *int `json:"jump_host_id,omitempty"`
	// Custom Jump Host
	JumpIP   string `json:"jump_ip,omitempty"`
	JumpPort string `json:"jump_port,omitempty"`
	JumpUser string `json:"jump_user,omitempty"`
	JumpPass string `json:"jump_pass,omitempty"`

	// Campi Tunnel SSH
	TunnelType  string `json:"tunnel_type,omitempty"`
	TunnelLPort string `json:"tunnel_lport,omitempty"`
	TunnelRHost string `json:"tunnel_rhost,omitempty"`
	TunnelRPort string `json:"tunnel_rport,omitempty"`
	VncPort     string `json:"vnc_port,omitempty"`

	// Campi DB (Cifrati)
	EncPassword   string `json:"-"`
	EncKey        string `json:"-"`
	EncPassphrase string `json:"-"`
	EncJumpPass   string `json:"-"`
}

type QuickConnectReq struct {
	Protocol string `json:"protocol"`
	IP       string `json:"ip"`
	User     string `json:"user"`
	Password string `json:"password"`
}

// Request struct per API varie
type BulkMoveReq struct {
	HostIDs       []int `json:"host_ids"`
	TargetGroupID *int  `json:"target_group_id"`
}
type ColorReq struct {
	Type  string `json:"type"`
	Color string `json:"color"`
}
type RenameReq struct {
	Name string `json:"name"`
}
type MoveGroupReq struct {
	ParentID *int `json:"parent_id"`
}

type LoginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Strutture per Admin API
type AdminUser struct {
	ID            int    `json:"id"`
	Username      string `json:"username"`
	Password      string `json:"password,omitempty"` // Solo in input
	IsGlobalAdmin bool   `json:"is_global_admin"`
}

type AdminGroup struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	DBPath        string `json:"db_path"`
	EncryptionKey string `json:"encryption_key"`
}

type AdminUserGroup struct {
	UserID       int  `json:"user_id"`
	GroupID      int  `json:"group_id"`
	IsGroupAdmin bool `json:"is_group_admin"`
}

type ImportDBReq struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Key  string `json:"key"`
}

type AssignmentReq struct {
	UserID  int  `json:"user_id"`
	GroupID int  `json:"group_id"`
	IsAdmin bool `json:"is_group_admin"`
}

// --- CRITTOGRAFIA ---
func encryptWithKey(text string, key []byte) (string, error) {
	if text == "" {
		return "", nil
	}
	c, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(c)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(text), nil)), nil
}

func decryptWithKey(encryptedText string, key []byte) (string, error) {
	if encryptedText == "" {
		return "", nil
	}
	ciphertext, err := base64.StdEncoding.DecodeString(encryptedText)
	if err != nil {
		return "", err
	}
	c, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(c)
	if err != nil {
		return "", err
	}
	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return "", fmt.Errorf("testo cifrato troppo corto")
	}
	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func encrypt(text string) (string, error) {
	return encryptWithKey(text, MasterKey)
}

func decrypt(encryptedText string) (string, error) {
	return decryptWithKey(encryptedText, MasterKey)
}

func loadConfig() {
	// Default config
	serverConfig = Config{
		ListenAddress: ":9112",
	}

	configFile, err := os.ReadFile("config.json")
	if err != nil {
		// Se il file non esiste, lo creiamo con i valori di default
		if os.IsNotExist(err) {
			log.Println("File di configurazione non trovato, ne creo uno nuovo con porta 9112: config.json")
			file, _ := json.MarshalIndent(serverConfig, "", "  ")
			_ = os.WriteFile("config.json", file, 0644)
		} else {
			log.Printf("Errore lettura config.json: %v. Uso i valori di default.", err)
		}
		return
	}

	// Se il file esiste, lo parso
	err = json.Unmarshal(configFile, &serverConfig)
	if err != nil {
		log.Printf("Errore parsing config.json: %v. Uso i valori di default.", err)
	}
}

func generateRandomKey(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		log.Fatal("Error generating random key:", err)
	}
	for i := range b {
		b[i] = charset[b[i]%byte(len(charset))]
	}
	return string(b)
}

// --- DATABASE ---
func initUsersDB() {
	var err error
	// Assicura che la cartella esista
	if _, err := os.Stat("data/server"); os.IsNotExist(err) {
		os.MkdirAll("data/server", 0755)
	}
	usersDB, err = sql.Open("sqlite3", "data/server/users.db")
	if err != nil {
		log.Fatal(err)
	}

	_, err = usersDB.Exec(`CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT UNIQUE,
		password TEXT,
		is_global_admin INTEGER DEFAULT 0
	);`)
	if err != nil {
		log.Fatal("Errore creazione tabella users:", err)
	}

	// MIGRATION: Assicura che la colonna is_global_admin esista (ignora errore se c'è già)
	usersDB.Exec("ALTER TABLE users ADD COLUMN is_global_admin INTEGER DEFAULT 0")

	// FIX: Assicura che l'utente 'admin' sia sempre Global Admin (corregge migrazioni su DB esistenti)
	usersDB.Exec("UPDATE users SET is_global_admin = 1 WHERE username = 'admin'")

	// Tabella Gruppi (Workspaces/Database)
	_, err = usersDB.Exec(`CREATE TABLE IF NOT EXISTS groups (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT UNIQUE,
		db_path TEXT,
		encryption_key TEXT
	);`)
	if err != nil {
		log.Fatal("Errore creazione tabella groups:", err)
	}

	// Tabella di Giunzione Utenti-Gruppi
	_, err = usersDB.Exec(`CREATE TABLE IF NOT EXISTS user_groups (
		user_id INTEGER,
		group_id INTEGER,
		is_group_admin INTEGER DEFAULT 0,
		PRIMARY KEY (user_id, group_id),
		FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE,
		FOREIGN KEY(group_id) REFERENCES groups(id) ON DELETE CASCADE
	);`)
	if err != nil {
		log.Fatal("Errore creazione tabella user_groups:", err)
	}

	// Tabella Sessioni (Persistenza)
	_, err = usersDB.Exec(`CREATE TABLE IF NOT EXISTS sessions (
		token TEXT PRIMARY KEY,
		user_id INTEGER,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
	);`)
	if err != nil {
		log.Fatal("Errore creazione tabella sessions:", err)
	}

	// Seed Default Admin
	var count int
	usersDB.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
	if count == 0 {
		// Admin globale
		usersDB.Exec("INSERT INTO users (username, password, is_global_admin) VALUES ('admin', 'admin', 1)")

		// Gruppo Default
		defaultKey := generateRandomKey(32)
		res, _ := usersDB.Exec("INSERT INTO groups (name, db_path, encryption_key) VALUES ('Default', 'data/server/default.db', ?)", defaultKey)
		groupID, _ := res.LastInsertId()

		// Associazione Admin -> Default Group
		var userID int
		usersDB.QueryRow("SELECT id FROM users WHERE username='admin'").Scan(&userID)
		usersDB.Exec("INSERT INTO user_groups (user_id, group_id, is_group_admin) VALUES (?, ?, 1)", userID, groupID)
	}

	// Load MasterKey (Fix: Ensure a key is loaded on startup)
	var keyStr string
	err = usersDB.QueryRow("SELECT encryption_key FROM groups WHERE name='Default'").Scan(&keyStr)
	if err != nil {
		// Fallback: try first available group
		usersDB.QueryRow("SELECT encryption_key FROM groups LIMIT 1").Scan(&keyStr)
	}
	if keyStr != "" {
		MasterKey = []byte(keyStr)
	} else {
		log.Println("⚠️ Warning: No MasterKey loaded. Cryptography will fail until login/workspace switch.")
	}
}

func migrateDB(targetDB *sql.DB) {
	targetDB.Exec(`CREATE TABLE IF NOT EXISTS groups (id INTEGER PRIMARY KEY AUTOINCREMENT, parent_id INTEGER, name TEXT NOT NULL);`)
	targetDB.Exec(`CREATE TABLE IF NOT EXISTS hosts (
		id INTEGER PRIMARY KEY AUTOINCREMENT, 
		group_id INTEGER, 
		name TEXT, 
		ip TEXT, 
		user TEXT, 
		protocol TEXT DEFAULT 'ssh',
		enc_password TEXT,
		enc_key TEXT,
		enc_passphrase TEXT,
		FOREIGN KEY(group_id) REFERENCES groups(id) ON DELETE SET NULL
	);`)

	// MIGRATIONS (Ignora errori se le colonne esistono già)
	targetDB.Exec(`ALTER TABLE hosts ADD COLUMN favorite INTEGER DEFAULT 0;`)
	targetDB.Exec(`ALTER TABLE hosts ADD COLUMN is_temp INTEGER DEFAULT 0;`)
	targetDB.Exec(`ALTER TABLE hosts ADD COLUMN jump_ip TEXT DEFAULT '';`)
	targetDB.Exec(`ALTER TABLE hosts ADD COLUMN jump_port TEXT DEFAULT '22';`)
	targetDB.Exec(`ALTER TABLE hosts ADD COLUMN jump_user TEXT DEFAULT '';`)
	targetDB.Exec(`ALTER TABLE hosts ADD COLUMN enc_jump_pass TEXT DEFAULT '';`)
	targetDB.Exec(`ALTER TABLE hosts ADD COLUMN tunnel_type TEXT DEFAULT '';`)
	targetDB.Exec(`ALTER TABLE hosts ADD COLUMN tunnel_lport TEXT DEFAULT '';`)
	targetDB.Exec(`ALTER TABLE hosts ADD COLUMN tunnel_rhost TEXT DEFAULT '';`)
	targetDB.Exec(`ALTER TABLE hosts ADD COLUMN tunnel_rport TEXT DEFAULT '';`)
	targetDB.Exec(`ALTER TABLE hosts ADD COLUMN vnc_port TEXT DEFAULT '5900';`)
	targetDB.Exec(`ALTER TABLE hosts ADD COLUMN jump_host_id INTEGER DEFAULT NULL;`)

	targetDB.Exec(`ALTER TABLE groups ADD COLUMN jump_ip TEXT DEFAULT '';`)
	targetDB.Exec(`ALTER TABLE groups ADD COLUMN jump_port TEXT DEFAULT '22';`)
	targetDB.Exec(`ALTER TABLE groups ADD COLUMN jump_user TEXT DEFAULT '';`)
	targetDB.Exec(`ALTER TABLE groups ADD COLUMN enc_jump_pass TEXT DEFAULT '';`)
	targetDB.Exec(`ALTER TABLE groups ADD COLUMN bg_color TEXT DEFAULT 'transparent';`)
	targetDB.Exec(`ALTER TABLE groups ADD COLUMN text_color TEXT DEFAULT '#e6edf3';`)
	targetDB.Exec(`ALTER TABLE groups ADD COLUMN user TEXT DEFAULT '';`)
	targetDB.Exec(`ALTER TABLE groups ADD COLUMN enc_password TEXT DEFAULT '';`)
	targetDB.Exec(`ALTER TABLE groups ADD COLUMN enc_key TEXT DEFAULT '';`)
	targetDB.Exec(`ALTER TABLE groups ADD COLUMN enc_passphrase TEXT DEFAULT '';`)
	targetDB.Exec(`ALTER TABLE groups ADD COLUMN jump_host_id INTEGER DEFAULT NULL;`)
}

func initDB() {
	var err error
	if _, err := os.Stat("data/server"); os.IsNotExist(err) {
		os.MkdirAll("data/server", 0755)
	}
	db, err = sql.Open("sqlite3", "data/server/default.db?_foreign_keys=on")
	if err != nil {
		log.Fatal(err)
	}
	migrateDB(db)
	seedData()
}

func seedData() {
	// Lasciamo il database vuoto come richiesto, pronto per l'uso.
}

// --- HELPER FILE COPY ---
func copyFile(src, dst string) error {
	sourceFileStat, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !sourceFileStat.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", src)
	}
	source, err := os.Open(src)
	if err != nil {
		return err
	}
	defer source.Close()
	destination, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destination.Close()
	_, err = io.Copy(destination, source)
	return err
}

func getTargetDBAndKey(c *fiber.Ctx) (*sql.DB, []byte, bool, error) {
	wsID := c.Query("workspace_id")
	if wsID == "" || wsID == "0" {
		return db, MasterKey, false, nil
	}
	var dbPath, encKey string
	if err := usersDB.QueryRow("SELECT db_path, encryption_key FROM groups WHERE id = ?", wsID).Scan(&dbPath, &encKey); err != nil {
		return nil, nil, false, err
	}
	tempDB, err := sql.Open("sqlite3", dbPath+"?_foreign_keys=on")
	if err != nil {
		return nil, nil, false, err
	}
	// Se encKey è vuota, potremmo usare MasterKey o fallire. Qui usiamo MasterKey come fallback se vuota, ma dovrebbe esserci.
	key := MasterKey
	if encKey != "" {
		key = []byte(encKey)
	}
	return tempDB, key, true, nil
}

// --- MAIN ---
func Start() {
	loadConfig()
	initUsersDB()
	initDB()

	// Inizializza currentWorkspaceID con il gruppo Default
	usersDB.QueryRow("SELECT id FROM groups WHERE db_path = 'data/server/default.db'").Scan(&currentWorkspaceID)

	app := fiber.New(fiber.Config{
		BodyLimit: 10 * 1024 * 1024,
	})

	app.Get("/favicon.ico", func(c *fiber.Ctx) error {
		return c.SendStatus(204)
	})

	app.Static("/", "./web/home") // Se hai risolto il Cannot GET /, lascio invariato
	app.Static("/host", "./web/host")
	app.Static("/lib", "./web/lib")
	// --- MIDDLEWARE AUTH ---
	authReq := func(c *fiber.Ctx) error {
		cookie := c.Cookies("session_id")
		if cookie == "" {
			return c.Status(401).JSON(fiber.Map{"status": "error", "msg": "Autorizzazione richiesta"})
		}

		var uid int
		var username string
		err := usersDB.QueryRow("SELECT u.id, u.username FROM users u JOIN sessions s ON u.id = s.user_id WHERE s.token = ?", cookie).Scan(&uid, &username)
		if err != nil {
			c.ClearCookie("session_id")
			return c.Status(401).JSON(fiber.Map{"status": "error", "msg": "Sessione non valida"})
		}

		// NUOVO: Forza la corrispondenza tra utente UI e utente bridge, se attivo
		bridgeMu.Lock()
		bridgeUser := activeBridgeUser
		bridgeMu.Unlock()
		if bridgeUser != "" && username != bridgeUser {
			c.ClearCookie("session_id")
			return c.Status(403).JSON(fiber.Map{"status": "error", "msg": "Conflitto utente. L'utente loggato deve corrispondere all'utente del bridge: " + bridgeUser})
		}

		c.Locals("user_id", uid)
		return c.Next()
	}

	adminReq := func(c *fiber.Ctx) error {
		uid := c.Locals("user_id").(int)
		var isGlobal int
		err := usersDB.QueryRow("SELECT is_global_admin FROM users WHERE id = ?", uid).Scan(&isGlobal)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"status": "error", "msg": "User check failed"})
		}
		if isGlobal != 1 {
			return c.Status(403).JSON(fiber.Map{"status": "error", "msg": "Forbidden: Global Admins only"})
		}
		return c.Next()
	}

	// --- API ME (Current User Info) ---
	app.Get("/api/me", authReq, func(c *fiber.Ctx) error {
		uid := c.Locals("user_id").(int)
		var u AdminUser
		err := usersDB.QueryRow("SELECT id, username, is_global_admin FROM users WHERE id = ?", uid).Scan(&u.ID, &u.Username, &u.IsGlobalAdmin)
		if err != nil {
			return c.Status(404).JSON(fiber.Map{"status": "error", "msg": "User not found"})
		}
		return c.JSON(u)
	})

	// --- API AUTO-LOGIN (Bridge) ---
	app.Get("/api/autologin", func(c *fiber.Ctx) error {
		token := c.Query("token")
		bridgeMu.Lock()
		validToken := activeBridgeToken
		targetUser := activeBridgeUser
		bridgeMu.Unlock()

		if token == "" || token != validToken || targetUser == "" {
			return c.Status(401).SendString("Invalid or expired auto-login token")
		}

		var userID int
		usersDB.QueryRow("SELECT id FROM users WHERE username = ?", targetUser).Scan(&userID)

		sessionToken := generateRandomKey(32)
		usersDB.Exec("INSERT INTO sessions (token, user_id) VALUES (?, ?)", sessionToken, userID)

		c.Cookie(&fiber.Cookie{
			Name:     "session_id",
			Value:    sessionToken,
			HTTPOnly: true,
		})

		return c.Redirect("/")
	})

	// --- API LOGIN ---
	app.Post("/api/login", func(c *fiber.Ctx) error {
		var req LoginReq
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).SendString("Bad Request")
		}

		var userID int
		var pwd string
		var isGlobalAdmin bool

		// Verifica credenziali
		err := usersDB.QueryRow("SELECT id, password, is_global_admin FROM users WHERE username = ?", req.Username).Scan(&userID, &pwd, &isGlobalAdmin)
		if err != nil {
			if err == sql.ErrNoRows {
				return c.Status(401).JSON(fiber.Map{"status": "error", "msg": "Utente non trovato"})
			}
			log.Println("Login DB Error:", err)
			return c.Status(500).JSON(fiber.Map{"status": "error", "msg": "Errore Database Interno"})
		}
		if pwd != req.Password {
			return c.Status(401).JSON(fiber.Map{"status": "error", "msg": "Password errata"})
		}
		log.Printf("User %s logged in. ID: %d, GlobalAdmin: %v", req.Username, userID, isGlobalAdmin)

		// Recupera i workspace accessibili
		var rows *sql.Rows
		if isGlobalAdmin {
			rows, err = usersDB.Query(`SELECT id, name, db_path, encryption_key, 1 as is_group_admin FROM groups`)
		} else {
			rows, err = usersDB.Query(`
				SELECT g.id, g.name, g.db_path, g.encryption_key, ug.is_group_admin 
				FROM groups g 
				JOIN user_groups ug ON g.id = ug.group_id 
				WHERE ug.user_id = ?`, userID)
		}

		if err != nil {
			return c.Status(500).SendString("DB Error")
		}
		defer rows.Close()

		var workspaces []map[string]interface{}
		for rows.Next() {
			var gID int
			var gName, gPath, gKey string
			var isGrpAdmin bool
			rows.Scan(&gID, &gName, &gPath, &gKey, &isGrpAdmin)
			workspaces = append(workspaces, map[string]interface{}{
				"id": gID, "name": gName, "db_path": gPath, "encryption_key": gKey, "is_group_admin": isGrpAdmin,
			})
		}

		// Se è il primo login e non ci sono workspace, o se è global admin, gestiamo di conseguenza.
		// Per ora ritorniamo la lista.

		// Imposta MasterKey del primo workspace trovato come default per questa sessione server (semplificazione)
		if len(workspaces) > 0 {
			if key, ok := workspaces[0]["encryption_key"].(string); ok {
				MasterKey = []byte(key)
			}
		}

		// Create Session
		token := generateRandomKey(32)
		_, err = usersDB.Exec("INSERT INTO sessions (token, user_id) VALUES (?, ?)", token, userID)
		if err != nil {
			return c.Status(500).SendString("Session Error")
		}

		c.Cookie(&fiber.Cookie{
			Name:     "session_id",
			Value:    token,
			HTTPOnly: true,
		})

		return c.JSON(fiber.Map{
			"status": "success",
			"user": fiber.Map{
				"id":              userID,
				"username":        req.Username,
				"is_global_admin": isGlobalAdmin,
			},
			"workspaces": workspaces,
			// Per retrocompatibilità col bridge attuale che si aspetta campi piatti:
			"encryption_key":       string(MasterKey),
			"current_workspace_id": currentWorkspaceID,
		})
	})

	// --- API LOGOUT ---
	app.Post("/api/logout", func(c *fiber.Ctx) error {
		cookie := c.Cookies("session_id")
		if cookie != "" {
			usersDB.Exec("DELETE FROM sessions WHERE token = ?", cookie)
		}
		c.ClearCookie("session_id")
		return c.JSON(fiber.Map{"status": "success"})
	})

	// --- API WORKSPACE SWITCH ---
	app.Post("/api/workspace/switch", authReq, func(c *fiber.Ctx) error {
		var req struct {
			ID   int  `json:"id"`
			Save bool `json:"save"`
		}
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).SendString("Bad Request")
		}

		var dbPath, encKey string
		err := usersDB.QueryRow("SELECT db_path, encryption_key FROM groups WHERE id = ?", req.ID).Scan(&dbPath, &encKey)
		if err != nil {
			return c.Status(404).SendString("Workspace not found")
		}

		if db != nil {
			db.Close()
		}
		db, err = sql.Open("sqlite3", dbPath+"?_foreign_keys=on")
		if err != nil {
			return c.Status(500).SendString("Error opening DB: " + err.Error())
		}
		currentWorkspaceID = req.ID
		migrateDB(db) // Assicura che il DB switchato sia aggiornato
		if encKey != "" {
			MasterKey = []byte(encKey)
		}

		// Notifica il Bridge per salvare la preferenza
		if req.Save {
			bridgeMu.Lock()
			if activeBridge != nil {
				activeBridge.WriteJSON(JMessage{
					Type:    "save_workspace",
					Payload: map[string]string{"id": strconv.Itoa(req.ID)},
				})
			}
			bridgeMu.Unlock()
		}

		return c.JSON(fiber.Map{"status": "success"})
	})

	// --- API ADMIN: USERS ---
	app.Get("/api/admin/users", authReq, func(c *fiber.Ctx) error {
		uid := c.Locals("user_id").(int)
		var isGlobal int
		usersDB.QueryRow("SELECT is_global_admin FROM users WHERE id = ?", uid).Scan(&isGlobal)

		// Se non è global, controlliamo se è almeno Group Admin di qualche gruppo
		if isGlobal == 0 {
			var count int
			usersDB.QueryRow("SELECT COUNT(*) FROM user_groups WHERE user_id = ? AND is_group_admin = 1", uid).Scan(&count)
			if count == 0 {
				return c.Status(403).JSON(fiber.Map{"status": "error", "msg": "Forbidden"})
			}
		}

		rows, err := usersDB.Query("SELECT id, username, is_global_admin FROM users")
		if err != nil {
			return c.Status(500).SendString(err.Error())
		}
		defer rows.Close()
		users := []AdminUser{}
		for rows.Next() {
			var u AdminUser
			rows.Scan(&u.ID, &u.Username, &u.IsGlobalAdmin)
			users = append(users, u)
		}
		return c.JSON(users)
	})

	app.Post("/api/admin/users", authReq, func(c *fiber.Ctx) error {
		uid := c.Locals("user_id").(int)
		var isGlobal int
		usersDB.QueryRow("SELECT is_global_admin FROM users WHERE id = ?", uid).Scan(&isGlobal)

		if isGlobal == 0 {
			var count int
			usersDB.QueryRow("SELECT COUNT(*) FROM user_groups WHERE user_id = ? AND is_group_admin = 1", uid).Scan(&count)
			if count == 0 {
				return c.Status(403).JSON(fiber.Map{"status": "error", "msg": "Forbidden"})
			}
		}

		var u AdminUser
		if err := c.BodyParser(&u); err != nil {
			return c.Status(400).SendString(err.Error())
		}

		// Group Admin non può creare Global Admin
		if isGlobal == 0 && u.IsGlobalAdmin {
			return c.Status(403).JSON(fiber.Map{"status": "error", "msg": "Group Admins cannot create Global Admins"})
		}

		_, err := usersDB.Exec("INSERT INTO users (username, password, is_global_admin) VALUES (?, ?, ?)", u.Username, u.Password, u.IsGlobalAdmin)
		if err != nil {
			return c.Status(500).SendString(err.Error())
		}
		return c.SendStatus(201)
	})

	app.Put("/api/admin/users/:id", authReq, func(c *fiber.Ctx) error {
		uid := c.Locals("user_id").(int)
		var isGlobal int
		usersDB.QueryRow("SELECT is_global_admin FROM users WHERE id = ?", uid).Scan(&isGlobal)

		if isGlobal == 0 {
			var count int
			usersDB.QueryRow("SELECT COUNT(*) FROM user_groups WHERE user_id = ? AND is_group_admin = 1", uid).Scan(&count)
			if count == 0 {
				return c.Status(403).JSON(fiber.Map{"status": "error", "msg": "Forbidden"})
			}
		}

		id := c.Params("id")
		var u AdminUser
		if err := c.BodyParser(&u); err != nil {
			return c.Status(400).SendString(err.Error())
		}

		// Group Admin non può promuovere a Global Admin
		if isGlobal == 0 && u.IsGlobalAdmin {
			return c.Status(403).JSON(fiber.Map{"status": "error", "msg": "Group Admins cannot set Global Admin flag"})
		}

		if u.Password != "" {
			usersDB.Exec("UPDATE users SET password = ?, is_global_admin = ? WHERE id = ?", u.Password, u.IsGlobalAdmin, id)
		} else {
			usersDB.Exec("UPDATE users SET is_global_admin = ? WHERE id = ?", u.IsGlobalAdmin, id)
		}
		return c.SendStatus(200)
	})

	app.Delete("/api/admin/users/:id", authReq, func(c *fiber.Ctx) error {
		uid := c.Locals("user_id").(int)
		var isGlobal int
		usersDB.QueryRow("SELECT is_global_admin FROM users WHERE id = ?", uid).Scan(&isGlobal)

		if isGlobal == 0 {
			var count int
			usersDB.QueryRow("SELECT COUNT(*) FROM user_groups WHERE user_id = ? AND is_group_admin = 1", uid).Scan(&count)
			if count == 0 {
				return c.Status(403).JSON(fiber.Map{"status": "error", "msg": "Forbidden"})
			}
		}

		id := c.Params("id")
		usersDB.Exec("DELETE FROM users WHERE id = ?", id)
		return c.SendStatus(200)
	})

	// --- API ADMIN: GROUPS (DATABASES) ---
	app.Get("/api/admin/groups", authReq, func(c *fiber.Ctx) error {
		// FIX: Usa l'ID utente dalla sessione sicura, non dalla query string
		userID := c.Locals("user_id").(int)
		var rows *sql.Rows
		var err error

		var isGlobal int
		usersDB.QueryRow("SELECT is_global_admin FROM users WHERE id = ?", userID).Scan(&isGlobal)

		if isGlobal == 1 {
			// Global Admin vede tutto
			rows, err = usersDB.Query("SELECT id, name, db_path, encryption_key FROM groups")
		} else {
			// Group Admin vede solo i gruppi dove è admin
			rows, err = usersDB.Query(`
				SELECT g.id, g.name, g.db_path, g.encryption_key 
				FROM groups g 
				JOIN user_groups ug ON g.id = ug.group_id 
				WHERE ug.user_id = ? AND ug.is_group_admin = 1`, userID)
		}

		if err != nil {
			return c.Status(500).SendString(err.Error())
		}
		defer rows.Close()

		groups := []AdminGroup{}
		for rows.Next() {
			var g AdminGroup
			rows.Scan(&g.ID, &g.Name, &g.DBPath, &g.EncryptionKey)
			groups = append(groups, g)
		}
		return c.JSON(groups)
	})

	app.Post("/api/admin/groups", authReq, adminReq, func(c *fiber.Ctx) error {
		var g AdminGroup
		if err := c.BodyParser(&g); err != nil {
			return c.Status(400).SendString(err.Error())
		}

		newDBPath := fmt.Sprintf("data/server/%s.db", g.Name)
		// Crea file vuoto
		file, err := os.Create(newDBPath)
		if err != nil {
			return c.Status(500).SendString("Error creating DB file")
		}
		file.Close()

		// Inizializza schema (aprendo temporaneamente il nuovo db)
		tempDB, _ := sql.Open("sqlite3", newDBPath)
		migrateDB(tempDB)
		tempDB.Close()

		key := generateRandomKey(32)
		_, err = usersDB.Exec("INSERT INTO groups (name, db_path, encryption_key) VALUES (?, ?, ?)", g.Name, newDBPath, key)
		if err != nil {
			return c.Status(500).SendString(err.Error())
		}
		return c.SendStatus(201)
	})

	// NUOVO: Upload Workspace (File DB + Chiave)
	app.Post("/api/admin/workspace/upload", authReq, adminReq, func(c *fiber.Ctx) error {
		// 1. Recupera campi form
		name := c.FormValue("name")
		key := strings.TrimSpace(c.FormValue("key"))
		file, err := c.FormFile("db_file")

		if name == "" || key == "" || err != nil {
			return c.Status(400).SendString("Missing name, key or file")
		}

		// CHECK: Verifica se il gruppo esiste già
		var count int
		usersDB.QueryRow("SELECT COUNT(*) FROM groups WHERE name = ?", name).Scan(&count)
		if count > 0 {
			return c.Status(409).SendString("Un workspace con questo nome esiste già.")
		}

		// 2. Salva il file
		// Sanitizza il nome per evitare path traversal
		safeName := filepath.Base(name)
		dstPath := fmt.Sprintf("data/server/%s.db", safeName)

		// CHECK: Verifica se il file esiste già (per evitare sovrascritture accidentali)
		if _, err := os.Stat(dstPath); err == nil {
			return c.Status(409).SendString("Un file database con questo nome esiste già sul server.")
		}

		if err := c.SaveFile(file, dstPath); err != nil {
			return c.Status(500).SendString("Failed to save file: " + err.Error())
		}

		// 3. Registra nel DB
		_, err = usersDB.Exec("INSERT INTO groups (name, db_path, encryption_key) VALUES (?, ?, ?)", name, dstPath, key)
		if err != nil {
			// Se fallisce il DB, rimuovi il file per pulizia
			os.Remove(dstPath)
			return c.Status(500).SendString("DB Error: " + err.Error())
		}

		return c.SendStatus(201)
	})

	app.Post("/api/admin/groups/import", authReq, adminReq, func(c *fiber.Ctx) error {
		var req ImportDBReq
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).SendString(err.Error())
		}

		// Verifica esistenza file
		if _, err := os.Stat(req.Path); os.IsNotExist(err) {
			return c.Status(400).SendString("File database non trovato sul server: " + req.Path)
		}

		_, err := usersDB.Exec("INSERT INTO groups (name, db_path, encryption_key) VALUES (?, ?, ?)", req.Name, req.Path, strings.TrimSpace(req.Key))
		if err != nil {
			return c.Status(500).SendString(err.Error())
		}
		return c.SendStatus(201)
	})

	app.Delete("/api/admin/groups/:id", authReq, adminReq, func(c *fiber.Ctx) error {
		id := c.Params("id")

		// Recupera il path prima di cancellare per poter rimuovere il file (opzionale)
		var dbPath string
		usersDB.QueryRow("SELECT db_path FROM groups WHERE id = ?", id).Scan(&dbPath)

		_, err := usersDB.Exec("DELETE FROM groups WHERE id = ?", id)

		// Opzionale: Cancellare anche il file fisico?
		// Per sicurezza spesso si lascia, ma se vuoi cancellarlo decommenta:
		// if dbPath != "" { os.Remove(dbPath) }

		if err != nil {
			return c.Status(500).SendString(err.Error())
		}
		return c.SendStatus(200)
	})

	// --- API ADMIN: ASSIGNMENTS ---
	app.Get("/api/admin/assignments/user/:id", authReq, func(c *fiber.Ctx) error {
		uid := c.Locals("user_id").(int)
		var isGlobal int
		usersDB.QueryRow("SELECT is_global_admin FROM users WHERE id = ?", uid).Scan(&isGlobal)

		if isGlobal == 0 {
			// Group Admin: deve essere admin di almeno un gruppo
			var count int
			usersDB.QueryRow("SELECT COUNT(*) FROM user_groups WHERE user_id = ? AND is_group_admin = 1", uid).Scan(&count)
			if count == 0 {
				return c.Status(403).JSON(fiber.Map{"status": "error", "msg": "Forbidden"})
			}
		}

		userID := c.Params("id")
		rows, err := usersDB.Query("SELECT group_id, is_group_admin FROM user_groups WHERE user_id = ?", userID)
		if err != nil {
			return c.Status(500).SendString(err.Error())
		}
		defer rows.Close()

		assigns := []map[string]interface{}{}
		for rows.Next() {
			var gid int
			var isAdmin bool
			rows.Scan(&gid, &isAdmin)
			assigns = append(assigns, map[string]interface{}{"group_id": gid, "is_group_admin": isAdmin})
		}
		return c.JSON(assigns)
	})

	app.Post("/api/admin/assignments", authReq, func(c *fiber.Ctx) error {
		uid := c.Locals("user_id").(int)
		var isGlobal int
		usersDB.QueryRow("SELECT is_global_admin FROM users WHERE id = ?", uid).Scan(&isGlobal)

		var req AssignmentReq
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).SendString(err.Error())
		}

		// Se Group Admin, verifica che gestisca il gruppo target
		if isGlobal == 0 {
			var isAdminOfTarget int
			usersDB.QueryRow("SELECT COUNT(*) FROM user_groups WHERE user_id = ? AND group_id = ? AND is_group_admin = 1", uid, req.GroupID).Scan(&isAdminOfTarget)
			if isAdminOfTarget == 0 {
				return c.Status(403).JSON(fiber.Map{"status": "error", "msg": "You are not admin of this group"})
			}
		}

		_, err := usersDB.Exec("INSERT OR REPLACE INTO user_groups (user_id, group_id, is_group_admin) VALUES (?, ?, ?)", req.UserID, req.GroupID, req.IsAdmin)
		if err != nil {
			return c.Status(500).SendString(err.Error())
		}
		return c.SendStatus(200)
	})

	app.Delete("/api/admin/assignments", authReq, func(c *fiber.Ctx) error {
		uid := c.Locals("user_id").(int)
		var isGlobal int
		usersDB.QueryRow("SELECT is_global_admin FROM users WHERE id = ?", uid).Scan(&isGlobal)

		var req AssignmentReq
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).SendString(err.Error())
		}

		// Se Group Admin, verifica che gestisca il gruppo target
		if isGlobal == 0 {
			var isAdminOfTarget int
			usersDB.QueryRow("SELECT COUNT(*) FROM user_groups WHERE user_id = ? AND group_id = ? AND is_group_admin = 1", uid, req.GroupID).Scan(&isAdminOfTarget)
			if isAdminOfTarget == 0 {
				return c.Status(403).JSON(fiber.Map{"status": "error", "msg": "You are not admin of this group"})
			}
		}

		_, err := usersDB.Exec("DELETE FROM user_groups WHERE user_id = ? AND group_id = ?", req.UserID, req.GroupID)
		if err != nil {
			return c.Status(500).SendString(err.Error())
		}
		return c.SendStatus(200)
	})

	app.Post("/api/admin/groups/:id/clone", authReq, adminReq, func(c *fiber.Ctx) error {
		id := c.Params("id")
		var req struct {
			NewName string `json:"new_name"`
		}
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).SendString(err.Error())
		}

		var srcPath, srcKey string
		err := usersDB.QueryRow("SELECT db_path, encryption_key FROM groups WHERE id = ?", id).Scan(&srcPath, &srcKey)
		if err != nil {
			return c.Status(404).SendString("Group not found")
		}

		newPath := fmt.Sprintf("data/server/%s.db", req.NewName)
		if err := copyFile(srcPath, newPath); err != nil {
			return c.Status(500).SendString("Error copying DB file: " + err.Error())
		}

		_, err = usersDB.Exec("INSERT INTO groups (name, db_path, encryption_key) VALUES (?, ?, ?)", req.NewName, newPath, srcKey)
		if err != nil {
			return c.Status(500).SendString(err.Error())
		}
		return c.SendStatus(201)
	})

	// --- API ADMIN: SERVER CONFIG ---
	app.Get("/api/admin/config", authReq, adminReq, func(c *fiber.Ctx) error {
		return c.JSON(serverConfig)
	})

	app.Post("/api/admin/config", authReq, adminReq, func(c *fiber.Ctx) error {
		var newConfig Config
		if err := c.BodyParser(&newConfig); err != nil {
			return c.Status(400).SendString(err.Error())
		}

		// Validazione base
		if newConfig.ListenAddress == "" {
			return c.Status(400).SendString("ListenAddress cannot be empty")
		}

		// Salva su file
		file, _ := json.MarshalIndent(newConfig, "", "  ")
		if err := os.WriteFile("config.json", file, 0644); err != nil {
			return c.Status(500).SendString("Failed to write config file: " + err.Error())
		}

		// Se la porta è cambiata, riavvia (esci)
		if newConfig.ListenAddress != serverConfig.ListenAddress {
			go func() {
				log.Println("Porta cambiata. Riavvio server...")
				time.Sleep(1 * time.Second)
				os.Exit(0) // Systemd o Docker dovrebbero riavviarlo
			}()
			return c.JSON(fiber.Map{"status": "success", "msg": "Config saved. Server restarting..."})
		}

		serverConfig = newConfig
		return c.JSON(fiber.Map{"status": "success", "msg": "Config saved."})
	})

	// --- API GET TREE ---
	app.Get("/api/tree", authReq, func(c *fiber.Ctx) error {
		userID := c.Locals("user_id").(int)
		targetDB := db
		wsID := c.Query("workspace_id")

		// Determina ID Workspace target
		var targetID int
		if wsID != "" {
			targetID, _ = strconv.Atoi(wsID)
		} else {
			targetID = currentWorkspaceID
		}

		// Verifica Permessi
		var isGlobal int
		usersDB.QueryRow("SELECT is_global_admin FROM users WHERE id = ?", userID).Scan(&isGlobal)
		if isGlobal == 0 {
			var count int
			usersDB.QueryRow("SELECT COUNT(*) FROM user_groups WHERE user_id = ? AND group_id = ?", userID, targetID).Scan(&count)
			if count == 0 {
				return c.JSON(fiber.Map{"groups": []interface{}{}, "hosts": []interface{}{}})
			}
		}

		// Se richiesto un workspace specifico, apri una connessione temporanea
		if wsID != "" {
			var dbPath string
			if err := usersDB.QueryRow("SELECT db_path FROM groups WHERE id = ?", wsID).Scan(&dbPath); err == nil {
				if tempDB, err := sql.Open("sqlite3", dbPath+"?_foreign_keys=on"); err == nil {
					targetDB = tempDB
					migrateDB(targetDB) // Assicura che il DB del tab sia aggiornato
					defer tempDB.Close()
				}
			}
		}

		rows, err := targetDB.Query("SELECT id, parent_id, name, bg_color, text_color, user, jump_ip, jump_port, jump_user, jump_host_id FROM groups")
		if err != nil {
			return c.Status(500).SendString(err.Error())
		}
		defer rows.Close()
		var groups []Group
		for rows.Next() {
			var g Group
			rows.Scan(&g.ID, &g.ParentID, &g.Name, &g.BgColor, &g.TextColor, &g.User, &g.JumpIP, &g.JumpPort, &g.JumpUser, &g.JumpHostID)
			groups = append(groups, g)
		}

		rows2, err := targetDB.Query(`SELECT id, group_id, name, ip, user, protocol, favorite, jump_ip, 
			CASE WHEN enc_key != '' THEN 1 ELSE 0 END as has_key 
			FROM hosts WHERE is_temp = 0`)
		if err != nil {
			return c.Status(500).SendString(err.Error())
		}
		defer rows2.Close()

		// Per supportare has_key senza modificare la struct, possiamo usare un trucco con una mappa
		var hosts []map[string]interface{}
		for rows2.Next() {
			var h Host
			var hasKey int
			rows2.Scan(&h.ID, &h.GroupID, &h.Name, &h.IP, &h.User, &h.Protocol, &h.Favorite, &h.JumpIP, &hasKey)

			hostMap := map[string]interface{}{
				"id": h.ID, "group_id": h.GroupID, "name": h.Name, "ip": h.IP,
				"user": h.User, "protocol": h.Protocol, "favorite": h.Favorite,
				"jump_ip": h.JumpIP, "has_key": hasKey == 1,
			}
			hosts = append(hosts, hostMap)
		}
		return c.JSON(fiber.Map{"groups": groups, "hosts": hosts, "workspace_id": targetID})
	})

	// --- API CREDENTIALS ---
	app.Get("/api/host-credentials/:id", func(c *fiber.Ctx) error {
		id := c.Params("id")
		wsID := c.Query("workspace_id")
		targetDB := db
		reqKey := MasterKey

		if wsID != "" && wsID != "0" {
			var dbPath, encKey string
			if err := usersDB.QueryRow("SELECT db_path, encryption_key FROM groups WHERE id = ?", wsID).Scan(&dbPath, &encKey); err == nil {
				if tempDB, err := sql.Open("sqlite3", dbPath+"?_foreign_keys=on"); err == nil {
					targetDB = tempDB
					defer tempDB.Close()
				}
				if encKey != "" {
					reqKey = []byte(encKey)
				}
			}
		}

		var h Host
		var groupID sql.NullInt64

		// 1. Prendi le credenziali dirette dell'host e il suo group_id
		err := targetDB.QueryRow(`SELECT group_id, ip, user, enc_password, enc_key, enc_passphrase, protocol, 
			jump_ip, jump_port, jump_user, enc_jump_pass, jump_host_id,
			tunnel_type, tunnel_lport, tunnel_rhost, tunnel_rport, vnc_port
			FROM hosts WHERE id = ?`, id).
			Scan(&groupID, &h.IP, &h.User, &h.EncPassword, &h.EncKey, &h.EncPassphrase, &h.Protocol,
				&h.JumpIP, &h.JumpPort, &h.JumpUser, &h.EncJumpPass, &h.JumpHostID,
				&h.TunnelType, &h.TunnelLPort, &h.TunnelRHost, &h.TunnelRPort, &h.VncPort)
		if err != nil {
			return c.Status(404).SendString("Host not found")
		}

		// 2. Inizia la risoluzione gerarchica se necessario
		finalUser := h.User
		finalEncPass := h.EncPassword
		finalEncKey := h.EncKey
		finalEncPhrase := h.EncPassphrase

		finalJumpIP := h.JumpIP
		finalJumpPort := h.JumpPort
		finalJumpUser := h.JumpUser
		finalEncJumpPass := h.EncJumpPass
		finalJumpHostID := h.JumpHostID

		var currentGroupID *int
		if groupID.Valid {
			cgID := int(groupID.Int64)
			currentGroupID = &cgID
		}

		// Continua a cercare solo se manca qualcosa di fondamentale
		for currentGroupID != nil {
			var g Group
			var parentID sql.NullInt64

			err := targetDB.QueryRow(`SELECT parent_id, user, enc_password, enc_key, enc_passphrase, 
				jump_ip, jump_port, jump_user, enc_jump_pass, jump_host_id FROM groups WHERE id = ?`, *currentGroupID).
				Scan(&parentID, &g.User, &g.EncPassword, &g.EncKey, &g.EncPassphrase, &g.JumpIP, &g.JumpPort, &g.JumpUser, &g.EncJumpPass, &g.JumpHostID)
			if err != nil {
				break // Errore o gruppo non trovato, interrompi
			}

			// Applica le credenziali del gruppo solo se quelle finali sono ancora vuote
			if finalUser == "" {
				finalUser = g.User
			}
			if finalEncPass == "" {
				finalEncPass = g.EncPassword
			}
			if finalEncKey == "" {
				finalEncKey = g.EncKey
			}
			if finalEncPhrase == "" {
				finalEncPhrase = g.EncPassphrase
			}

			// Ereditarietà Jump Host (Se l'host non ne ha uno definito, e non ne abbiamo ancora trovato uno nei gruppi figli)
			if finalJumpIP == "" && finalJumpHostID == nil {
				finalJumpIP = g.JumpIP
				finalJumpPort = g.JumpPort
				finalJumpUser = g.JumpUser
				finalEncJumpPass = g.EncJumpPass
				finalJumpHostID = g.JumpHostID
			}

			if parentID.Valid {
				pID := int(parentID.Int64)
				currentGroupID = &pID
			} else {
				currentGroupID = nil // Radice raggiunta
			}
		}

		// 3. Decripta le credenziali finali trovate
		plainPass, err := decryptWithKey(finalEncPass, reqKey)
		if err != nil {
			return c.Status(500).SendString("Decrypt pass error: " + err.Error())
		}

		plainKey, err := decryptWithKey(finalEncKey, reqKey)
		if err != nil {
			return c.Status(500).SendString("Decrypt key error: " + err.Error())
		}

		plainPhrase, err := decryptWithKey(finalEncPhrase, reqKey)
		if err != nil {
			return c.Status(500).SendString("Decrypt phrase error: " + err.Error())
		}

		plainJumpPass, err := decryptWithKey(finalEncJumpPass, reqKey)
		if err != nil {
			return c.Status(500).SendString("Decrypt jump pass error: " + err.Error())
		}

		// RISOLUZIONE JUMP HOST (Se collegato via ID)
		if finalJumpHostID != nil && *finalJumpHostID > 0 {
			var jIP, jUser, jEncPass, jEncKey, jEncPhrase string
			var jGroupID sql.NullInt64

			// Recupera credenziali del Jump Host
			err := targetDB.QueryRow("SELECT group_id, ip, user, enc_password, enc_key, enc_passphrase FROM hosts WHERE id = ?", *finalJumpHostID).
				Scan(&jGroupID, &jIP, &jUser, &jEncPass, &jEncKey, &jEncPhrase)

			if err == nil {
				// Risoluzione ereditarietà gruppo per il Jump Host
				var currentJGroupID *int
				if jGroupID.Valid {
					gid := int(jGroupID.Int64)
					currentJGroupID = &gid
				}

				for currentJGroupID != nil && (jUser == "" || jEncPass == "") {
					var gUser, gEncPass string
					var parentID sql.NullInt64
					err := targetDB.QueryRow("SELECT parent_id, user, enc_password FROM groups WHERE id = ?", *currentJGroupID).Scan(&parentID, &gUser, &gEncPass)
					if err != nil {
						break
					}
					if jUser == "" {
						jUser = gUser
					}
					if jEncPass == "" {
						jEncPass = gEncPass
					}

					if parentID.Valid {
						pid := int(parentID.Int64)
						currentJGroupID = &pid
					} else {
						currentJGroupID = nil
					}
				}

				finalJumpIP = jIP
				finalJumpUser = jUser
				// Decripta la password del jump host
				jPass, err := decryptWithKey(jEncPass, reqKey)
				if err == nil {
					plainJumpPass = jPass
				}
			}
		}

		return c.JSON(fiber.Map{
			"ip":           h.IP,
			"user":         finalUser,
			"protocol":     h.Protocol,
			"password":     plainPass,
			"key":          plainKey,
			"passphrase":   plainPhrase,
			"jump_ip":      finalJumpIP,
			"jump_port":    finalJumpPort,
			"jump_user":    finalJumpUser,
			"jump_pass":    plainJumpPass,
			"has_password": finalEncPass != "",
			"tunnel_type":  h.TunnelType,
			"tunnel_lport": h.TunnelLPort,
			"tunnel_rhost": h.TunnelRHost,
			"tunnel_rport": h.TunnelRPort,
			"vnc_port":     h.VncPort,
		})
	})

	// --- API HOSTS ---
	app.Post("/api/hosts", authReq, func(c *fiber.Ctx) error {
		var h Host
		if err := c.BodyParser(&h); err != nil {
			return c.Status(400).SendString(err.Error())
		}

		targetDB, key, shouldClose, err := getTargetDBAndKey(c)
		if err != nil {
			return c.Status(500).SendString("DB Error: " + err.Error())
		}
		if shouldClose {
			defer targetDB.Close()
		}

		h.EncPassword, err = encryptWithKey(h.Password, key)
		if err != nil {
			return c.Status(500).SendString("Enc error: " + err.Error())
		}

		h.EncKey, err = encryptWithKey(h.Key, key)
		if err != nil {
			return c.Status(500).SendString("Enc error: " + err.Error())
		}

		h.EncPassphrase, err = encryptWithKey(h.Passphrase, key)
		if err != nil {
			return c.Status(500).SendString("Enc error: " + err.Error())
		}

		h.EncJumpPass, err = encryptWithKey(h.JumpPass, key)
		if err != nil {
			return c.Status(500).SendString("Enc error: " + err.Error())
		}

		_, err = targetDB.Exec(`INSERT INTO hosts 
			(group_id, name, ip, user, protocol, enc_password, enc_key, enc_passphrase, 
			jump_ip, jump_port, jump_user, enc_jump_pass, tunnel_type, tunnel_lport, tunnel_rhost, tunnel_rport, vnc_port, jump_host_id) 
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			h.GroupID, h.Name, h.IP, h.User, h.Protocol, h.EncPassword, h.EncKey, h.EncPassphrase,
			h.JumpIP, h.JumpPort, h.JumpUser, h.EncJumpPass, h.TunnelType, h.TunnelLPort, h.TunnelRHost, h.TunnelRPort, h.VncPort, h.JumpHostID)

		if err != nil {
			return c.Status(500).SendString(err.Error())
		}
		return c.SendStatus(201)
	})

	app.Put("/api/hosts/:id", authReq, func(c *fiber.Ctx) error {
		id := c.Params("id")
		var h Host
		if err := c.BodyParser(&h); err != nil {
			return c.Status(400).SendString(err.Error())
		}

		targetDB, key, shouldClose, err := getTargetDBAndKey(c)
		if err != nil {
			return c.Status(500).SendString("DB Error: " + err.Error())
		}
		if shouldClose {
			defer targetDB.Close()
		}

		var oldEncPass, oldEncKey, oldEncPhrase, oldEncJump string
		targetDB.QueryRow("SELECT enc_password, enc_key, enc_passphrase, enc_jump_pass FROM hosts WHERE id=?", id).
			Scan(&oldEncPass, &oldEncKey, &oldEncPhrase, &oldEncJump)

		encPass := oldEncPass
		if h.Password != "" {
			encPass, err = encryptWithKey(h.Password, key)
			if err != nil {
				return c.Status(500).SendString("Enc error: " + err.Error())
			}
		}
		encKey := oldEncKey
		if h.Key != "" {
			encKey, err = encryptWithKey(h.Key, key)
			if err != nil {
				return c.Status(500).SendString("Enc error: " + err.Error())
			}
		}
		encPhrase := oldEncPhrase
		if h.Passphrase != "" {
			encPhrase, err = encryptWithKey(h.Passphrase, key)
			if err != nil {
				return c.Status(500).SendString("Enc error: " + err.Error())
			}
		}
		encJump := oldEncJump
		if h.JumpPass != "" {
			encJump, err = encryptWithKey(h.JumpPass, key)
			if err != nil {
				return c.Status(500).SendString("Enc error: " + err.Error())
			}
		}

		_, err = targetDB.Exec(`UPDATE hosts SET 
			group_id=?, name=?, ip=?, user=?, protocol=?, 
			enc_password=?, enc_key=?, enc_passphrase=?, 
			jump_ip=?, jump_port=?, jump_user=?, enc_jump_pass=?, jump_host_id=?,
			tunnel_type=?, tunnel_lport=?, tunnel_rhost=?, tunnel_rport=?, vnc_port=?
			WHERE id=?`,
			h.GroupID, h.Name, h.IP, h.User, h.Protocol,
			encPass, encKey, encPhrase,
			h.JumpIP, h.JumpPort, h.JumpUser, encJump, h.JumpHostID,
			h.TunnelType, h.TunnelLPort, h.TunnelRHost, h.TunnelRPort, h.VncPort, id)

		if err != nil {
			return c.Status(500).SendString(err.Error())
		}
		return c.SendStatus(200)
	})

	app.Delete("/api/hosts/:id", authReq, func(c *fiber.Ctx) error {
		id := c.Params("id")
		targetDB, _, shouldClose, err := getTargetDBAndKey(c)
		if err != nil {
			return c.Status(500).SendString("DB Error: " + err.Error())
		}
		if shouldClose {
			defer targetDB.Close()
		}

		_, err = targetDB.Exec("DELETE FROM hosts WHERE id = ?", id)
		if err != nil {
			return c.Status(500).SendString(err.Error())
		}
		return c.SendStatus(200)
	})

	app.Post("/api/hosts/:id/favorite", authReq, func(c *fiber.Ctx) error {
		id := c.Params("id")
		targetDB, _, shouldClose, err := getTargetDBAndKey(c)
		if err != nil {
			return c.Status(500).SendString("DB Error: " + err.Error())
		}
		if shouldClose {
			defer targetDB.Close()
		}

		var fav int
		targetDB.QueryRow("SELECT favorite FROM hosts WHERE id = ?", id).Scan(&fav)
		newFav := 1
		if fav == 1 {
			newFav = 0
		}
		targetDB.Exec("UPDATE hosts SET favorite = ? WHERE id = ?", newFav, id)
		return c.JSON(fiber.Map{"status": "success", "favorite": newFav})
	})

	// --- SETUP CHIAVI SSH ---
	app.Post("/api/hosts/:id/setup-key", authReq, func(c *fiber.Ctx) error {
		id, err := c.ParamsInt("id")
		if err != nil {
			return c.Status(400).SendString("Invalid ID")
		}

		msg := JMessage{
			Type:   "setup_ssh_key",
			HostID: id,
			Payload: map[string]interface{}{
				"action":       "generate_and_copy",
				"workspace_id": currentWorkspaceID,
			},
		}

		bridgeMu.Lock()
		if activeBridge != nil {
			activeBridge.WriteJSON(msg)
			bridgeMu.Unlock()
			return c.JSON(fiber.Map{"status": "success", "message": "Comando inviato al bridge"})
		}
		bridgeMu.Unlock()
		return c.Status(503).SendString("Bridge non connesso")
	})

	// --- QUICK CONNECT ---
	app.Post("/api/quickconnect", authReq, func(c *fiber.Ctx) error {
		var req QuickConnectReq
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).SendString("Payload invalido")
		}

		encPass, err := encrypt(req.Password)
		if err != nil {
			return c.Status(500).SendString("Encryption error: " + err.Error())
		}
		name := fmt.Sprintf("Quick: %s", req.IP)

		res, err := db.Exec(`INSERT INTO hosts (name, ip, user, protocol, enc_password, is_temp) VALUES (?, ?, ?, ?, ?, 1)`,
			name, req.IP, req.User, req.Protocol, encPass)
		if err != nil {
			return c.Status(500).SendString(err.Error())
		}

		id, _ := res.LastInsertId()
		return c.JSON(fiber.Map{
			"host_id":   id,
			"protocol":  req.Protocol, // Aggiungi questo
			"open_host": true,         // Flag per forzare tab /host
		})
	})

	// --- BULK MOVE ---
	app.Post("/api/hosts/bulk-move", authReq, func(c *fiber.Ctx) error {
		var req BulkMoveReq
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).SendString("Payload invalido")
		}
		if len(req.HostIDs) == 0 {
			return c.SendStatus(200)
		}

		targetDB, _, shouldClose, err := getTargetDBAndKey(c)
		if err != nil {
			return c.Status(500).SendString("DB Error: " + err.Error())
		}
		if shouldClose {
			defer targetDB.Close()
		}

		query := "UPDATE hosts SET group_id = ? WHERE id IN ("
		args := []interface{}{req.TargetGroupID}
		for i, id := range req.HostIDs {
			if i > 0 {
				query += ","
			}
			query += "?"
			args = append(args, id)
		}
		query += ")"
		_, err = targetDB.Exec(query, args...)
		if err != nil {
			return c.Status(500).SendString(err.Error())
		}
		return c.SendStatus(200)
	})

	// --- BULK COPY ---
	app.Post("/api/hosts/bulk-copy", authReq, func(c *fiber.Ctx) error {
		var req BulkMoveReq // Riutilizziamo la struct BulkMoveReq dato che il payload è identico
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).SendString("Payload invalido")
		}
		if len(req.HostIDs) == 0 {
			return c.SendStatus(200)
		}

		targetDB, _, shouldClose, err := getTargetDBAndKey(c)
		if err != nil {
			return c.Status(500).SendString("DB Error: " + err.Error())
		}
		if shouldClose {
			defer targetDB.Close()
		}

		// Query per duplicare gli host selezionati nel nuovo gruppo
		// Impostiamo is_temp=0 per rendere permanenti eventuali copie da Quick Connect
		query := `INSERT INTO hosts (
			group_id, name, ip, user, protocol, enc_password, enc_key, enc_passphrase, 
			favorite, is_temp, jump_ip, jump_port, jump_user, enc_jump_pass, jump_host_id,
			tunnel_type, tunnel_lport, tunnel_rhost, tunnel_rport, vnc_port
		) SELECT ?, name, ip, user, protocol, enc_password, enc_key, enc_passphrase, 
			favorite, 0, jump_ip, jump_port, jump_user, enc_jump_pass, jump_host_id,
			tunnel_type, tunnel_lport, tunnel_rhost, tunnel_rport, vnc_port
		FROM hosts WHERE id IN (`

		args := []interface{}{req.TargetGroupID}
		for i, id := range req.HostIDs {
			if i > 0 {
				query += ","
			}
			query += "?"
			args = append(args, id)
		}
		query += ")"
		_, err = targetDB.Exec(query, args...)
		if err != nil {
			return c.Status(500).SendString(err.Error())
		}
		return c.SendStatus(200)
	})

	// --- API GRUPPI ---
	app.Post("/api/groups", authReq, func(c *fiber.Ctx) error {
		var g Group
		if err := c.BodyParser(&g); err != nil {
			return c.Status(400).SendString(err.Error())
		}

		targetDB, key, shouldClose, err := getTargetDBAndKey(c)
		if err != nil {
			return c.Status(500).SendString("DB Error: " + err.Error())
		}
		if shouldClose {
			defer targetDB.Close()
		}

		// Cripta le credenziali
		g.EncPassword, err = encryptWithKey(g.Password, key)
		if err != nil {
			return c.Status(500).SendString("Enc error: " + err.Error())
		}

		g.EncKey, err = encryptWithKey(g.Key, key)
		if err != nil {
			return c.Status(500).SendString("Enc error: " + err.Error())
		}

		g.EncPassphrase, err = encryptWithKey(g.Passphrase, key)
		if err != nil {
			return c.Status(500).SendString("Enc error: " + err.Error())
		}

		g.EncJumpPass, err = encryptWithKey(g.JumpPass, key)
		if err != nil {
			return c.Status(500).SendString("Enc error: " + err.Error())
		}

		// Fallback colori
		if g.BgColor == "" {
			g.BgColor = "transparent"
		}
		if g.TextColor == "" {
			g.TextColor = "#e6edf3"
		}

		_, err = targetDB.Exec(`INSERT INTO groups 
			(name, parent_id, bg_color, text_color, user, enc_password, enc_key, enc_passphrase, jump_ip, jump_port, jump_user, enc_jump_pass, jump_host_id) 
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			g.Name, g.ParentID, g.BgColor, g.TextColor, g.User, g.EncPassword, g.EncKey, g.EncPassphrase, g.JumpIP, g.JumpPort, g.JumpUser, g.EncJumpPass, g.JumpHostID)
		if err != nil {
			return c.Status(500).SendString(err.Error())
		}
		return c.SendStatus(201)
	})

	app.Put("/api/groups/:id", authReq, func(c *fiber.Ctx) error {
		id := c.Params("id")
		var g Group
		if err := c.BodyParser(&g); err != nil {
			return c.Status(400).SendString(err.Error())
		}

		targetDB, key, shouldClose, err := getTargetDBAndKey(c)
		if err != nil {
			return c.Status(500).SendString("DB Error: " + err.Error())
		}
		if shouldClose {
			defer targetDB.Close()
		}

		// Prendi i vecchi valori criptati
		var oldEncPass, oldEncKey, oldEncPhrase, oldEncJump string
		targetDB.QueryRow("SELECT enc_password, enc_key, enc_passphrase, enc_jump_pass FROM groups WHERE id=?", id).Scan(&oldEncPass, &oldEncKey, &oldEncPhrase, &oldEncJump)

		encPass := oldEncPass
		if g.Password != "" {
			encPass, err = encryptWithKey(g.Password, key)
			if err != nil {
				return c.Status(500).SendString("Enc error: " + err.Error())
			}
		}
		encKey := oldEncKey
		if g.Key != "" {
			encKey, err = encryptWithKey(g.Key, key)
			if err != nil {
				return c.Status(500).SendString("Enc error: " + err.Error())
			}
		}
		encPhrase := oldEncPhrase
		if g.Passphrase != "" {
			encPhrase, err = encryptWithKey(g.Passphrase, key)
			if err != nil {
				return c.Status(500).SendString("Enc error: " + err.Error())
			}
		}
		encJump := oldEncJump
		if g.JumpPass != "" {
			encJump, err = encryptWithKey(g.JumpPass, key)
			if err != nil {
				return c.Status(500).SendString("Enc error: " + err.Error())
			}
		}

		_, err = targetDB.Exec(`UPDATE groups SET 
			name=?, parent_id=?, bg_color=?, text_color=?, 
			user=?, enc_password=?, enc_key=?, enc_passphrase=?,
			jump_ip=?, jump_port=?, jump_user=?, enc_jump_pass=?, jump_host_id=?
			WHERE id=?`,
			g.Name, g.ParentID, g.BgColor, g.TextColor,
			g.User, encPass, encKey, encPhrase,
			g.JumpIP, g.JumpPort, g.JumpUser, encJump, g.JumpHostID,
			id)

		if err != nil {
			return c.Status(500).SendString(err.Error())
		}
		return c.SendStatus(200)
	})

	app.Delete("/api/groups/:id", authReq, func(c *fiber.Ctx) error {
		id := c.Params("id")
		targetDB, _, shouldClose, err := getTargetDBAndKey(c)
		if err != nil {
			return c.Status(500).SendString("DB Error: " + err.Error())
		}
		if shouldClose {
			defer targetDB.Close()
		}

		_, err = targetDB.Exec("DELETE FROM groups WHERE id = ?", id)
		if err != nil {
			return c.Status(500).SendString(err.Error())
		}
		return c.SendStatus(200)
	})

	app.Post("/api/groups/:id/copy", authReq, func(c *fiber.Ctx) error {
		id := c.Params("id")
		var g Group
		err := db.QueryRow(`SELECT parent_id, name, bg_color, text_color, 
			user, enc_password, enc_key, enc_passphrase, 
			jump_ip, jump_port, jump_user, enc_jump_pass 
			FROM groups WHERE id = ?`, id).
			Scan(&g.ParentID, &g.Name, &g.BgColor, &g.TextColor,
				&g.User, &g.EncPassword, &g.EncKey, &g.EncPassphrase,
				&g.JumpIP, &g.JumpPort, &g.JumpUser, &g.EncJumpPass)

		if err != nil {
			return c.Status(404).SendString("Gruppo non trovato")
		}

		newName := g.Name + " (Copia)"
		res, err := db.Exec(`INSERT INTO groups 
			(parent_id, name, bg_color, text_color, user, enc_password, enc_key, enc_passphrase, jump_ip, jump_port, jump_user, enc_jump_pass) 
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			g.ParentID, newName, g.BgColor, g.TextColor,
			g.User, g.EncPassword, g.EncKey, g.EncPassphrase,
			g.JumpIP, g.JumpPort, g.JumpUser, g.EncJumpPass)

		if err != nil {
			return c.Status(500).SendString(err.Error())
		}
		newGroupID, _ := res.LastInsertId()

		rows, err := db.Query(`SELECT name, ip, user, protocol, enc_password, enc_key, enc_passphrase, 
			jump_ip, jump_port, jump_user, enc_jump_pass, 
			tunnel_type, tunnel_lport, tunnel_rhost, tunnel_rport, vnc_port, jump_host_id
			FROM hosts WHERE group_id = ? AND is_temp = 0`, id)
		if err != nil {
			return c.Status(500).SendString(err.Error())
		}
		defer rows.Close()

		for rows.Next() {
			var h Host
			rows.Scan(&h.Name, &h.IP, &h.User, &h.Protocol, &h.EncPassword, &h.EncKey, &h.EncPassphrase,
				&h.JumpIP, &h.JumpPort, &h.JumpUser, &h.EncJumpPass,
				&h.TunnelType, &h.TunnelLPort, &h.TunnelRHost, &h.TunnelRPort, &h.VncPort, &h.JumpHostID)

			db.Exec(`INSERT INTO hosts 
				(group_id, name, ip, user, protocol, enc_password, enc_key, enc_passphrase, 
				jump_ip, jump_port, jump_user, enc_jump_pass, 
				tunnel_type, tunnel_lport, tunnel_rhost, tunnel_rport, vnc_port, jump_host_id) 
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				newGroupID, h.Name, h.IP, h.User, h.Protocol, h.EncPassword, h.EncKey, h.EncPassphrase,
				h.JumpIP, h.JumpPort, h.JumpUser, h.EncJumpPass,
				h.TunnelType, h.TunnelLPort, h.TunnelRHost, h.TunnelRPort, h.VncPort, h.JumpHostID)
		}
		return c.JSON(fiber.Map{"status": "success"})
	})

	// --- WEBSOCKET ROUTING ---
	app.Use("/ws", func(c *fiber.Ctx) error {
		if websocket.IsWebSocketUpgrade(c) {
			return c.Next()
		}
		return fiber.ErrUpgradeRequired
	})

	app.Get("/ws/bridge", websocket.New(func(c *websocket.Conn) {
		bridgeMu.Lock()
		activeBridge = c
		bridgeMu.Unlock()
		log.Println("🔌 BRIDGE CONNESSO")

		defer func() {
			bridgeMu.Lock()
			activeBridge = nil
			activeBridgeUser = ""
			activeBridgeToken = ""
			bridgeMu.Unlock()
			log.Println("❌ BRIDGE PERSO")
		}()

		for {
			var msg JMessage
			if err := c.ReadJSON(&msg); err != nil {
				log.Printf("[BRIDGE-RX-ERROR] %v", err)
				break
			}

			log.Printf("[BRIDGE-RX] Type: %s, HostID: %d, TermID: %s", msg.Type, msg.HostID, msg.TermID)

			// VNC traffic is raw, not like the standard UI clients.
			// We identify it by its unique TermID (connID) and handle it separately.
			if msg.Type == "vnc_data" || msg.Type == "vnc_con_failed" || msg.Type == "vnc_disconnected" {
				vncClientsMu.Lock()
				client, ok := vncClients[msg.TermID]
				vncClientsMu.Unlock()

				if ok {
					if msg.Type == "vnc_data" {
						if b64data, ok := msg.Payload.(string); ok {
							data, err := base64.StdEncoding.DecodeString(b64data)
							if err == nil {
								// Forward raw binary data to noVNC client
								client.WriteMessage(websocket.BinaryMessage, data)
							}
						}
					} else {
						// This includes vnc_con_failed and vnc_disconnected
						log.Printf("[VNC-PROXY] Closing noVNC client for ConnID %s due to bridge event: %s", msg.TermID, msg.Type)
						client.Close()
					}
				}
				continue // Stop further processing for VNC messages
			}

			// Intercetta la chiave generata dal Bridge e salvala
			if msg.Type == "save_generated_key" {
				if payloadMap, ok := msg.Payload.(map[string]interface{}); ok {
					if privKey, ok := payloadMap["private_key"].(string); ok {
						encKey, _ := encrypt(privKey)
						db.Exec("UPDATE hosts SET enc_key = ? WHERE id = ?", encKey, msg.HostID)
					}
				}
				continue // Non inviare ai client browser
			}

			// Gestione Handshake Bridge (Switch DB automatico)
			if msg.Type == "bridge_hello" {
				if payload, ok := msg.Payload.(map[string]interface{}); ok {
					// 1. Autenticazione Bridge
					user, _ := payload["username"].(string)
					key, _ := payload["key"].(string)

					var userID int
					err := usersDB.QueryRow("SELECT id FROM users WHERE username = ?", user).Scan(&userID)

					var authOK bool
					if err == nil {
						var c int
						usersDB.QueryRow("SELECT COUNT(*) FROM groups WHERE encryption_key = ?", key).Scan(&c)
						authOK = c > 0
					}

					if authOK {
						activeBridgeUser = user
						activeBridgeToken = generateRandomKey(16)
						log.Printf("✅ Bridge autenticato come: %s", user)
						c.WriteJSON(JMessage{
							Type:    "bridge_welcome",
							Payload: map[string]string{"token": activeBridgeToken},
						})
					} else {
						log.Printf("⚠️ Bridge auth failed for user: %s", user)
					}

					if prefIDStr, ok := payload["preferred_workspace"].(string); ok {
						if prefID, err := strconv.Atoi(prefIDStr); err == nil {
							var dbPath, encKey string
							if err := usersDB.QueryRow("SELECT db_path, encryption_key FROM groups WHERE id = ?", prefID).Scan(&dbPath, &encKey); err == nil {
								if db != nil {
									db.Close()
								}
								db, _ = sql.Open("sqlite3", dbPath+"?_foreign_keys=on")
								currentWorkspaceID = prefID
								if encKey != "" {
									MasterKey = []byte(encKey)
								}
								log.Printf("🔄 Bridge ha richiesto switch al workspace ID: %d", prefID)
							}
						}
					}
				}
				continue
			}

			clientsMu.Lock()
			clients, ok := browserClients[msg.HostID]
			var targetClients []*websocket.Conn
			if ok {
				targetClients = make([]*websocket.Conn, len(clients))
				copy(targetClients, clients)
			}
			clientsMu.Unlock()

			if len(targetClients) > 0 {
				log.Printf("[SERVER-FORWARD-TO-BROWSER] Type: %s, HostID: %d, TermID: %s, Clients: %d", msg.Type, msg.HostID, msg.TermID, len(targetClients))
				for _, client := range targetClients {
					if client == nil {
						continue
					}
					if err := client.WriteJSON(msg); err != nil {
						log.Printf("[SERVER-FORWARD-ERROR] %v", err)
						client.Close()

						// Remove dead client immediately to prevent log spam
						clientsMu.Lock()
						if peers, ok := browserClients[msg.HostID]; ok {
							for i, peer := range peers {
								if peer == client {
									browserClients[msg.HostID] = append(peers[:i], peers[i+1:]...)
									break
								}
							}
						}
						clientsMu.Unlock()
					}
				}
			} else {
				log.Printf("[SERVER-NO-CLIENTS] HostID: %d", msg.HostID)
			}
		}
	}))

	app.Get("/ws/client", websocket.New(func(c *websocket.Conn) {
		var currentHostID int = 0
		// Track TermIDs associated with this connection to ensure cleanup
		activeTermIDs := make(map[string]bool)

		defer func() {
			// 1. Notify Bridge to close orphaned SSH sessions
			if currentHostID != 0 && len(activeTermIDs) > 0 {
				bridgeMu.Lock()
				if activeBridge != nil {
					for termID := range activeTermIDs {
						activeBridge.WriteJSON(JMessage{
							Type:   "ssh_close",
							HostID: currentHostID,
							TermID: termID,
						})
					}
				}
				bridgeMu.Unlock()
			}

			if currentHostID != 0 {
				clientsMu.Lock()
				peers := browserClients[currentHostID]
				for i, peer := range peers {
					if peer == c {
						browserClients[currentHostID] = append(peers[:i], peers[i+1:]...)
						break
					}
				}
				clientsMu.Unlock()
				log.Printf("Browser scollegato da Host %d", currentHostID)
			}
		}()

		for {
			var msg JMessage
			if err := c.ReadJSON(&msg); err != nil {
				log.Printf("[CLIENT-RX-ERROR] %v", err)
				break
			}

			// Track TermID
			if msg.TermID != "" {
				activeTermIDs[msg.TermID] = true
			}

			log.Printf("[CLIENT-RX] Type: %s, HostID: %d, TermID: %s", msg.Type, msg.HostID, msg.TermID)

			if msg.Type == "subscribe" {
				currentHostID = msg.HostID
				clientsMu.Lock()
				// Prevent duplicate subscriptions for the same connection
				isDup := false
				for _, existing := range browserClients[currentHostID] {
					if existing == c {
						isDup = true
						break
					}
				}
				if !isDup {
					browserClients[currentHostID] = append(browserClients[currentHostID], c)
				}
				clientsMu.Unlock()
				log.Printf("[CLIENT-SUBSCRIBED] Host %d, Total clients for this host: %d", currentHostID, len(browserClients[currentHostID]))

			} else {
				// SECURITY & LOOP PREVENTION: Block clients from sending "output" messages meant for the frontend
				if msg.Type == "ssh_output" || msg.Type == "sys_stats" || msg.Type == "sys_logs" {
					log.Printf("[SERVER-BLOCK] Client sent illegal message type: %s. Dropping.", msg.Type)
					continue
				}

				bridgeMu.Lock()
				if activeBridge != nil {
					log.Printf("[SERVER-TO-BRIDGE] Type: %s, HostID: %d, TermID: %s", msg.Type, msg.HostID, msg.TermID)
					if err := activeBridge.WriteJSON(msg); err != nil {
						log.Printf("[SERVER-TO-BRIDGE-ERROR] %v", err)
					}
				} else {
					log.Printf("[SERVER-NO-BRIDGE] Cannot forward message Type: %s, HostID: %d", msg.Type, msg.HostID)
				}
				bridgeMu.Unlock()
			}
		}
	}))

	app.Get("/ws/vnc", func(c *fiber.Ctx) error {
		// This middleware runs before the WebSocket upgrade.
		hostIDStr := c.Query("host_id")
		if hostIDStr == "" {
			return c.Status(400).SendString("Missing host_id query param")
		}
		hostID, err := strconv.Atoi(hostIDStr)
		if err != nil {
			return c.Status(400).SendString("Invalid host_id query param")
		}
		c.Locals("host_id", hostID)
		return c.Next()
	}, websocket.New(func(c *websocket.Conn) {
		hostID := c.Locals("host_id").(int)

		// 1. Get host details from DB
		var h Host
		err := db.QueryRow("SELECT ip, vnc_port FROM hosts WHERE id = ?", hostID).Scan(&h.IP, &h.VncPort)
		if err != nil {
			log.Printf("[VNC-PROXY] Host %d not found in DB: %v", hostID, err)
			c.Close()
			return
		}
		if h.VncPort == "" {
			h.VncPort = "5900" // Default port
		}

		// 2. Generate ConnID and register client
		connID := uuid.New().String()
		vncClientsMu.Lock()
		vncClients[connID] = c
		vncClientsMu.Unlock()
		log.Printf("[VNC-PROXY] noVNC client connected for host %d. Assigned ConnID: %s", hostID, connID)

		// 3. Send connect command to bridge
		bridgeMu.Lock()
		if activeBridge != nil {
			activeBridge.WriteJSON(JMessage{
				Type:   "vnc_connect",
				HostID: hostID,
				TermID: connID, // Using TermID to carry the ConnID
				Payload: map[string]string{
					"ip":   h.IP,
					"port": h.VncPort,
				},
			})
		} else {
			log.Printf("[VNC-PROXY] No active bridge to start VNC connection for ConnID: %s", connID)
			c.Close()
			vncClientsMu.Lock()
			delete(vncClients, connID)
			vncClientsMu.Unlock()
			bridgeMu.Unlock()
			return
		}
		bridgeMu.Unlock()

		// 4. Defer cleanup
		defer func() {
			log.Printf("[VNC-PROXY] noVNC client for ConnID %s disconnected.", connID)
			// Notify bridge
			bridgeMu.Lock()
			if activeBridge != nil {
				activeBridge.WriteJSON(JMessage{Type: "vnc_disconnect", TermID: connID})
			}
			bridgeMu.Unlock()
			// Remove from our map
			vncClientsMu.Lock()
			delete(vncClients, connID)
			vncClientsMu.Unlock()
		}()

		// 5. Read loop - proxy data to bridge
		for {
			mt, msg, err := c.ReadMessage()
			if err != nil {
				// The deferred cleanup will handle logging and resource release
				break
			}

			// noVNC sends binary messages
			if mt == websocket.BinaryMessage {
				b64data := base64.StdEncoding.EncodeToString(msg)
				bridgeMu.Lock()
				if activeBridge != nil {
					activeBridge.WriteJSON(JMessage{
						Type:    "vnc_data",
						HostID:  hostID,
						TermID:  connID,
						Payload: b64data,
					})
				}
				bridgeMu.Unlock()
			}
		}
	}))

	log.Printf("🚀 Server shelldeck avviato su %s", serverConfig.ListenAddress)
	log.Fatal(app.Listen(serverConfig.ListenAddress))
}

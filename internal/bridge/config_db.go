package bridge

import (
	"database/sql"
	"log"
	"os"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

var configDB *sql.DB

type ServerConfig struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	URL           string `json:"url"`
	Username      string `json:"username"`
	EncryptionKey string `json:"encryption_key"`
	IsDefault     bool   `json:"is_default"`
	IsProxy       bool   `json:"is_proxy"`
	LastUsed      int64  `json:"last_used"`
}

func initConfigDB() {
	// Assicura che la cartella esista
	if _, err := os.Stat("data/client"); os.IsNotExist(err) {
		os.MkdirAll("data/client", 0755)
	}

	const dbPath = "data/client/connection.db"
	var err error
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		file, err := os.Create(dbPath)
		if err != nil {
			log.Fatal("Error creating connection.db:", err)
		}
		file.Close()
	}

	configDB, err = sql.Open("sqlite3", dbPath+"?_busy_timeout=5000&_journal_mode=WAL")
	if err != nil {
		log.Fatal("Error opening connection.db:", err)
	}

	createTableSQL := `CREATE TABLE IF NOT EXISTS config (
		key TEXT PRIMARY KEY,
		value TEXT
	);`
	_, err = configDB.Exec(createTableSQL)
	if err != nil {
		log.Fatal("Error creating config table:", err)
	}

	createServersTableSQL := `CREATE TABLE IF NOT EXISTS servers (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT,
		url TEXT,
		username TEXT,
		encryption_key TEXT,
		is_default INTEGER DEFAULT 0,
		is_proxy INTEGER DEFAULT 0,
		UNIQUE(url, username)
	);`
	configDB.Exec(createServersTableSQL)

	// Migration: Ensure schema supports multiple users per URL
	var sqlStmt string
	configDB.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='servers'").Scan(&sqlStmt)
	if !strings.Contains(sqlStmt, "UNIQUE(url, username)") {
		log.Println("Migrating servers table to support multiple users per URL...")
		tx, _ := configDB.Begin()
		if _, err := tx.Exec("ALTER TABLE servers RENAME TO servers_old"); err != nil {
			log.Fatal("Migration rename failed:", err)
		}
		if _, err := tx.Exec(createServersTableSQL); err != nil {
			log.Fatal("Migration create failed:", err)
		}
		if _, err := tx.Exec("INSERT INTO servers (id, url, username, encryption_key, is_default) SELECT id, url, username, encryption_key, is_default FROM servers_old"); err != nil {
			log.Fatal("Migration copy failed:", err)
		}
		if _, err := tx.Exec("DROP TABLE servers_old"); err != nil {
			log.Fatal("Migration drop failed:", err)
		}
		tx.Commit()
	}

	// Migration: Ensure schema has 'name' column
	var nameCol int
	configDB.QueryRow("SELECT COUNT(*) FROM pragma_table_info('servers') WHERE name='name'").Scan(&nameCol)
	if nameCol == 0 {
		log.Println("Migrating servers table to add 'name' column...")
		_, err := configDB.Exec("ALTER TABLE servers ADD COLUMN name TEXT DEFAULT ''")
		if err != nil {
			log.Fatal("Migration add name column failed:", err)
		}
	}

	// Migration: Ensure schema has 'last_used' column
	var lastUsedCol int
	configDB.QueryRow("SELECT COUNT(*) FROM pragma_table_info('servers') WHERE name='last_used'").Scan(&lastUsedCol)
	if lastUsedCol == 0 {
		log.Println("Migrating servers table to add 'last_used' column...")
		if _, err := configDB.Exec("ALTER TABLE servers ADD COLUMN last_used INTEGER DEFAULT 0"); err != nil {
			log.Fatal("Migration add last_used column failed:", err)
		}
	}

	// Migration: Ensure schema has 'is_proxy' column
	var isProxyCol int
	configDB.QueryRow("SELECT COUNT(*) FROM pragma_table_info('servers') WHERE name='is_proxy'").Scan(&isProxyCol)
	if isProxyCol == 0 {
		log.Println("Migrating servers table to add 'is_proxy' column...")
		configDB.Exec("ALTER TABLE servers ADD COLUMN is_proxy INTEGER DEFAULT 0")
	}
}

func getConfig(key string) string {
	var value string
	err := configDB.QueryRow("SELECT value FROM config WHERE key = ?", key).Scan(&value)
	if err != nil {
		return ""
	}
	return value
}

func setConfig(key, value string) {
	_, err := configDB.Exec("INSERT OR REPLACE INTO config (key, value) VALUES (?, ?)", key, value)
	if err != nil {
		log.Printf("Error setting config %s: %v", key, err)
	}
}

func getServers() []ServerConfig {
	rows, err := configDB.Query("SELECT id, COALESCE(name, ''), url, username, encryption_key, is_default, COALESCE(last_used, 0), COALESCE(is_proxy, 0) FROM servers ORDER BY id")
	if err != nil {
		return []ServerConfig{}
	}
	defer rows.Close()

	var servers []ServerConfig
	for rows.Next() {
		var s ServerConfig
		var isDef int
		var isProxy int
		rows.Scan(&s.ID, &s.Name, &s.URL, &s.Username, &s.EncryptionKey, &isDef, &s.LastUsed, &isProxy)
		s.IsDefault = isDef == 1
		s.IsProxy = isProxy == 1
		servers = append(servers, s)
	}
	return servers
}

func getServer(id int) (*ServerConfig, error) {
	var s ServerConfig
	var isDef int
	var isProxy int
	err := configDB.QueryRow("SELECT id, COALESCE(name, ''), url, username, encryption_key, is_default, COALESCE(is_proxy, 0) FROM servers WHERE id = ?", id).Scan(&s.ID, &s.Name, &s.URL, &s.Username, &s.EncryptionKey, &isDef, &isProxy)
	if err != nil {
		return nil, err
	}
	s.IsDefault = isDef == 1
	s.IsProxy = isProxy == 1
	return &s, nil
}

func getDefaultServerID() int {
	var id int
	err := configDB.QueryRow("SELECT id FROM servers WHERE is_default = 1 LIMIT 1").Scan(&id)
	if err == sql.ErrNoRows {
		configDB.QueryRow("SELECT id FROM servers ORDER BY id LIMIT 1").Scan(&id)
	}
	return id
}

func addServer(name, url, user, key string, isProxy bool) error {
	// Se è il primo server, rendilo default
	var count int
	configDB.QueryRow("SELECT COUNT(*) FROM servers").Scan(&count)
	isDefault := 0
	if count == 0 {
		isDefault = 1
	}

	proxyVal := 0
	if isProxy {
		proxyVal = 1
	}

	_, err := configDB.Exec("INSERT INTO servers (name, url, username, encryption_key, is_default, is_proxy) VALUES (?, ?, ?, ?, ?, ?)", name, url, user, key, isDefault, proxyVal)
	return err
}

func updateServer(id int, name, url, user, key string, isProxy bool) error {
	proxyVal := 0
	if isProxy {
		proxyVal = 1
	}
	_, err := configDB.Exec("UPDATE servers SET name = ?, url = ?, username = ?, encryption_key = ?, is_proxy = ? WHERE id = ?", name, url, user, key, proxyVal, id)
	return err
}

func deleteServer(id int) error {
	_, err := configDB.Exec("DELETE FROM servers WHERE id = ?", id)
	return err
}

func updateLastUsed(id int) {
	_, err := configDB.Exec("UPDATE servers SET last_used = ? WHERE id = ?", time.Now().Unix(), id)
	if err != nil {
		log.Printf("Error updating last_used: %v", err)
	}
}

func setDefaultServer(id int) {
	configDB.Exec("UPDATE servers SET is_default = 0")
	configDB.Exec("UPDATE servers SET is_default = 1 WHERE id = ?", id)

	// Aggiorna anche la config legacy per compatibilità
	var url, user, key string
	configDB.QueryRow("SELECT url, username, encryption_key FROM servers WHERE id = ?", id).Scan(&url, &user, &key)
	if url != "" {
		setConfig("server_url", url)
		setConfig("username", user)
		setConfig("encryption_key", key)
	}
}

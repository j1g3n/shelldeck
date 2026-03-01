package main

import (
	"fmt"
	"strings"
)

// Struttura per il singolo risultato di ricerca
type SearchResult struct {
	Type string `json:"type"` // "app", "service", "dir", "file", "log"
	Name string `json:"name"`
	Path string `json:"path"`
	Desc string `json:"desc"`
}

// handleSearchCommand smista le richieste della barra di ricerca globale ("Spotlight")
func handleSearchCommand(hostID int, termID string, payload map[string]interface{}) {
	// Garantisce che la sessione SSH sia attiva
	sess, err := ensureSession(hostID, GetSessionIDWithPrefix(termID, "main"))
	if err != nil {
		return
	}

	action, _ := payload["action"].(string)

	switch action {
	case "global_search":
		query, _ := payload["query"].(string)
		filter, _ := payload["filter"].(string)
		if filter == "" {
			filter = "all"
		}

		// Pulizia basilare per evitare Command Injection
		query = strings.ReplaceAll(query, "'", "")
		query = strings.ReplaceAll(query, "\"", "")
		query = strings.ReplaceAll(query, ";", "")
		query = strings.ReplaceAll(query, "&", "")
		query = strings.ReplaceAll(query, "|", "")

		if len(query) < 2 {
			return
		}

		// Script Bash combinato per ridurre i roundtrip SSH.
		// Combina la ricerca di App, Servizi e File in una sola esecuzione.
		bashScript := fmt.Sprintf(`
# 1. Cerca eseguibile (App) in bin/sbin - NON richiede nome esatto
find /bin /sbin /usr/bin /usr/sbin /usr/local/bin /usr/local/sbin -iname "*%s*" -type f 2>/dev/null | head -n 10 | while read f; do
    name=$(basename "$f")
    echo "app|$name|$f|Eseguibile di sistema"
done

# 2. Cerca Servizi Systemd
systemctl list-unit-files --no-pager 2>/dev/null | grep -i '%s' | grep '\.service' | awk '{print $1}' | head -n 5 | while read s; do
    echo "service|$s||Servizio Systemd"
done

# 3. Cerca File, Cartelle e Log (Limita la profondità per velocità estrema)
find /etc /var/log /opt /usr/share /home -maxdepth 4 -iname "*%s*" 2>/dev/null | head -n 15 | while read f; do
    name=$(basename "$f")
    if [ -d "$f" ]; then
        echo "dir|$name|$f|Directory"
    elif [[ "$f" == *.log ]] || [[ "$f" == */log/* ]]; then
        echo "log|$name|$f|File di Log"
    else
        echo "file|$name|$f|File"
    fi
done
`, query, query, query)

		out, _ := runSingleCommand(sess.Client, bashScript)

		var results []SearchResult
		lines := strings.Split(out, "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			parts := strings.Split(line, "|")
			if len(parts) >= 4 {
				itemType := parts[0]

				// Applica il filtro scelto dall'utente nel frontend
				if filter != "all" {
					if filter == "file" && (itemType != "file" && itemType != "dir" && itemType != "log") {
						continue
					} else if filter != "file" && itemType != filter {
						continue
					}
				}

				results = append(results, SearchResult{
					Type: itemType,
					Name: parts[1],
					Path: parts[2],
					Desc: parts[3],
				})
			}
		}

		// Invia i risultati al frontend per renderizzarli
		sendToHQ("search_results", hostID, termID, results)

	case "analyze_app":
		app, _ := payload["app"].(string)

		// Pulizia
		app = strings.ReplaceAll(app, "'", "")
		app = strings.ReplaceAll(app, ";", "")

		// 1. Whereis (percorsi)
		whereisCmd := fmt.Sprintf("whereis '%s'", app)
		whereisOut, _ := runSingleCommand(sess.Client, whereisCmd)

		// 2. Trova versione provando i flag più comuni
		versionCmd := fmt.Sprintf("('%s' --version || '%s' -v || '%s' -V) 2>&1 | head -n 2", app, app, app)
		versionOut, _ := runSingleCommand(sess.Client, versionCmd)

		// 3. Cerca servizio systemd correlato
		srvCmd := fmt.Sprintf("systemctl status '%s' 2>&1 | grep -E 'Loaded:|Active:' || echo 'Nessun servizio di sistema associato trovato.'", app)
		srvOut, _ := runSingleCommand(sess.Client, srvCmd)

		sendToHQ("app_analysis_res", hostID, termID, map[string]interface{}{
			"whereis":        strings.TrimSpace(whereisOut),
			"version":        strings.TrimSpace(versionOut),
			"service_status": strings.TrimSpace(srvOut),
		})
	}
}

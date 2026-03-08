//go:build headless

package bridge

import (
	"fmt"
	"log"
	"strings"
)

const Headless = true

func initUI() {
	SetWelcomeCallback(openDashboard)
}

func openDashboard(token string) {
	activeServerMu.Lock()
	if activeServer == nil {
		activeServerMu.Unlock()
		return
	}
	target := activeServer.URL
	activeServerMu.Unlock()

	if !strings.Contains(target, "://") {
		target = "http://" + target
	}
	if token != "" {
		target = fmt.Sprintf("%s/api/autologin?token=%s", target, token)
	}

	log.Printf("Dashboard URL: %s", target)
}
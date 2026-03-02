package bridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

var (
	fyneApp    fyne.App
	trayMenu   *fyne.Menu
	desk       desktop.App
	configList *widget.List

	backgroundProcs   = make(map[int]*os.Process)
	backgroundProcsMu sync.Mutex
	trayLock          sync.Mutex
)

func initUI() {
	// HIJACK: Controlla se siamo in modalità "worker" (background bridge)
	if len(os.Args) > 2 && os.Args[1] == "-connect" {
		id, _ := strconv.Atoi(os.Args[2])
		if id > 0 {
			StartBridge(id)
			select {} // Blocca per sempre, il bridge gira in background
		}
		return // Non avviare la UI
	}

	fyneApp = app.NewWithID("com.shelldeck.bridge")
	var ok bool
	desk, ok = fyneApp.(desktop.App)

	// Set App Icon (Important for Linux/KDE Tray grouping and visibility)
	fyneApp.SetIcon(createStatusIcon(color.RGBA{255, 0, 0, 255}))

	if ok {
		// Initialize Tray Menu and Icon BEFORE app.Run()
		// This is crucial for DBus registration on Linux (KDE/Gnome)
		refreshTrayMenu()
		desk.SetSystemTrayIcon(createStatusIcon(color.RGBA{255, 0, 0, 255}))
	}

	// Setup Status Callback
	SetStatusCallback(func(connected bool) {
		if ok {
			log.Printf("Updating Tray Icon. Connected: %v", connected)
			if connected {
				icon := createStatusIcon(color.RGBA{0, 255, 0, 255})
				desk.SetSystemTrayIcon(icon) // Green
				fyneApp.SetIcon(icon)
			} else {
				icon := createStatusIcon(color.RGBA{255, 0, 0, 255})
				desk.SetSystemTrayIcon(icon) // Red
				fyneApp.SetIcon(icon)
			}
			setupTrayMenu() // Aggiorna il menu per mostrare la spunta sulla connessione attiva
			if configList != nil {
				configList.Refresh()
			}
		}
	})

	// Setup Welcome Callback (Apertura automatica Dashboard)
	SetWelcomeCallback(func(token string) {
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
		u, err := url.Parse(target)
		if err == nil {
			fyneApp.OpenURL(u)
		}
	})

	fyneApp.Lifecycle().SetOnStarted(func() {
		// Check DB
		if len(getServers()) == 0 {
			showServerForm(nil)
		} else {
			// Auto-start default
			StartBridge(0)
		}
	})

	fyneApp.Run()
}

func createStatusIcon(c color.Color) fyne.Resource {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			dx := float64(x) - 32
			dy := float64(y) - 32
			if dx*dx+dy*dy < 28*28 {
				img.Set(x, y, c)
			}
		}
	}
	buf := new(bytes.Buffer)
	png.Encode(buf, img)
	name := "tray_disconnected.png"
	if c == (color.RGBA{0, 255, 0, 255}) {
		name = "tray_connected.png"
	}
	return fyne.NewStaticResource(name, buf.Bytes())
}

func setupTrayMenu() {
	refreshTrayMenu()
}

func refreshTrayMenu() {
	trayLock.Lock()
	defer trayLock.Unlock()

	if desk == nil {
		return
	}
	log.Println("Setting up Tray Menu...")

	refreshMenu := func() {
		servers := getServers()
		var items []*fyne.MenuItem

		// Add server items directly to the main menu
		for _, s := range servers {
			srv := s // capture
			label := srv.Name
			if label == "" {
				label = srv.URL
			}

			// Determina lo stato della connessione
			activeServerMu.Lock()
			isInternal := bridgeRunning && activeServer != nil && activeServer.ID == srv.ID
			activeServerMu.Unlock()

			backgroundProcsMu.Lock()
			proc := backgroundProcs[srv.ID]
			backgroundProcsMu.Unlock()
			isBackground := proc != nil

			item := fyne.NewMenuItem(label, nil)
			item.Checked = isInternal || isBackground

			subMenu := fyne.NewMenu("",
				fyne.NewMenuItem("Open Dashboard", func() {
					token := getConfig(fmt.Sprintf("token_%d", srv.ID))
					if isInternal && MagicLoginToken != "" {
						token = MagicLoginToken
					}
					target := srv.URL
					if !strings.Contains(target, "://") {
						target = "http://" + target
					}
					if token != "" {
						target = fmt.Sprintf("%s/api/autologin?token=%s", target, token)
					}
					u, err := url.Parse(target)
					if err == nil {
						fyneApp.OpenURL(u)
					}
				}),
				fyne.NewMenuItemSeparator(),
			)

			if isInternal {
				subMenu.Items = append(subMenu.Items, fyne.NewMenuItem("Disconnect", func() {
					StopBridge()
					setupTrayMenu()
				}))
			} else if isBackground {
				subMenu.Items = append(subMenu.Items, fyne.NewMenuItem("Disconnect", func() {
					backgroundProcsMu.Lock()
					if p := backgroundProcs[srv.ID]; p != nil {
						p.Kill()
						delete(backgroundProcs, srv.ID)
					}
					backgroundProcsMu.Unlock()
					setupTrayMenu()
				}))
			} else {
				subMenu.Items = append(subMenu.Items, fyne.NewMenuItem("Connect", func() {
					activeServerMu.Lock()
					internalBusy := bridgeRunning
					activeServerMu.Unlock()

					if !internalBusy {
						StartBridge(srv.ID)
					} else {
						launchBackgroundBridge(srv.ID, func() {
							setupTrayMenu()
						})
					}
					setupTrayMenu()
				}))
			}

			item.ChildMenu = subMenu
			items = append(items, item)
		}

		// Aggiungi separatore se ci sono server
		if len(servers) > 0 {
			items = append(items, fyne.NewMenuItemSeparator())
		}

		items = append(items, fyne.NewMenuItem("Configuration", func() {
			showConfigWindow()
		}))
		items = append(items, fyne.NewMenuItemSeparator())
		items = append(items, fyne.NewMenuItem("Quit", func() {
			StopBridge()
			fyneApp.Quit()
		}))

		trayMenu = fyne.NewMenu("ShellDeck", items...)
		desk.SetSystemTrayMenu(trayMenu)
	}

	refreshMenu()
}

func launchBackgroundBridge(serverID int, onExit func()) {
	exe, err := os.Executable()
	if err != nil {
		log.Println("Errore nel trovare l'eseguibile:", err)
		return
	}
	cmd := exec.Command(exe, "-connect", strconv.Itoa(serverID))
	if err := cmd.Start(); err != nil {
		log.Println("Errore avvio background bridge:", err)
		return
	}

	backgroundProcsMu.Lock()
	backgroundProcs[serverID] = cmd.Process
	backgroundProcsMu.Unlock()
	log.Printf("Background bridge started for server %d (PID: %d)", serverID, cmd.Process.Pid)

	go func() {
		cmd.Wait()
		backgroundProcsMu.Lock()
		delete(backgroundProcs, serverID)
		backgroundProcsMu.Unlock()
		log.Printf("Background bridge stopped for server %d", serverID)
		if onExit != nil {
			onExit()
		}
	}()
}

func showServerForm(existing *ServerConfig) {
	title := "Add New Server"
	if existing != nil {
		title = "Edit Server"
	}
	w := fyneApp.NewWindow(title)
	w.Resize(fyne.NewSize(400, 300))
	w.SetFixedSize(true)

	nameEntry := widget.NewEntry()
	nameEntry.SetPlaceHolder("My Server (Optional)")

	urlEntry := widget.NewEntry()
	urlEntry.SetPlaceHolder("localhost:9112")
	urlEntry.Text = "localhost:9112"

	userEntry := widget.NewEntry()
	userEntry.SetPlaceHolder("admin")
	userEntry.Text = "admin"

	passEntry := widget.NewPasswordEntry()
	passEntry.SetPlaceHolder("admin")
	passEntry.Text = "admin"

	if existing != nil {
		nameEntry.Text = existing.Name
		urlEntry.Text = existing.URL
		userEntry.Text = existing.Username
	}

	form := &widget.Form{
		Items: []*widget.FormItem{
			{Text: "Connection Name", Widget: nameEntry},
			{Text: "Server URL", Widget: urlEntry},
			{Text: "Username", Widget: userEntry},
			{Text: "Password", Widget: passEntry},
		},
		OnSubmit: func() {
			url := strings.TrimSpace(urlEntry.Text)
			if !strings.HasPrefix(url, "http") {
				url = "http://" + url
			}
			url = strings.TrimRight(url, "/")

			loginURL := fmt.Sprintf("%s/api/login", url)
			reqBody, _ := json.Marshal(map[string]string{
				"username":    userEntry.Text,
				"password":    passEntry.Text,
				"client_type": "bridge",
			})

			resp, err := http.Post(loginURL, "application/json", bytes.NewBuffer(reqBody))
			if err != nil {
				dialog.ShowError(err, w)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != 200 {
				var errResult map[string]interface{}
				json.NewDecoder(resp.Body).Decode(&errResult)
				msg := "Login failed"
				if m, ok := errResult["msg"].(string); ok {
					msg = m
				}
				dialog.ShowError(fmt.Errorf("%s (%d)", msg, resp.StatusCode), w)
				return
			}

			var result map[string]interface{}
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				dialog.ShowError(err, w)
				return
			}

			key, ok := result["encryption_key"].(string)
			if !ok || key == "" {
				dialog.ShowError(fmt.Errorf("Invalid key from server"), w)
				return
			}

			if existing == nil {
				// Add
				if err := addServer(nameEntry.Text, url, userEntry.Text, key); err != nil {
					dialog.ShowError(err, w)
					return
				}
				// If this is the first server, make it active automatically
				if len(getServers()) == 1 {
					servers := getServers()
					StartBridge(servers[0].ID) // addServer lo imposta già come default
				} else {
					showConfigWindow()
				}
			} else {
				// Edit
				if err := updateServer(existing.ID, nameEntry.Text, url, userEntry.Text, key); err != nil {
					dialog.ShowError(err, w)
					return
				}
				showConfigWindow()
			}

			w.Close()
			setupTrayMenu()
		},
	}

	var objects []fyne.CanvasObject
	if existing == nil && len(getServers()) == 0 {
		objects = append(objects, widget.NewLabelWithStyle("Welcome to ShellDeck Bridge", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}))
		objects = append(objects, widget.NewLabel("Please configure your first server connection."))

		note := widget.NewLabel("Please note: if this is your server's first run, default user and password is 'admin'.\nDefault port is 9112. Remember to change your password.\nIf you are connecting to an existing server, be sure the user exists on server.")
		note.Wrapping = fyne.TextWrapWord
		objects = append(objects, note)
	}
	objects = append(objects, form)
	w.SetContent(container.NewVBox(objects...))
	w.Show()
}

func showConfigWindow() {
	w := fyneApp.NewWindow("Bridge Configuration")
	w.Resize(fyne.NewSize(750, 500))

	servers := getServers()
	configList = widget.NewList(
		func() int { return len(servers) },
		func() fyne.CanvasObject {
			icon := widget.NewIcon(theme.ComputerIcon())

			nameLabel := widget.NewLabel("Server Name")
			nameLabel.TextStyle = fyne.TextStyle{Bold: true}

			urlLabel := widget.NewLabel("URL")
			urlLabel.TextStyle = fyne.TextStyle{Italic: true}

			infoBox := container.NewVBox(nameLabel, urlLabel)

			dashBtn := widget.NewButtonWithIcon("", theme.HomeIcon(), nil)
			dashBtn.Importance = widget.LowImportance

			connectBtn := widget.NewButton("OFF", nil)

			defBtn := widget.NewButtonWithIcon("", theme.ConfirmIcon(), nil)
			defBtn.Importance = widget.LowImportance

			editBtn := widget.NewButtonWithIcon("", theme.SettingsIcon(), nil)
			editBtn.Importance = widget.LowImportance

			delBtn := widget.NewButtonWithIcon("", theme.DeleteIcon(), nil)
			delBtn.Importance = widget.LowImportance

			return container.NewHBox(
				icon,
				infoBox,
				layout.NewSpacer(),
				dashBtn,
				connectBtn,
				defBtn,
				editBtn,
				delBtn,
			)
		},
		func(i int, o fyne.CanvasObject) {
			s := servers[i]
			box := o.(*fyne.Container)

			infoBox := box.Objects[1].(*fyne.Container)
			nameLabel := infoBox.Objects[0].(*widget.Label)
			urlLabel := infoBox.Objects[1].(*widget.Label)

			dashBtn := box.Objects[3].(*widget.Button)
			connectBtn := box.Objects[4].(*widget.Button)
			defBtn := box.Objects[5].(*widget.Button)
			editBtn := box.Objects[6].(*widget.Button)
			delBtn := box.Objects[7].(*widget.Button)

			name := s.Name
			if name == "" {
				name = s.URL
			}
			nameLabel.SetText(name)
			urlLabel.SetText(fmt.Sprintf("%s (%s)", s.URL, s.Username))

			dashBtn.OnTapped = func() {
				token := getConfig(fmt.Sprintf("token_%d", s.ID))
				target := s.URL
				if !strings.Contains(target, "://") {
					target = "http://" + target
				}
				if token != "" {
					target = fmt.Sprintf("%s/api/autologin?token=%s", target, token)
				}
				u, err := url.Parse(target)
				if err == nil {
					fyneApp.OpenURL(u)
				}
			}

			// Check Status
			activeServerMu.Lock()
			isInternal := bridgeRunning && activeServer != nil && activeServer.ID == s.ID
			activeServerMu.Unlock()

			backgroundProcsMu.Lock()
			proc := backgroundProcs[s.ID]
			backgroundProcsMu.Unlock()
			isBackground := proc != nil

			if isInternal || isBackground {
				connectBtn.SetText("ON")
				connectBtn.SetIcon(nil)
				connectBtn.Importance = widget.HighImportance
				connectBtn.OnTapped = func() {
					if isInternal {
						StopBridge()
					} else if isBackground {
						backgroundProcsMu.Lock()
						if p := backgroundProcs[s.ID]; p != nil {
							p.Kill()
							delete(backgroundProcs, s.ID)
						}
						backgroundProcsMu.Unlock()
					}
					setupTrayMenu()
					configList.Refresh()
				}
			} else {
				connectBtn.SetText("Connect")
				connectBtn.SetIcon(theme.MediaPlayIcon())
				connectBtn.Importance = widget.HighImportance
				connectBtn.OnTapped = func() {
					activeServerMu.Lock()
					internalBusy := bridgeRunning
					activeServerMu.Unlock()

					if !internalBusy {
						StartBridge(s.ID)
					} else {
						launchBackgroundBridge(s.ID, func() {
							setupTrayMenu()
							if configList != nil {
								configList.Refresh()
							}
						})
					}
					setupTrayMenu()
					configList.Refresh()
				}
			}

			if s.IsDefault {
				defBtn.Disable()
				defBtn.Importance = widget.HighImportance
			} else {
				defBtn.Enable()
				defBtn.Importance = widget.LowImportance
			}
			defBtn.OnTapped = func() {
				setDefaultServer(s.ID)
				servers = getServers()
				configList.Refresh()
				setupTrayMenu()
			}

			editBtn.OnTapped = func() {
				w.Close()
				showServerForm(&s)
			}

			delBtn.OnTapped = func() {
				dialog.ShowConfirm("Delete Server", "Are you sure?", func(b bool) {
					if b {
						deleteServer(s.ID)
						servers = getServers() // Refresh local list
						configList.Refresh()   // Trigger list refresh
						w.Close()              // Close and reopen to refresh fully (simpler)
						showConfigWindow()
						setupTrayMenu()
					}
				}, w)
			}
		},
	)

	addBtn := widget.NewButtonWithIcon("Add New Server", theme.ContentAddIcon(), func() {
		w.Close()
		showServerForm(nil)
	})

	w.SetContent(container.NewBorder(nil, addBtn, nil, nil, configList))
	w.Show()
}

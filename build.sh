#!/bin/bash

echo "🚀 Inizio compilazione Shelldeck..."

# 1. Creazione della struttura delle cartelle pulite
echo "📁 Preparazione delle directory..."
mkdir -p releases/linux/server releases/linux/bridge releases/linux/standalone
mkdir -p releases/windows/server releases/windows/bridge releases/windows/standalone
#mkdir -p releases/mac-intel/server releases/mac-intel/bridge releases/mac-intel/standalone

# ---------------------------------------------------------
echo "🐧 Compilazione per Linux..."
# Server
GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o releases/linux/server/shelldeck-server-linux ./cmd/server
chmod +x releases/linux/server/shelldeck-server-linux
# Bridge (Estraiamo solo il binario puro dall'archivio .tar.xz)
go run github.com/fyne-io/fyne-cross@latest linux -arch=amd64 -app-id=com.shelldeck.bridge -name=shelldeck-bridge ./cmd/bridge
tar -O -xf fyne-cross/dist/linux-amd64/shelldeck-bridge.tar.xz usr/local/bin/bridge > releases/linux/bridge/shelldeck-bridge-linux
chmod +x releases/linux/bridge/shelldeck-bridge-linux

# Standalone
go run github.com/fyne-io/fyne-cross@latest linux -arch=amd64 -app-id=com.shelldeck.standalone -name=shelldeck-standalone-linux ./cmd/standalone
tar -O -xf fyne-cross/dist/linux-amd64/shelldeck-standalone-linux.tar.xz usr/local/bin/standalone > releases/linux/standalone/shelldeck-standalone-linux
chmod +x releases/linux/standalone/shelldeck-standalone-linux
#proxy-bridge
go build -tags headless -o releases/linux/bridge/shelldeck-bridge-headless ./cmd/bridge
# ---------------------------------------------------------
echo "🪟 Compilazione per Windows..."
# Server
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o releases/windows/server/shelldeck-server-windows.exe ./cmd/server

# Bridge (Estraiamo il file .exe dallo .zip generato)
go run github.com/fyne-io/fyne-cross@latest windows -arch=amd64 -app-id=com.shelldeck.bridge -name=shelldeck-bridge ./cmd/bridge
unzip -p fyne-cross/dist/windows-amd64/shelldeck-bridge.zip shelldeck-bridge.exe > releases/windows/bridge/shelldeck-bridge-windows.exe

# Standalone (Estraiamo il file .exe dallo .zip generato)
go run github.com/fyne-io/fyne-cross@latest windows -arch=amd64 -app-id=com.shelldeck.standalone -name=shelldeck-standalone-windows ./cmd/standalone
unzip -p fyne-cross/dist/windows-amd64/shelldeck-standalone-windows.zip shelldeck-standalone-windows.exe > releases/windows/standalone/shelldeck-standalone-windows.exe
# ---------------------------------------------------------
#echo "🍎 Compilazione per Mac (Intel)..."
# Server
#GOOS=darwin GOARCH=amd64 go build -ldflags="-s -w" -o releases/mac-intel/server/shelldeck-server-mac-intel ./cmd/server

# Bridge (Per Mac, l'app è in realtà una cartella .app)
#go run github.com/fyne-io/fyne-cross@latest darwin -arch=amd64 -app-id=com.shelldeck.bridge -name=shelldeck-bridge-mac-intel ./cmd/bridge
 #rm -rf releases/mac-intel/bridge/shelldeck-bridge-mac-intel.app # Pulisce prima di sovrascrivere
#mv fyne-cross/dist/darwin-amd64/shelldeck-bridge-mac-intel.app releases/mac-intel/bridge/

# Standalone
#go run github.com/fyne-io/fyne-cross@latest darwin -arch=amd64 -app-id=com.shelldeck.standalone -name=shelldeck-standalone-mac-intel ./cmd/standalone
#rm -rf releases/mac-intel/standalone/shelldeck-standalone-mac-intel.app
#mv fyne-cross/dist/darwin-amd64/shelldeck-standalone-mac-intel.app releases/mac-intel/standalone/

echo "✅ Compilazione completata! Trovi tutti gli eseguibili puliti nella cartella 'releases/'."
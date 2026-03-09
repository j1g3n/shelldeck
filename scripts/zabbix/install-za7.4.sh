#!/bin/bash

# 1. Controllo permessi di root
if [ "$EUID" -ne 0 ]; then
  echo "❌ Errore: Per favore, esegui questo script come root (usa sudo)."
  exit 1
fi

# 2. Rilevamento automatico e dinamico del sistema operativo
. /etc/os-release
OS=$ID
VERSION=$VERSION_ID

# Verifica che sia effettivamente Debian o Ubuntu
if [ "$OS" != "ubuntu" ] && [ "$OS" != "debian" ]; then
    echo "❌ Errore: Questo script è progettato solo per distribuzioni basate su Debian o Ubuntu. Rilevato: $OS"
    exit 1
fi

echo "🔍 Sistema operativo rilevato: $NAME $VERSION"

# 3. Generazione dinamica dell'URL del pacchetto per Zabbix 7.4
REPO_DEB="zabbix-release_latest_7.4+${OS}${VERSION}_all.deb"
REPO_URL="https://repo.zabbix.com/zabbix/7.4/release/${OS}/pool/main/z/zabbix-release/${REPO_DEB}"

# 4. Richiesta dati all'utente
echo "----------------------------------------"
read -p "Inserisci l'IP del Server Zabbix: " ZIP
read -p "Inserisci l'Hostname dell'Agent: " ZHOST
echo "----------------------------------------"

# 5. Download e Installazione
echo "📥 Scaricamento del pacchetto repository per $OS $VERSION (Zabbix 7.4)..."
wget -qO $REPO_DEB $REPO_URL

# Controllo se il download è andato a buon fine
if [ ! -s $REPO_DEB ]; then
    echo "❌ Errore: Impossibile scaricare il pacchetto."
    echo "🔗 URL tentato: $REPO_URL"
    echo "💡 Verifica che Zabbix 7.4 supporti $OS $VERSION e che l'URL sia corretto."
    rm -f $REPO_DEB
    exit 1
fi

dpkg -i $REPO_DEB

echo "⚙️ Aggiornamento repository e installazione di zabbix-agent2..."
apt-get update -qq
apt-get install -y zabbix-agent2

# 6. Configurazione automatica
echo "📝 Iniezione dei dati nel file /etc/zabbix/zabbix_agent2.conf..."
sed -i "s/^Server=127.0.0.1/Server=$ZIP/" /etc/zabbix/zabbix_agent2.conf
sed -i "s/^ServerActive=127.0.0.1/ServerActive=$ZIP/" /etc/zabbix/zabbix_agent2.conf
sed -i "s/^Hostname=Zabbix server/Hostname=$ZHOST/" /etc/zabbix/zabbix_agent2.conf

# 7. Avvio e pulizia
echo "🚀 Abilitazione e riavvio del servizio zabbix-agent2..."
systemctl enable zabbix-agent2
systemctl restart zabbix-agent2

rm -f $REPO_DEB

echo "🚀INSTALLED"
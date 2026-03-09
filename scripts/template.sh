#!/bin/bash

# Verifica che lo script sia eseguito con i permessi di root
if [ "$EUID" -ne 0 ]; then
  echo "Errore: Esegui questo script come root o usando sudo."
  exit 1
fi

# Variabili predefinite
OLD_HOSTNAME=$(hostname)
OLD_IP="10.10.2.100" # IP fisso del template

echo "=========================================="
echo "    Configurazione Clone Proxmox VM       "
echo "=========================================="

# 1. Richiesta Dati in Input (solo i nuovi)
read -p "Inserisci il NUOVO Hostname: " NEW_HOSTNAME
read -p "Inserisci il NUOVO IP (es. 10.10.2.105): " NEW_IP

echo "------------------------------------------"

# 2. Modifica Hostname e file hosts
echo "[1/3] Modifica dell'hostname da $OLD_HOSTNAME a $NEW_HOSTNAME..."
hostnamectl set-hostname "$NEW_HOSTNAME"
sed -i "s/$OLD_HOSTNAME/$NEW_HOSTNAME/g" /etc/hosts

# 3. Modifica Netplan e applicazione
echo "[2/3] Aggiornamento della configurazione Netplan..."
NETPLAN_FILE=$(ls /etc/netplan/*.yaml 2>/dev/null | head -n 1)

if [ -n "$NETPLAN_FILE" ]; then
    # Crea un backup
    cp "$NETPLAN_FILE" "${NETPLAN_FILE}.bak"
    
    # Sostituisce il vecchio IP 10.10.2.100 con quello nuovo
    sed -i "s/$OLD_IP/$NEW_IP/g" "$NETPLAN_FILE"
    
    echo "      Applicazione delle nuove regole di rete con 'netplan apply'..."
    netplan apply
else
    echo "      [ERRORE] Nessun file di configurazione trovato in /etc/netplan/"
fi

# 4. Modifica Zabbix Agent 2
echo "[3/3] Aggiornamento configurazione Zabbix Agent 2..."
ZABBIX_CONF="/etc/zabbix/zabbix_agent2.conf"

if [ -f "$ZABBIX_CONF" ]; then
    # Crea un backup
    cp "$ZABBIX_CONF" "${ZABBIX_CONF}.bak"
    
    # Modifica il parametro Hostname
    sed -i "s/^Hostname=.*/Hostname=$NEW_HOSTNAME/" "$ZABBIX_CONF"
    
    echo "      Riavvio del servizio zabbix-agent2..."
    systemctl restart zabbix-agent2
else
    echo "      [AVVISO] File $ZABBIX_CONF non trovato. Verifica che Zabbix Agent 2 sia installato."
fi

echo "=========================================="
echo "Configurazione completata con successo!"
echo "Nuovo Hostname: $(hostname)"
echo "Nuovo IP configurato in Netplan: $NEW_IP"
echo "=========================================="
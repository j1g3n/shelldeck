#!/bin/bash

# Controllo privilegi root
if [[ $EUID -ne 0 ]]; then
   echo "Questo script deve essere eseguito come root (sudo -s)"
   exit 1
fi

echo "--- Configurazione Iniziale Zabbix Proxy ---"
read -p "Inserisci IP del Server Zabbix centrale: " ZBX_SERVER_IP
read -p "Inserisci Hostname di questo Proxy: " ZBX_PROXY_HOSTNAME
read -s -p "Imposta Password ROOT per MariaDB: " MYSQL_ROOT_PASS
echo ""
read -s -p "Imposta Password UTENTE ZABBIX per DB: " ZBX_DB_PASS
echo ""

# 1. Verifica e Installazione MariaDB
if ! command -v mariadb &> /dev/null; then
    echo "MariaDB non trovato. Installazione in corso..."
    apt update
    apt install -y mariadb-server
    systemctl enable --now mariadb

    # Secure configuration automatizzata
    mysql -e "ALTER USER 'root'@'localhost' IDENTIFIED BY '$MYSQL_ROOT_PASS';"
    mysql -u root -p"$MYSQL_ROOT_PASS" -e "DELETE FROM mysql.user WHERE User='';"
    mysql -u root -p"$MYSQL_ROOT_PASS" -e "DROP DATABASE IF EXISTS test;"
    mysql -u root -p"$MYSQL_ROOT_PASS" -e "DELETE FROM mysql.db WHERE Db='test' OR Db='test\\_%';"
    mysql -u root -p"$MYSQL_ROOT_PASS" -e "FLUSH PRIVILEGES;"
    echo "MariaDB installato e configurato."
else
    echo "MariaDB è già installato."
fi

# 2. Installazione Repository Zabbix 7.4
echo "Installazione repository Zabbix 7.4..."
wget https://repo.zabbix.com/zabbix/7.4/release/ubuntu/pool/main/z/zabbix-release/zabbix-release_latest_7.4+ubuntu24.04_all.deb
dpkg -i zabbix-release_latest_7.4+ubuntu24.04_all.deb
apt update

# 3. Installazione Zabbix Proxy e Agent 2
echo "Installazione componenti Zabbix..."
apt install -y zabbix-proxy-mysql zabbix-sql-scripts zabbix-agent2

# 4. Creazione Database
echo "Configurazione Database per il Proxy..."
mysql -u root -p"$MYSQL_ROOT_PASS" <<EOF
create database zabbix_proxy character set utf8mb4 collate utf8mb4_bin;
create user 'zabbix'@'localhost' identified by '$ZBX_DB_PASS';
grant all privileges on zabbix_proxy.* to 'zabbix'@'localhost';
set global log_bin_trust_function_creators = 1;
EOF

# Import dello schema
echo "Importazione schema database (richiederà la password dell'utente zabbix appena creata)..."
zcat /usr/share/zabbix/sql-scripts/mysql/proxy.sql.gz | mysql --default-character-set=utf8mb4 -uzabbix -p"$ZBX_DB_PASS" zabbix_proxy

# Disabilita log_bin_trust_function_creators
mysql -u root -p"$MYSQL_ROOT_PASS" -e "set global log_bin_trust_function_creators = 0;"

# 5. Configurazione Zabbix Proxy
echo "Configurazione zabbix_proxy.conf..."
sed -i "s/^ProxyMode=.*/ProxyMode=0/" /etc/zabbix/zabbix_proxy.conf # 0 = Active, 1 = Passive
sed -i "s/^Server=.*/Server=$ZBX_SERVER_IP/" /etc/zabbix/zabbix_proxy.conf
sed -i "s/^Hostname=.*/Hostname=$ZBX_PROXY_HOSTNAME/" /etc/zabbix/zabbix_proxy.conf
sed -i "s/^DBName=.*/DBName=zabbix_proxy/" /etc/zabbix/zabbix_proxy.conf
sed -i "s/^DBUser=.*/DBUser=zabbix/" /etc/zabbix/zabbix_proxy.conf
sed -i "/^# DBPassword=/c\DBPassword=$ZBX_DB_PASS" /etc/zabbix/zabbix_proxy.conf

# 6. Configurazione Zabbix Agent 2
echo "Configurazione zabbix_agent2.conf..."
# L'agent punta al proxy locale (127.0.0.1) o al server centrale? 
# Solitamente l'agent sul proxy comunica con il proxy stesso.
sed -i "s/^Server=.*/Server=127.0.0.1, $ZBX_SERVER_IP/" /etc/zabbix/zabbix_agent2.conf
sed -i "s/^ServerActive=.*/ServerActive=127.0.0.1/" /etc/zabbix/zabbix_agent2.conf
sed -i "s/^Hostname=.*/Hostname=$ZBX_PROXY_HOSTNAME/" /etc/zabbix/zabbix_agent2.conf

# 7. Avvio Servizi
echo "Riavvio e abilitazione servizi..."
systemctl restart zabbix-proxy zabbix-agent2
systemctl enable zabbix-proxy zabbix-agent2

echo "------------------------------------------------------"
echo "Installazione Completata!"
echo "Proxy Hostname: $ZBX_PROXY_HOSTNAME"
echo "Zabbix Server: $ZBX_SERVER_IP"
echo "Ricorda di aggiungere il Proxy nell'interfaccia web di Zabbix"
echo "utilizzando lo stesso Hostname configurato qui."
echo "------------------------------------------------------"
# shelldeck To-Do List

## 🖥️ System

### 💾 Disk Management
- [X] **Frontend**: Riscrivere completamente l'interfaccia grafica per la gestione dei dischi.
- [X] **Backend**: Implementare le funzioni necessarie per supportare la nuova interfaccia (es. dettagli partizioni, mount points, usage).
- [ ] **UI**: Migliorare la visualizzazione delle percentuali di utilizzo.

### 🌐 Network
- [ ] **Configurazione**: Aggiungere supporto per file di configurazione alternativi a Netplan (es. `nmcli`, `ifcfg` o NetworkManager keyfiles) per il supporto completo a Fedora/RedHat.

### 📦 Package Manager
- [ ] **Clean**: Fixare e verificare il funzionamento del comando `clean` su sistemi RedHat/Fedora (DNF).

### 🛠️ Admin Tools
- [X] **Hostname**: Fixare la logica di gestione e cambio dell'hostname.
- [X] **Hosts File**: Fixare la lettura e scrittura del file `/etc/hosts`.

### ⚙️ Services (System)
- [X] **Crontab Root**: Fixare la visualizzazione e modifica del crontab per l'utente `root`.

### 📊 Task Manager
- [ ] **Ricerca**: Migliorare la ricerca per includere anche il comando completo, non solo nome processo e PID.

---

## 🔌 Services Module

### 🪶 Web Servers (Apache/Nginx)
- [X] **Moduli**: Fixare il recupero e la visualizzazione della lista dei moduli.
- [X] **Stato**: Gestire correttamente l'interfaccia quando Apache2 non è installato sul server target.
- [X] **Logs**: Integrare una pagina dedicata e potenziata per l'analisi dei log di accesso ed errore.
- [ ] **Config**: Sistemare il salvataggio delle nuove configurazioni per Apache2 e Nginx.

### 🔐 SSH
- [ ] **Gestione Key**: Integrare un pannello per la gestione delle chiavi SSH.
- [ ] **Visualizzazione**: Mostrare le chiavi attualmente presenti in `authorized_keys`.
- [ ] **Aggiunta**: Implementare la funzionalità per aggiungere nuove chiavi pubbliche.
#!/bin/bash

# ==========================================
# Script per esportare e splittare il codice su macOS/Linux
# ==========================================

OUTPUT_DIR="jconman_export"
MAX_BYTES=100000 # Circa 100KB

# Colori per il terminale
CYAN='\033[0;36m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
WHITE='\033[0;37m'
NC='\033[0m' # Nessun colore

# Crea o ripulisce la cartella di output
if [ -d "$OUTPUT_DIR" ]; then
    rm -rf "$OUTPUT_DIR"
fi
mkdir -p "$OUTPUT_DIR"

echo -e "${CYAN}Raccolta dei file in corso...${NC}"

# 1. Raccogli i file (escludendo le cartelle e filtrando le estensioni)
# Usiamo un array e read con -d $'\0' per gestire in sicurezza eventuali file con spazi nel nome
FILES=()
while IFS= read -r -d $'\0'; do
    FILES+=("$REPLY")
done < <(find . -type d \( -name ".git" -o -name "node_modules" -o -name "vendor" -o -name "tmp" \) -prune \
    -o -type f \( -name "*.go" -o -name "*.html" -o -name "*.js" -o -name "*.css" -o -name "*.json" \) -print0)

# Variabili per gestire la divisione
PART_NUM=1
CURRENT_BYTES=0

get_current_file_path() {
    echo "$OUTPUT_DIR/jconman_part_${PART_NUM}.txt"
}

CURRENT_FILE=$(get_current_file_path)

# 2. Crea l'indice della struttura del progetto
{
    echo "=== STRUTTURA DEL PROGETTO ==="
    for file in "${FILES[@]}"; do
        # Rimuove il fastidioso "./" all'inizio del percorso per renderlo più pulito
        echo "$file" | sed 's|^./||'
    done
    echo ""
    echo ""
} > "$CURRENT_FILE"

# Calcola il peso iniziale dell'indice in byte
CURRENT_BYTES=$(wc -c < "$CURRENT_FILE" | tr -d ' ')

# 3. Scrivi il contenuto gestendo lo split
for file in "${FILES[@]}"; do
    clean_name=$(echo "$file" | sed 's|^./||')
    echo -e "${GREEN}Elaboro: $clean_name${NC}"
    
    # Crea un file temporaneo per calcolare con esattezza la dimensione del blocco in byte
    TEMP_BLOCK=$(mktemp)
    
    echo "================================================================" > "$TEMP_BLOCK"
    echo "FILE: $clean_name" >> "$TEMP_BLOCK"
    echo "================================================================" >> "$TEMP_BLOCK"
    cat "$file" >> "$TEMP_BLOCK"
    echo "" >> "$TEMP_BLOCK"
    echo "" >> "$TEMP_BLOCK"
    
    BLOCK_BYTES=$(wc -c < "$TEMP_BLOCK" | tr -d ' ')

    # Se questo blocco supera il limite e il file corrente ha già dei dati, passa al prossimo
    if [ $((CURRENT_BYTES + BLOCK_BYTES)) -gt $MAX_BYTES ] && [ $CURRENT_BYTES -gt 0 ]; then
        PART_NUM=$((PART_NUM + 1))
        CURRENT_FILE=$(get_current_file_path)
        CURRENT_BYTES=0
    fi
    
    # Appendi il blocco al file corrente e aggiorna il contatore
    cat "$TEMP_BLOCK" >> "$CURRENT_FILE"
    CURRENT_BYTES=$((CURRENT_BYTES + BLOCK_BYTES))
    
    # Pulisci il file temporaneo
    rm -f "$TEMP_BLOCK"
done

echo -e "\n${YELLOW}Finito! I file sono stati generati nella cartella '$OUTPUT_DIR'.${NC}"
echo -e "${WHITE}Sono stati creati $PART_NUM file pronti per l'IA.${NC}"r
"""
export_code.py — Esporta il codebase di Shelldeck in un .txt analizzabile.

USO:
    python export_code.py --repo /path/to/shelldeck --out dataset/shelldeck_code.txt

Il file prodotto contiene:
  - Tutto il codice Go, HTML, CSS, JS, YAML, Markdown
  - Commenti e docstring preservati
  - Nomi di funzioni, variabili, tipi estratti separatamente
  - Struttura del repo (albero file)
  - Statistiche base del codebase

Il testo prodotto è compatibile con calculate13.py — basta metterlo
nella cartella dataset e girare l'analisi normalmente.
"""

import os
import sys
import argparse
import re
from pathlib import Path
from collections import Counter, defaultdict

# ─────────────────────────────────────────────
# CONFIGURAZIONE
# ─────────────────────────────────────────────

# Estensioni da includere
INCLUDE_EXT = {
    '.go', '.html', '.htm', '.css', '.js', '.ts',
    '.yaml', '.yml', '.json', '.toml', '.md', '.txt',
    '.sh', '.dockerfile', '.mod', '.sum',
}

# Directory da escludere sempre
EXCLUDE_DIRS = {
    '.git', '.github', 'node_modules', 'vendor', '__pycache__',
    '.idea', '.vscode', 'dist', 'build', 'bin', 'tmp', '.cache',
}

# File da escludere (troppo grandi o non informativi)
EXCLUDE_FILES = {
    'go.sum', 'package-lock.json', 'yarn.lock',
}

# Dimensione massima per file singolo (byte) — evita file binari mascherati
MAX_FILE_SIZE = 500_000

# ─────────────────────────────────────────────
# PARSER STRUTTURALE GO
# ─────────────────────────────────────────────

def extract_go_structure(content):
    """
    Estrae struttura semantica dal codice Go:
    - Nomi funzioni e metodi
    - Nomi tipi/struct/interface
    - Nomi package e import
    - Commenti (preservati)
    - Costanti e variabili globali
    """
    structure = []

    # Package
    pkg = re.findall(r'^package\s+(\w+)', content, re.MULTILINE)
    if pkg:
        structure.append(f"[PACKAGE] {' '.join(set(pkg))}")

    # Import
    imports = re.findall(r'"([^"]+)"', content)
    if imports:
        # Prendi solo i nomi finali dei path
        imp_names = [i.split('/')[-1] for i in imports if '/' in i or '.' not in i]
        if imp_names:
            structure.append(f"[IMPORTS] {' '.join(imp_names[:20])}")

    # Funzioni e metodi
    funcs = re.findall(
        r'^func\s+(?:\([^)]+\)\s+)?(\w+)\s*\(', content, re.MULTILINE
    )
    if funcs:
        structure.append(f"[FUNCTIONS] {' '.join(funcs)}")

    # Struct e interface
    types = re.findall(
        r'^type\s+(\w+)\s+(?:struct|interface)', content, re.MULTILINE
    )
    if types:
        structure.append(f"[TYPES] {' '.join(types)}")

    # Costanti
    consts = re.findall(r'^\s+(\w+)\s*=', content, re.MULTILINE)
    consts = [c for c in consts if c[0].isupper()][:20]
    if consts:
        structure.append(f"[CONSTANTS] {' '.join(consts)}")

    # Commenti (informativi per l'analisi semantica)
    comments = re.findall(r'//\s*(.+)', content)
    comments = [c.strip() for c in comments if len(c.strip()) > 10][:30]
    if comments:
        structure.append(f"[COMMENTS] {' . '.join(comments)}")

    return '\n'.join(structure)


def extract_html_structure(content):
    """Estrae testo leggibile da HTML rimuovendo tag."""
    # Rimuovi script e style embedded
    content = re.sub(r'<script[^>]*>.*?</script>', ' ', content, flags=re.DOTALL)
    content = re.sub(r'<style[^>]*>.*?</style>', ' ', content, flags=re.DOTALL)
    # Estrai attributi significativi
    ids = re.findall(r'\bid=["\']([^"\']+)["\']', content)
    classes = re.findall(r'\bclass=["\']([^"\']+)["\']', content)
    # Rimuovi tag HTML
    text = re.sub(r'<[^>]+>', ' ', content)
    text = re.sub(r'\s+', ' ', text).strip()
    extras = []
    if ids:     extras.append(f"[IDS] {' '.join(ids[:30])}")
    if classes: extras.append(f"[CLASSES] {' '.join(list(set(' '.join(classes).split()))[:30])}")
    return text + '\n' + '\n'.join(extras) if text else '\n'.join(extras)


def extract_css_structure(content):
    """Estrae selettori e proprietà significative da CSS."""
    selectors = re.findall(r'([.#]?[\w-]+(?:\s*[>+~]\s*[\w-]+)*)\s*\{', content)
    props = re.findall(r'([\w-]+)\s*:', content)
    prop_freq = Counter(props).most_common(20)
    result = []
    if selectors:
        result.append(f"[SELECTORS] {' '.join(selectors[:30])}")
    if prop_freq:
        result.append(f"[PROPERTIES] {' '.join(p[0] for p in prop_freq)}")
    # Commenti CSS
    comments = re.findall(r'/\*\s*(.+?)\s*\*/', content, re.DOTALL)
    comments = [c.replace('\n', ' ').strip() for c in comments if len(c.strip()) > 5][:10]
    if comments:
        result.append(f"[COMMENTS] {' . '.join(comments)}")
    return '\n'.join(result)


def process_file(filepath):
    """
    Legge un file e produce testo analizzabile.
    Ritorna (testo, metadati_dict).
    """
    ext = filepath.suffix.lower()
    try:
        content = filepath.read_text(encoding='utf-8', errors='ignore')
    except Exception as e:
        return None, {'error': str(e)}

    if not content.strip():
        return None, {'empty': True}

    lines = content.count('\n')
    size = len(content)

    if ext == '.go':
        processed = extract_go_structure(content)
        # Aggiungi anche il testo raw (commenti + nomi sono già estratti)
        # ma includi il contenuto completo per l'analisi frattale
        full_text = processed + '\n\n' + content
    elif ext in ('.html', '.htm'):
        full_text = extract_html_structure(content)
    elif ext == '.css':
        full_text = extract_css_structure(content)
    elif ext in ('.md', '.txt'):
        full_text = content  # testo diretto, ottimo per analisi
    elif ext in ('.yaml', '.yml', '.toml'):
        # Estrai chiavi e valori stringa
        keys = re.findall(r'^([\w-]+)\s*:', content, re.MULTILINE)
        vals = re.findall(r':\s*"?([^"\n{}\[\]]+)"?\s*$', content, re.MULTILINE)
        full_text = f"[KEYS] {' '.join(keys)}\n[VALUES] {' '.join(vals[:30])}\n{content}"
    else:
        full_text = content

    meta = {
        'lines': lines,
        'size_bytes': size,
        'ext': ext,
    }
    return full_text, meta


# ─────────────────────────────────────────────
# WALKER DEL REPO
# ─────────────────────────────────────────────

def walk_repo(repo_path):
    """
    Percorre il repo e ritorna lista di (Path, tipo).
    Ordina: Go prima (più informativo), poi HTML/CSS, poi altri.
    """
    repo = Path(repo_path)
    files = []

    for filepath in repo.rglob('*'):
        if not filepath.is_file():
            continue
        # Salta directory escluse
        parts = set(filepath.parts)
        if parts & EXCLUDE_DIRS:
            continue
        if filepath.name in EXCLUDE_FILES:
            continue
        if filepath.suffix.lower() not in INCLUDE_EXT:
            continue
        if filepath.stat().st_size > MAX_FILE_SIZE:
            print(f"  ⚠️  Skipped (too large): {filepath.name}")
            continue
        files.append(filepath)

    # Ordina per estensione poi nome
    priority = {'.go': 0, '.md': 1, '.html': 2, '.htm': 2,
                '.css': 3, '.js': 4, '.yaml': 5, '.yml': 5}
    files.sort(key=lambda f: (priority.get(f.suffix.lower(), 9), f.name))
    return files


# ─────────────────────────────────────────────
# MAIN EXPORT
# ─────────────────────────────────────────────

def export_repo(repo_path, output_path):
    repo = Path(repo_path)
    if not repo.exists():
        print(f"❌ Repo non trovato: {repo_path}")
        sys.exit(1)

    print(f"🔍 Analisi repo: {repo.resolve()}")
    files = walk_repo(repo_path)
    print(f"📁 {len(files)} file trovati")

    # Statistiche per tipo
    by_ext = defaultdict(list)
    for f in files:
        by_ext[f.suffix.lower()].append(f)

    for ext, flist in sorted(by_ext.items()):
        print(f"   {ext:8s}: {len(flist)} file")

    # Costruisci albero del repo
    tree_lines = []
    for f in files:
        rel = f.relative_to(repo)
        tree_lines.append(str(rel))

    # Elabora ogni file
    all_text_parts = []
    total_lines = 0
    total_size = 0
    file_metas = []

    # Header
    all_text_parts.append(f"=== SHELLDECK CODEBASE EXPORT ===")
    all_text_parts.append(f"Repo: {repo.resolve()}")
    all_text_parts.append(f"Files: {len(files)}")
    all_text_parts.append("")
    all_text_parts.append("=== STRUTTURA REPO ===")
    all_text_parts.extend(tree_lines)
    all_text_parts.append("")

    print("\n📝 Elaborazione file...")
    for filepath in files:
        rel = filepath.relative_to(repo)
        text, meta = process_file(filepath)

        if text is None:
            continue

        total_lines += meta.get('lines', 0)
        total_size  += meta.get('size_bytes', 0)
        file_metas.append({'path': str(rel), **meta})

        # Separatore leggibile
        all_text_parts.append(f"\n{'='*60}")
        all_text_parts.append(f"FILE: {rel}")
        all_text_parts.append(f"EXT: {meta.get('ext','?')} | LINES: {meta.get('lines',0)}")
        all_text_parts.append('='*60)
        all_text_parts.append(text)
        all_text_parts.append("")

        print(f"   ✓ {rel} ({meta.get('lines',0)} righe)")

    # Footer statistiche
    all_text_parts.append("\n=== STATISTICHE CODEBASE ===")
    all_text_parts.append(f"File totali processati: {len(file_metas)}")
    all_text_parts.append(f"Righe totali: {total_lines:,}")
    all_text_parts.append(f"Dimensione totale: {total_size/1024:.1f} KB")
    all_text_parts.append("")

    # Analisi nomi (proxy per ricchezza concettuale del design)
    all_content = '\n'.join(all_text_parts)

    # Estrai tutti i nomi Go per analisi semantica
    go_functions = re.findall(r'\[FUNCTIONS\] (.+)', all_content)
    go_types     = re.findall(r'\[TYPES\] (.+)', all_content)
    all_funcs = ' '.join(go_functions).split()
    all_types = ' '.join(go_types).split()

    all_text_parts.append("=== INVENTARIO SEMANTICO ===")
    all_text_parts.append(f"Funzioni Go uniche: {len(set(all_funcs))}")
    all_text_parts.append(f"Tipi/Struct Go unici: {len(set(all_types))}")

    if all_funcs:
        all_text_parts.append(f"\nFunzioni: {' '.join(sorted(set(all_funcs)))}")
    if all_types:
        all_text_parts.append(f"\nTipi: {' '.join(sorted(set(all_types)))}")

    # Vocabulary del codice (per analisi lessicale)
    all_text_parts.append("\n=== VOCABULARY ARCHITETTURALE ===")
    # Estrai identifier camelCase e snake_case
    identifiers = re.findall(r'\b([a-zA-Z][a-zA-Z0-9]{2,})\b', all_content)
    id_freq = Counter(identifiers)
    # Rimuovi keyword comuni
    go_keywords = {'func','type','struct','interface','package','import',
                   'return','if','else','for','range','var','const','map',
                   'chan','go','defer','select','case','switch','default',
                   'break','continue','nil','true','false','error','string',
                   'int','bool','byte','rune','float','make','new','append',
                   'len','cap','close','delete','copy','panic','recover',
                   'the','and','for','with','this','that','from','have',
                   'FILE','EXT','LINES','KEYS','VALUES','IMPORTS','PACKAGE',
                   'FUNCTIONS','TYPES','CONSTANTS','COMMENTS','SELECTORS',
                   'PROPERTIES','STATISTICS','IDS','CLASSES'}
    clean_ids = {k: v for k, v in id_freq.items()
                 if k.lower() not in go_keywords and len(k) > 2}
    top_ids = sorted(clean_ids.items(), key=lambda x: x[1], reverse=True)[:100]
    all_text_parts.append("Top 100 identifier per frequenza:")
    all_text_parts.append(' '.join(f"{k}({v})" for k, v in top_ids))

    # Scrivi output
    output = Path(output_path)
    output.parent.mkdir(parents=True, exist_ok=True)
    final_text = '\n'.join(all_text_parts)
    output.write_text(final_text, encoding='utf-8')

    word_count = len(re.findall(r'\b\w+\b', final_text))
    print(f"\n✅ Export completato!")
    print(f"   Output: {output.resolve()}")
    print(f"   Dimensione: {len(final_text)/1024:.1f} KB")
    print(f"   Parole totali: {word_count:,}")
    print(f"\n➡️  Copia {output.name} nella cartella dataset/ e lancia calculate13.py")
    print(f"   Il file verrà analizzato insieme alle conversazioni.")


# ─────────────────────────────────────────────
# ENTRY POINT
# ─────────────────────────────────────────────

if __name__ == '__main__':
    parser = argparse.ArgumentParser(
        description='Esporta codebase Shelldeck in formato analizzabile'
    )
    parser.add_argument(
        '--repo',
        default='.',
        help='Path al repo Shelldeck (default: directory corrente)'
    )
    parser.add_argument(
        '--out',
        default='dataset/shelldeck_code.txt',
        help='Path output (default: dataset/shelldeck_code.txt)'
    )
    args = parser.parse_args()
    export_repo(args.repo, args.out)

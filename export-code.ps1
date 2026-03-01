# ==========================================
# Script per esportare e splittare il codice
# ==========================================

# Cartella di output e dimensione massima per file (in caratteri, ~100KB)
$OutputDir = "jconman_export"
$MaxChars = 100000 

# Estensioni e cartelle da ignorare
$AllowedExtensions = @(".go", ".html", ".js", ".css", ".json")
$ExcludedDirs = @(".git", "node_modules", "vendor", "tmp")

# Crea o ripulisce la cartella di output
if (Test-Path $OutputDir) {
    Remove-Item -Path $OutputDir -Recurse -Force
}
New-Item -ItemType Directory -Path $OutputDir | Out-Null

Write-Host "Raccolta dei file in corso..." -ForegroundColor Cyan

# Ottieni tutti i file
$AllFiles = Get-ChildItem -Path . -Recurse -File | Where-Object {
    $ext = $_.Extension.ToLower()
    $AllowedExtensions -contains $ext
}

# Filtra le cartelle da escludere
$ValidFiles = @()
foreach ($file in $AllFiles) {
    $skip = $false
    foreach ($dir in $ExcludedDirs) {
        if ($file.FullName -match "\\$dir\\") {
            $skip = $true
            break
        }
    }
    if (-not $skip) {
        $ValidFiles += $file
    }
}

# Variabili per gestire la divisione in file
$PartNum = 1
$CurrentChars = 0

function Get-CurrentFilePath {
    return "$OutputDir\jconman_part_$PartNum.txt"
}

# 1. Crea l'indice della struttura
$treeStr = "=== STRUTTURA DEL PROGETTO ===`r`n"
foreach ($file in $ValidFiles) {
    $relativePath = $file.FullName.Replace($PWD.Path, ".")
    $treeStr += "$relativePath`r`n"
}
$treeStr += "`r`n`r`n"

Add-Content -Path (Get-CurrentFilePath) -Value $treeStr -Encoding UTF8 -NoNewline
$CurrentChars += $treeStr.Length

# 2. Scrivi il contenuto gestendo lo split
foreach ($file in $ValidFiles) {
    $relativePath = $file.FullName.Replace($PWD.Path, ".")
    Write-Host "Elaboro: $relativePath" -ForegroundColor Green
    
    $header = "================================================================`r`n"
    $header += "FILE: $relativePath`r`n"
    $header += "================================================================`r`n"
    
    $content = Get-Content -Path $file.FullName -Raw -Encoding UTF8
    if ($null -eq $content) { $content = "" } # Gestione file vuoti
    
    $block = $header + $content + "`r`n`r`n"
    $blockLength = $block.Length

    # Se questo blocco supera il limite e il file corrente ha già dei dati, passa al prossimo
    if (($CurrentChars + $blockLength) -gt $MaxChars -and $CurrentChars -gt 0) {
        $PartNum++
        $CurrentChars = 0
    }
    
    Add-Content -Path (Get-CurrentFilePath) -Value $block -Encoding UTF8 -NoNewline
    $CurrentChars += $blockLength
}

Write-Host "`r`nFinito! I file sono stati generati nella cartella '$OutputDir'." -ForegroundColor Yellow
Write-Host "Sono stati creati $PartNum file pronti per l'IA." -ForegroundColor White
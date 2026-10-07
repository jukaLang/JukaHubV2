param(
    [string]$Src = "..\jukaconfig.json",
    # Named jukaconfig.device.json on purpose: player/.gitignore excludes
    # jukaconfig.json (it may hold user secrets), and the generated device
    # config has to be committed for the CI build to pick it up.
    [string]$Dst = ".\jukaconfig.device.json"
)

$ErrorActionPreference = "Stop"
$cfg = Get-Content $Src -Raw | ConvertFrom-Json

# Scenes that stay on the handheld build: YouTube first, everything that needs a
# heavy dependency (terminal, file explorer, chat, IPTV, ...) is dropped.
$keep = @(
    "Main",
    "Tube",
    "Shorts",
    "Favorites",
    "Settings",
    "SettingsGeneral",
    "SettingsAppearance",
    "ThemePresets",
    "LogExporter",
    "Exit"
)

$scenes = @()
foreach ($name in $keep) {
    $s = $cfg.scenes | Where-Object { $_.name -eq $name }
    if (-not $s) { throw "Scene '$name' not found in $Src" }
    $scenes += $s
}

# --- Home screen: 6 tiles that all point at scenes kept above ---------------
$main = $scenes | Where-Object { $_.name -eq "Main" }
$recent = $main.elements | Where-Object { $_.type -eq "recent" }

function New-Tile($text, $bg, $x, $y, $target) {
    $e = $main.elements[1] | Select-Object *
    $e.text = $text
    $e.bgColor = $bg
    $e.x = $x
    $e.y = $y
    $e.trigger = "change_scene:$target"
    return $e
}

$main.elements = @(
    $recent,
    (New-Tile "Media"    "#1D4ED8" 30  324 "Tube"),
    (New-Tile "Shorts"   "#0891B2" 280 324 "Shorts"),
    (New-Tile "Favorites" "#BE185D" 30 524 "Favorites"),
    (New-Tile "Settings" "#475569" 280 524 "Settings"),
    (New-Tile "Logs"     "#B45309" 530 524 "LogExporter"),
    (New-Tile "Exit"     "#7F1D1D" 780 524 "Exit")
)

# --- Settings menu: drop the Plugins entry (needs the network) -------------
$set = $scenes | Where-Object { $_.name -eq "Settings" }
$set.elements = @($set.elements | Where-Object { $_.trigger -ne "change_scene:Plugins" })

# --- Variables: device defaults --------------------------------------------
$cfg.variables.fullscreen = $true
$cfg.variables.weatherEnabled = $false
$cfg.variables.screenWidth = 1280
$cfg.variables.screenHeight = 720
$cfg.variables.audioBackend = "ffplay"
$cfg.variables.playbackResolution = "720"
$cfg.variables.reducedMotion = $true
$cfg.variables.lowPower = $true
$cfg.variables.Custom.Fullscreen = $true
$cfg.variables.Custom.WeatherEnabled = $false
$cfg.variables.Custom.AudioBackend = "ffplay"
$cfg.variables.Custom.PlaybackResolution = "720"
$cfg.variables.Custom.ScreenWidth = 1280
$cfg.variables.Custom.ScreenHeight = 720
$cfg.variables.Custom.WeatherUnit = "C"

$out = [ordered]@{}
foreach ($p in $cfg.PSObject.Properties) {
    if ($p.Name -eq "scenes") { $out["scenes"] = $scenes } else { $out[$p.Name] = $p.Value }
}

$json = $out | ConvertTo-Json -Depth 12
$full = Join-Path (Get-Location) $Dst
[System.IO.File]::WriteAllText($full, $json)
Write-Host "Wrote $full ($($scenes.Count) scenes)"
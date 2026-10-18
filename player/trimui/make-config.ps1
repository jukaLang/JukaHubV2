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
# heavy dependency (terminal, file explorer, IPTV, ...) is dropped.
# Chat, Misc (Tools) and Emulator are kept because the home screen tiles
# reference them.
$keep = @(
    "Main",
    "Tube",
    "Shorts",
    "Apps",
    "Favorites",
    "Chat",
    "Misc",
    "Emulator",
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

# --- Home screen: 2x4 tile grid + Clock + Back -----------------------------
# Top row:    Media  Shorts  Apps  Settings
# Bottom row: Favorites  Chat  Tools  Emulator
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

function New-DarkButton($text, $x, $y, $w, $h, $trigger) {
    $e = $main.elements[1] | Select-Object *
    $e.text = $text
    $e.bgColor = "#1E293B"
    $e.color = "#F8FAFC"
    $e.x = $x
    $e.y = $y
    $e.width = $w
    $e.height = $h
    $e.trigger = $trigger
    $e.style = "dark"
    return $e
}

$main.elements = @(
    $recent,
    (New-Tile "Media"     "#1D4ED8" 30  324 "Tube"),
    (New-Tile "Shorts"    "#0891B2" 280 324 "Shorts"),
    (New-Tile "Apps"      "#15803D" 530 324 "Apps"),
    (New-Tile "Settings"  "#475569" 780 324 "Settings"),
    (New-Tile "Favorites" "#BE185D" 30  524 "Favorites"),
    (New-Tile "Chat"      "#10B981" 340 524 "Chat"),
    (New-Tile "Tools"     "#6B46C1" 648 524 "Misc"),
    (New-Tile "Emulator"  "#DC2626" 956 524 "Emulator"),
    (New-DarkButton "Clock" 1100 30  80 80 "toggle_calendar"),
    (New-DarkButton "Back"  0    672 60 36 "go_back")
)

# --- Settings menu: drop the Plugins entry (needs the network) -------------
$set = $scenes | Where-Object { $_.name -eq "Settings" }
$set.elements = @($set.elements | Where-Object { $_.trigger -ne "change_scene:Plugins" })

# --- Variables: device defaults -------------------------------------------
$cfg.variables.fullscreen = $true
$cfg.variables.weatherEnabled = $false
$cfg.variables.screenWidth = 1280
$cfg.variables.screenHeight = 720
$cfg.variables.playbackResolution = "720"
$cfg.variables.reducedMotion = $true
$cfg.variables.lowPower = $true
$cfg.variables.Custom.Fullscreen = $true
$cfg.variables.Custom.WeatherEnabled = $false
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
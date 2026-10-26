param(
    [string]$Src = "..\jukaconfig.json",
    # Named jukaconfig.device.json on purpose: player/.gitignore excludes
    # jukaconfig.json (it may hold user secrets), and the generated device
    # config has to be committed for the CI build to pick it up.
    [string]$Dst = ".\jukaconfig.device.json",
    # Deliberate trimming, for a maintainer who really wants a scene gone.
    # Anything a kept scene still navigates to is refused, so a trim can never
    # leave a dead tile behind. Dropping a scene the Apps launcher opens (see
    # builtinApplets in apps.go) still hides that app: the script cannot see
    # the Go table, so that one is on the maintainer.
    [string[]]$Drop = @()
)

$ErrorActionPreference = "Stop"
$cfg = Get-Content $Src -Raw | ConvertFrom-Json

# ---------------------------------------------------------------------------
# Scenes: keep the source's list whole.
#
# This script used to carry a hand-maintained list of "scenes that stay on the
# handheld build". It went stale in the worst way: it named Emulator, which no
# longer exists (so the script threw before writing anything), and it omitted
# JukaLand, Live TV, Podcasts, Tickers, Weather, Unit Converter, Canvas Sandbox,
# IP Stream and the Audio Player. Those scenes are rows in the Apps launcher
# (builtinApplets in apps.go), and the launcher hides a row whose scene the
# config does not ship — so the handheld silently lost nine working apps.
#
# The device config is now the full config plus device defaults. There is no
# whitelist to forget: a scene added to jukaconfig.json is in the handheld build
# the next time this runs.
# ---------------------------------------------------------------------------
$scenes = @($cfg.scenes)
if ($scenes.Count -eq 0) {
    throw "$Src has no scenes"
}

function Get-ChangeTargets($scene) {
    $targets = @()
    foreach ($el in @($scene.elements)) {
        $trigger = [string]$el.trigger
        if ($trigger.StartsWith("change_scene:")) {
            $targets += $trigger.Substring("change_scene:".Length)
        }
    }
    return @($targets | Where-Object { $_ -ne "" })
}

# --- Optional trimming, validated -----------------------------------------
if ($Drop.Count -gt 0) {
    $dropping = @{}
    foreach ($name in $Drop) { $dropping[$name] = $true }

    # A dropped scene must not be the destination of any scene that stays: the
    # button would navigate nowhere, which the menu audit reports as a bug.
    $kept = @($scenes | Where-Object { -not $dropping.ContainsKey($_.name) })
    $referenced = @{}
    foreach ($scene in $kept) {
        foreach ($target in Get-ChangeTargets $scene) {
            if ($dropping.ContainsKey($target)) {
                $referenced[$target] = $scene.name
            }
        }
    }
    if ($referenced.Count -gt 0) {
        $lines = $referenced.Keys | ForEach-Object { "  $_ (opened from $($referenced[$_]))" }
        throw ("Refusing to drop scenes that are still reachable:`n" + ($lines -join "`n"))
    }
    $scenes = $kept
    Write-Host "Dropped $($Drop -join ', ')"
}

# --- The result has to be navigable ---------------------------------------
# Every change_scene target must exist, so no button in the generated config
# points at a scene the device config does not ship. This is the check that
# would have caught the stale whitelist instead of a dead Home tile.
$names = @($scenes | ForEach-Object { $_.name })
$broken = @()
foreach ($scene in $scenes) {
    foreach ($target in Get-ChangeTargets $scene) {
        if ($names -notcontains $target) {
            $broken += "  $($scene.name) -> $target"
        }
    }
}
if ($broken.Count -gt 0) {
    throw ("$Src has buttons that navigate to missing scenes:`n" + ($broken -join "`n"))
}

# --- Variables: device defaults -------------------------------------------
# The handheld runs fullscreen at 1280x720 and never asks the network for
# weather. Everything else is the user's, so nothing else is touched here.
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
$cfg.variables.Custom.WeatherUnit = ""

$out = [ordered]@{}
foreach ($p in $cfg.PSObject.Properties) {
    if ($p.Name -eq "scenes") { $out["scenes"] = @($scenes) } else { $out[$p.Name] = $p.Value }
}

# ---------------------------------------------------------------------------
# JSON writer.
#
# ConvertTo-Json is not usable for a committed file: Windows PowerShell pads
# every key out to a column and indents nested objects by eye-watering amounts,
# and the exact layout differs between 5.1 and 7.x. Either way a regeneration
# would rewrite all ~8000 lines of the committed config, so a real change could
# never be reviewed. This writer emits the 2-space layout the repo already uses
# (see .editorconfig), in document order, with LF endings and no BOM — the same
# shape as the source jukaconfig.json.
# ---------------------------------------------------------------------------
function ConvertTo-JsonString([string]$s) {
    $sb = New-Object System.Text.StringBuilder
    [void]$sb.Append('"')
    foreach ($ch in $s.ToCharArray()) {
        switch ($ch) {
            '\' { [void]$sb.Append('\\') }
            '"' { [void]$sb.Append('\"') }
            "`n" { [void]$sb.Append('\n') }
            "`r" { [void]$sb.Append('\r') }
            "`t" { [void]$sb.Append('\t') }
            "`b" { [void]$sb.Append('\b') }
            "`f" { [void]$sb.Append('\f') }
            default {
                if ([int]$ch -lt 32) {
                    [void]$sb.Append(('\u{0:x4}' -f [int]$ch))
                } else {
                    [void]$sb.Append($ch)
                }
            }
        }
    }
    [void]$sb.Append('"')
    return $sb.ToString()
}

function ConvertTo-JsonNumber($n) {
    if ($n -is [int] -or $n -is [long] -or $n -is [int16] -or $n -is [byte]) {
        return [string]$n
    }
    return ([double]$n).ToString('R', [System.Globalization.CultureInfo]::InvariantCulture)
}

function Format-JsonValue($value, [int]$indent) {
    $pad = ' ' * (2 * $indent)
    $innerPad = ' ' * (2 * ($indent + 1))
    if ($null -eq $value) { return 'null' }
    if ($value -is [bool]) { if ($value) { return 'true' } else { return 'false' } }
    if ($value -is [string]) { return (ConvertTo-JsonString $value) }
    if ($value -is [int] -or $value -is [long] -or $value -is [double] -or
        $value -is [decimal] -or $value -is [single] -or $value -is [int16]) {
        return (ConvertTo-JsonNumber $value)
    }
    if ($value -is [System.Collections.IDictionary]) {
        $keys = @($value.Keys)
        if ($keys.Count -eq 0) { return '{}' }
        $lines = @()
        foreach ($key in $keys) {
            $lines += ($innerPad + (ConvertTo-JsonString ([string]$key)) + ': ' + (Format-JsonValue $value[$key] ($indent + 1)))
        }
        return "{`n" + ($lines -join ",`n") + "`n$pad}"
    }
    if ($value -is [System.Management.Automation.PSCustomObject]) {
        $props = @($value.PSObject.Properties)
        if ($props.Count -eq 0) { return '{}' }
        $lines = @()
        foreach ($p in $props) {
            $lines += ($innerPad + (ConvertTo-JsonString ([string]$p.Name)) + ': ' + (Format-JsonValue $p.Value ($indent + 1)))
        }
        return "{`n" + ($lines -join ",`n") + "`n$pad}"
    }
    if ($value -is [System.Collections.IEnumerable]) {
        $items = @($value)
        if ($items.Count -eq 0) { return '[]' }
        $lines = @()
        foreach ($item in $items) {
            $lines += ($innerPad + (Format-JsonValue $item ($indent + 1)))
        }
        return "[`n" + ($lines -join ",`n") + "`n$pad]"
    }
    throw "cannot write $($value.GetType().FullName) as JSON"
}

$json = Format-JsonValue $out 0
$full = Join-Path (Get-Location) $Dst
$utf8 = New-Object System.Text.UTF8Encoding($false)
[System.IO.File]::WriteAllText($full, $json, $utf8)
Write-Host "Wrote $full ($($scenes.Count) scenes)"

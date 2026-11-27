param(
    [Parameter(Mandatory = $true)][string]$Config,
    [Parameter(Mandatory = $true)][string]$Out
)

# VERSIONINFO for the Windows exe, from the same version the About screen and
# the updater read. Called by build.bat; packaging/windows/generate.py renders
# the same resource in CI (DESK-4).

$v = [string](Get-Content -LiteralPath $Config -Raw | ConvertFrom-Json).Version
if ($v -notmatch '^v?(\d+)\.(\d+)\.(\d+)$') {
    throw "Not a release version in $Config : $v"
}
$major = $Matches[1]
$minor = $Matches[2]
$patch = $Matches[3]
$q = [char]34

$lines = @(
    '/* generated from jukaconfig.json */'
    '1 VERSIONINFO'
    " FILEVERSION $major,$minor,$patch,0"
    " PRODUCTVERSION $major,$minor,$patch,0"
    ' FILEFLAGSMASK 0x3fL'
    ' FILEOS 0x40004L'
    ' FILETYPE 0x1L'
    ' FILESUBTYPE 0x0L'
    'BEGIN'
    (' BLOCK ' + $q + 'StringFileInfo' + $q)
    ' BEGIN'
    ('  BLOCK ' + $q + '040904b0' + $q)
    '  BEGIN'
    ('   VALUE ' + $q + 'CompanyName' + $q + ', ' + $q + 'jukaLang' + $q)
    ('   VALUE ' + $q + 'FileDescription' + $q + ', ' + $q + 'JukaHub handheld launcher' + $q)
    ('   VALUE ' + $q + 'FileVersion' + $q + ', ' + $q + $v + $q)
    ('   VALUE ' + $q + 'InternalName' + $q + ', ' + $q + 'JukaHub' + $q)
    ('   VALUE ' + $q + 'LegalCopyright' + $q + ', ' + $q + 'jukaLang' + $q)
    ('   VALUE ' + $q + 'OriginalFilename' + $q + ', ' + $q + 'JukaHub.exe' + $q)
    ('   VALUE ' + $q + 'ProductName' + $q + ', ' + $q + 'JukaHub' + $q)
    ('   VALUE ' + $q + 'ProductVersion' + $q + ', ' + $q + $v + $q)
    '  END'
    ' END'
    (' BLOCK ' + $q + 'VarFileInfo' + $q)
    ' BEGIN'
    ('  VALUE ' + $q + 'Translation' + $q + ', 0x409, 1200')
    ' END'
    'END'
)
[IO.File]::WriteAllLines($Out, $lines)
Write-Output "version resource: $v"

param(
    [Parameter(Mandatory = $true)][string]$Serial,
    [string]$Adb = "$env:LOCALAPPDATA\Android\Sdk\platform-tools\adb.exe",
    [switch]$SkipBuild
)
$ErrorActionPreference = 'Stop'
$project = Split-Path $PSScriptRoot -Parent
function Invoke-Adb {
    & $Adb -s $Serial @args
    if ($LASTEXITCODE -ne 0) { throw "adb failed ($LASTEXITCODE)" }
}
if (-not (Test-Path "$project\dist\sysutilsd")) { throw 'Build the native service first (tools/build-service.sh)' }
if (-not $SkipBuild) {
    Push-Location $project
    try { & .\gradlew.bat :app:assembleDebug --console=plain; if ($LASTEXITCODE -ne 0) { throw 'Build failed' } }
    finally { Pop-Location }
}
Invoke-Adb root
Invoke-Adb wait-for-device
Invoke-Adb shell mkdir -p /data/local/sysutils/bin
Invoke-Adb push "$project\dist\sysutilsd" /data/local/sysutils/bin/sysutilsd
Invoke-Adb shell chmod 700 /data/local/sysutils /data/local/sysutils/bin /data/local/sysutils/bin/sysutilsd
Invoke-Adb install -r "$project\app\build\outputs\apk\debug\app-debug.apk"
Invoke-Adb push "$project\tools\start-manual.sh" /data/local/tmp/sysutils-start-manual.sh
Invoke-Adb push "$project\tools\pair.sh" /data/local/tmp/sysutils-pair.sh
Invoke-Adb shell sh /data/local/tmp/sysutils-start-manual.sh
Invoke-Adb shell sh /data/local/tmp/sysutils-pair.sh
Write-Host 'Service started manually. Automatic boot integration is not installed.'
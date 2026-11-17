#!/system/bin/sh
set -eu
umask 077
root=/data/local/sysutils
mkdir -p "$root"
chmod 700 "$root"
[ -x "$root/bin/sysutilsd" ] || { echo 'Install the service binary in /data/local/sysutils/bin first'; exit 1; }
if pidof sysutilsd >/dev/null; then
    echo 'SysUtils is already running'
    exit 0
fi
nohup "$root/bin/sysutilsd" > "$root/daemon.log" 2>&1 < /dev/null &
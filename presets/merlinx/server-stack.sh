#!/system/bin/sh
set -eu
n=0
until ip -4 addr show wlan0 | grep -q 'inet '; do
    n=$((n+1))
    [ "$n" -lt 90 ] || { echo 'Wi-Fi address not available'; exit 1; }
    sleep 2
done
/system/bin/sh /data/local/server/start.sh
/data/local/server/debian docker ps
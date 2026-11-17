#!/system/bin/sh
set -eu
settings put global airplane_mode_radios cell
cmd connectivity airplane-mode enable
svc wifi enable
svc bluetooth enable
stop vendor.ril-daemon-mtk
start vendor.fingerprint-default
# Stop the modem loader only after the modem is powered off (GATED).
n=0
until grep -q 'md1:1 ' /sys/kernel/ccci/boot; do
    n=$((n+1))
    [ "$n" -lt 45 ] || { echo 'Modem did not reach GATED state'; exit 1; }
    sleep 2
done
stop ccci_mdinit
cat /sys/kernel/ccci/boot
echo 'Modem loader stopped; reattached fingerprint reader enabled'

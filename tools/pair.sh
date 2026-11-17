#!/system/bin/sh
set -eu
run-as com.krashkanter.droidutils sh -c 'umask 077; mkdir -p files; cat > files/token' < /data/local/sysutils/token
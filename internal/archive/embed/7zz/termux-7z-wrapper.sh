#!/system/bin/sh
# Wrapper to run p7zip with full TERMUX environment.
# This is embedded in the vrhub-server binary and extracted at runtime on Android.
PREFIX="/data/data/com.termux/files/usr"
export PREFIX
export LD_LIBRARY_PATH="${PREFIX}/lib:${LD_LIBRARY_PATH:-}"
export HOME="/data/user/0/com.termux/files/home"
exec "${PREFIX}/lib/p7zip/7z" "$@"

#!/bin/sh
# usage: try.sh <file>  (Haiku sources are given as $V/src/kits/..; MODE=-c OUT=x.o emits an object)
V=${HAIKU_SRC:-/home/arti/projects/Vitruvian}
H=$(dirname "$0")
exec g++ -std=gnu++17 ${MODE:--fsyntax-only} -Wno-multichar -fmax-errors=0 -include $H/shim/haiku_prelude.h \
 -I$H/shim -I$H/shim/prim -I$V/headers -I$V/headers/os -I$V/headers/os/app -I$V/headers/os/support \
 -I$V/headers/os/storage -I$V/headers/os/interface -I$V/headers/os/locale -I$V/headers/os/kernel -I$V/headers/private -I$V/headers/private/app \
 -I$V/headers/private/shared -I$V/headers/private/support -I$V/headers/private/kernel -I$V/headers/private/interface -I$V/headers/private/system -I$V/headers/os/drivers -I$V/headers/private/system/arch/x86_64 -I$V/src/kits/storage -I$V/headers/private/storage -I$V/headers/posix -I$V/headers/build/config_headers \
 "${1}" ${OUT:+-o $OUT}

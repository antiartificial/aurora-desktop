# EXPERIMENT (decision 0008, "does Haiku's own source compile against our kits?"). Standalone: not wired into
# ../CMakeLists.txt. Haiku sources are referenced IN PLACE (read-only, MIT, Haiku Inc. / Vitruvian Project),
# never copied. See README.md. Usage: cmake -S . -B build -G Ninja && cmake --build build && build/test_flat
cmake_minimum_required(VERSION 3.20)
project(haiku_compat C CXX)
# An absolute default, overridable with -DHAIKU_SRC=... or the HAIKU_SRC environment variable, so
# a checkout somewhere else can build at all. The default is one machine's path and always was.
if(NOT HAIKU_SRC AND DEFINED ENV{HAIKU_SRC})
	set(HAIKU_SRC "$ENV{HAIKU_SRC}")
endif()
set(HAIKU_SRC "/home/arti/projects/Vitruvian" CACHE PATH "Vitruvian/Haiku checkout (read-only)")
include(${CMAKE_CURRENT_LIST_DIR}/CheckHaikuSrc.cmake)
aurora_check_haiku_src("${HAIKU_SRC}" "${CMAKE_CURRENT_LIST_DIR}/../haiku-src.lock")
set(K ${HAIKU_SRC}/src/kits)
set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_EXTENSIONS ON)

add_library(kitsos_real STATIC ../src/shm.c ../src/port.c ../src/area.c ../src/sem.c ../src/thread.c)
target_include_directories(kitsos_real PRIVATE ../include)  # PRIVATE: our Message.h etc. must NOT shadow Haiku's
target_link_libraries(kitsos_real PUBLIC pthread)

add_library(haiku_app_real STATIC
	${K}/app/Message.cpp ${K}/app/Handler.cpp ${K}/app/Looper.cpp ${K}/app/Messenger.cpp
	${K}/app/MessageAdapter.cpp ${K}/app/LooperList.cpp ${K}/app/TokenSpace.cpp ${K}/app/MessageQueue.cpp
	${K}/app/DirectMessageTarget.cpp ${K}/app/MessageFilter.cpp ${K}/app/MessageUtils.cpp ${K}/app/PropertyInfo.cpp
	${K}/support/String.cpp ${K}/support/List.cpp ${K}/support/Archivable.cpp ${K}/support/ArchivingManagers.cpp
	${K}/support/ByteOrder.cpp ${K}/support/BlockCache.cpp ${K}/support/DataIO.cpp ${K}/support/Locker.cpp
	${K}/support/StringList.cpp ${K}/support/Flattenable.cpp ${K}/interface/Rect.cpp ${K}/interface/Point.cpp
	${HAIKU_SRC}/src/system/libroot2/KMessage.cpp
	stubs.cpp)
# Order matters: our shim OS.h first, then our primitives, then Haiku's real headers.
target_include_directories(haiku_app_real SYSTEM PUBLIC shim shim/prim
	${HAIKU_SRC}/headers ${HAIKU_SRC}/headers/os ${HAIKU_SRC}/headers/os/app ${HAIKU_SRC}/headers/os/support
	${HAIKU_SRC}/headers/os/storage ${HAIKU_SRC}/headers/os/interface ${HAIKU_SRC}/headers/os/locale
	${HAIKU_SRC}/headers/os/kernel ${HAIKU_SRC}/headers/private ${HAIKU_SRC}/headers/private/app
	${HAIKU_SRC}/headers/private/shared ${HAIKU_SRC}/headers/private/support ${HAIKU_SRC}/headers/private/kernel
	${HAIKU_SRC}/headers/private/interface ${HAIKU_SRC}/headers/private/system ${HAIKU_SRC}/headers/os/drivers
	${HAIKU_SRC}/headers/private/system/arch/x86_64 ${HAIKU_SRC}/headers/private/storage
	${HAIKU_SRC}/headers/build/config_headers)
target_compile_options(haiku_app_real PUBLIC -include ${CMAKE_CURRENT_SOURCE_DIR}/shim/haiku_prelude.h -Wno-multichar -w -ffunction-sections)
target_link_libraries(haiku_app_real PUBLIC kitsos_real)

add_executable(test_flat test_flat.cpp)
target_link_options(test_flat PRIVATE -Wl,--gc-sections)
target_link_libraries(test_flat haiku_app_real)

add_executable(test_looper test_looper.cpp)
target_link_options(test_looper PRIVATE -Wl,--gc-sections)
target_link_libraries(test_looper haiku_app_real)

# ---- haiku_ledger_real: a live bus service on Haiku's real BMessage/BHandler/BLooper/BMessenger ----
# The bridge is the SAME source as kits/src/bus (public API only), compiled here against Haiku's headers.
set(B ${CMAKE_CURRENT_SOURCE_DIR}/../src/bus)
add_library(kitsbus_real STATIC ${B}/Json.cpp ${B}/BusClient.cpp ${B}/BusConvert.cpp ${B}/BusBridge.cpp)
target_include_directories(kitsbus_real PUBLIC ${CMAKE_CURRENT_SOURCE_DIR}/../include/bus)
target_link_libraries(kitsbus_real PUBLIC haiku_app_real)

# NO --unresolved-symbols=ignore-all anywhere. BPath is defined (stubs.cpp). BEntry/BFile/BNode/BAppFileInfo
# are referenced only by BArchivable's instantiate-by-name; -ffunction-sections + --gc-sections drop it, so the
# link succeeds only while nothing reachable needs them, and fails loudly if that changes.
add_executable(haiku_ledger_real ../demo/haiku_ledger_real.cpp real_init.cpp)
target_include_directories(haiku_ledger_real PRIVATE ../demo)
target_link_options(haiku_ledger_real PRIVATE -Wl,--gc-sections)
target_link_libraries(haiku_ledger_real kitsbus_real)

cmake_minimum_required(VERSION 3.20)
project(aurora_kits C CXX)
set(CMAKE_C_STANDARD 11)
set(CMAKE_CXX_STANDARD 23)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
add_compile_options(-Wall -Wextra -Wno-unused-parameter -Wno-multichar)
enable_testing()

add_library(kitsos STATIC src/shm.c src/port.c src/area.c src/sem.c src/thread.c)
target_include_directories(kitsos PUBLIC include)
target_link_libraries(kitsos PUBLIC pthread)

# Messaging core above the primitives: BMessage, BHandler, BLooper, BMessenger, BApplication.
add_library(kitsapp STATIC src/app/Message.cpp src/app/Handler.cpp src/app/Looper.cpp
	src/app/Messenger.cpp src/app/Application.cpp)
target_include_directories(kitsapp PUBLIC include PRIVATE src/app)
target_link_libraries(kitsapp PUBLIC kitsos)

add_executable(test_kits tests/test_kits.cpp)
target_link_libraries(test_kits kitsos)
add_test(NAME kits COMMAND test_kits)

add_executable(demo_two_process demo/demo_two_process.cpp)
target_link_libraries(demo_two_process kitsos)
add_test(NAME demo_two_process COMMAND demo_two_process)

add_executable(test_app tests/test_app.cpp)
target_link_libraries(test_app kitsapp)
add_test(NAME app COMMAND test_app)

# Haiku-style two-process demo: one binary, "server" and "client" modes.
add_executable(haiku_app demo/haiku_app.cpp)
target_link_libraries(haiku_app kitsapp)
add_test(NAME haiku_app COMMAND haiku_app)

# Bus bridge: a C++ bus client and the BLooper <-> bus bridge (decision 0008's untested claim).
add_library(kitsbus STATIC src/bus/Json.cpp src/bus/BusClient.cpp src/bus/BusConvert.cpp src/bus/BusBridge.cpp)
target_include_directories(kitsbus PUBLIC include include/bus)
target_link_libraries(kitsbus PUBLIC kitsapp)

add_executable(haiku_ledger demo/haiku_ledger.cpp)
target_link_libraries(haiku_ledger kitsbus)

add_executable(test_bus tests/test_bus.cpp)
target_link_libraries(test_bus kitsbus)
# The real-busd tests need the Go toolchain; they build busd themselves into the build tree.
find_program(GO_EXE go)
if(GO_EXE)
  # Always run, rather than an add_custom_command with OUTPUT files. That form
  # had no DEPENDS on the Go sources, so once build/busd existed ninja held it
  # up to date forever: bus_bridge, bus_demo and every demo that takes busd
  # from this build tree were testing whatever daemon happened to be built
  # first. One was found 14 hours stale, which cost an hour of chasing a
  # failure in code that had already been fixed.
  #
  # Listing the Go sources as DEPENDS would be the other fix and is worse: the
  # list goes stale the moment a file is added, and it fails the same silent
  # way. Go has its own build cache, so an unconditional `go build` is close to
  # free when nothing changed and correct when something did.
  add_custom_target(bus_binaries ALL
    COMMAND ${CMAKE_COMMAND} -E env ${GO_EXE} build -o ${CMAKE_BINARY_DIR}/busd ./cmd/busd
    COMMAND ${CMAKE_COMMAND} -E env ${GO_EXE} build -o ${CMAKE_BINARY_DIR}/bus ./cmd/bus
    WORKING_DIRECTORY ${CMAKE_SOURCE_DIR}/../bus
    BYPRODUCTS ${CMAKE_BINARY_DIR}/busd ${CMAKE_BINARY_DIR}/bus
    COMMENT "building busd and bus (Go)")
  add_dependencies(test_bus bus_binaries)
  add_test(NAME bus_bridge COMMAND test_bus ${CMAKE_BINARY_DIR}/busd)
  add_test(NAME bus_demo COMMAND ${CMAKE_SOURCE_DIR}/demo/bus_demo.sh ${CMAKE_BINARY_DIR})
  set_tests_properties(bus_demo PROPERTIES TIMEOUT 60)
else()
  add_test(NAME bus_bridge COMMAND test_bus)   # busd tests skip without Go
endif()

# The shared substrate (render) and the aurora::ui toolkit (ui) build WITHOUT the Haiku checkout.
# That is the point of ADR 0003: a clean checkout builds the toolkit, atlas and the chooser.
add_subdirectory(render)
add_subdirectory(ui)

# Real Haiku kit code (Vitruvian's src/kits/app, compiled in place, read-only): haiku_ledger_real and the
# haiku-compat + compat tests. Off when the Haiku source or Go (for busd) is absent. See haiku-compat/README.md.
set(HAIKU_SRC "/home/arti/projects/Vitruvian" CACHE PATH "Vitruvian/Haiku checkout (read-only)")
if(EXISTS ${HAIKU_SRC}/src/kits/app/Message.cpp)
  add_subdirectory(haiku-compat)
  add_subdirectory(compat)
  add_test(NAME haiku_real_flat COMMAND test_flat)
  add_test(NAME haiku_real_looper COMMAND test_looper)
  if(GO_EXE)
    add_dependencies(haiku_ledger_real bus_binaries)
    add_test(NAME bus_demo_real COMMAND ${CMAKE_SOURCE_DIR}/demo/bus_demo_real.sh
      ${CMAKE_BINARY_DIR}/busd ${CMAKE_BINARY_DIR}/bus $<TARGET_FILE:haiku_ledger_real>)
    set_tests_properties(bus_demo_real PROPERTIES TIMEOUT 60)
  endif()
endif()

cmake_minimum_required(VERSION 3.20)
project(aurora_kits C CXX)
set(CMAKE_C_STANDARD 11)
set(CMAKE_CXX_STANDARD 17)
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

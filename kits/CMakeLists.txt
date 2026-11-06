cmake_minimum_required(VERSION 3.20)
project(aurora_kits C CXX)
set(CMAKE_C_STANDARD 11)
set(CMAKE_CXX_STANDARD 17)
add_compile_options(-Wall -Wextra -Wno-unused-parameter)
enable_testing()

add_library(kitsos STATIC src/shm.c src/port.c src/area.c src/sem.c src/thread.c)
target_include_directories(kitsos PUBLIC include)
target_link_libraries(kitsos PUBLIC pthread)

add_executable(test_kits tests/test_kits.cpp)
target_link_libraries(test_kits kitsos)
add_test(NAME kits COMMAND test_kits)

add_executable(demo_two_process demo/demo_two_process.cpp)
target_link_libraries(demo_two_process kitsos)
add_test(NAME demo_two_process COMMAND demo_two_process)

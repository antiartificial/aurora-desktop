# ui/ — the aurora::ui toolkit (the new kit). Links ../render only — never haiku_iface_real.
# This is the library that used to live inside interface/CMakeLists.txt's "aurora::ui" block;
# lifting it out of that file, together with ../render, is ADR 0003: on a clean checkout (no
# HAIKU_SRC) this directory still builds atlas, aurora-chooser, tk_layout and the toolkit tests.

add_library(aurora_ui STATIC au_draw.cpp)
target_include_directories(aurora_ui PUBLIC ${CMAKE_CURRENT_SOURCE_DIR})
target_link_libraries(aurora_ui PUBLIC aurora_render)
if(TARGET aurora_wl_present)
	target_link_libraries(aurora_ui PUBLIC aurora_wl_present)
endif()

add_executable(aurora_ui_spike demo/spike.cpp)
target_link_libraries(aurora_ui_spike PRIVATE aurora_ui)

add_library(aurora_ui_tk STATIC ui.cpp)
target_link_libraries(aurora_ui_tk PUBLIC aurora_ui)

# Layout, headless. No window and no fonts: Measure/Place are called directly and the frames are
# read back, which is why this is a test and the other aurora-ui demos are demos.
add_executable(tk_layout demo/tk_layout.cpp)
target_link_libraries(tk_layout PRIVATE aurora_ui_tk)
add_test(NAME tk_layout COMMAND tk_layout)

# The toolkit foundations Atlas is built on: the column rule, the icon model (procedural, vector
# and bitmap), the transition primitive, and the three selection states that must never render
# alike. Headless, and every drawing claim is checked at scale 1 AND 1.45.
add_executable(test_ui_toolkit tests/test_ui_toolkit.cpp)
target_link_libraries(test_ui_toolkit PRIVATE aurora_ui_tk)
add_test(NAME ui_toolkit COMMAND test_ui_toolkit)
set_tests_properties(ui_toolkit PROPERTIES TIMEOUT 60)

# ADR 0009 / docs 00 §8.0: the tree-swap use-after-free, proven gone under ASan. State<T>::set is a
# header template, so it instantiates in this TU and instruments the freed read even though the node
# is allocated in the uninstrumented library (ASan intercepts malloc/free globally).
add_executable(test_root_swap tests/test_root_swap.cpp)
target_link_libraries(test_root_swap PRIVATE aurora_ui_tk)
target_compile_options(test_root_swap PRIVATE -fsanitize=address -fno-omit-frame-pointer -g)
target_link_options(test_root_swap PRIVATE -fsanitize=address)
add_test(NAME root_swap COMMAND test_root_swap)
set_tests_properties(root_swap PROPERTIES TIMEOUT 60
	ENVIRONMENT "ASAN_OPTIONS=detect_leaks=0")   # the claim is "no use-after-free", not "no leaks"

# 8.2 foundation: Binding<T> converts from State<T>& and a literal and survives a cell's death.
# ASan proves the liveness token stops the freed read.
add_executable(test_binding tests/test_binding.cpp)
target_link_libraries(test_binding PRIVATE aurora_ui_tk)
target_compile_options(test_binding PRIVATE -fsanitize=address -fno-omit-frame-pointer -g)
target_link_options(test_binding PRIVATE -fsanitize=address)
add_test(NAME binding COMMAND test_binding)
set_tests_properties(binding PROPERTIES TIMEOUT 60 ENVIRONMENT "ASAN_OPTIONS=detect_leaks=0")

# 8.2b: set() from a worker is race-free (worker defers; v_ is written only on the loop thread).
# ThreadSanitizer proves there is no data race between a worker's set() and the loop's get().
add_executable(test_threadsafe_set tests/test_threadsafe_set.cpp)
target_link_libraries(test_threadsafe_set PRIVATE aurora_ui_tk)
target_compile_options(test_threadsafe_set PRIVATE -fsanitize=thread -fno-omit-frame-pointer -g -O1)
target_link_options(test_threadsafe_set PRIVATE -fsanitize=thread)
add_test(NAME threadsafe_set COMMAND test_threadsafe_set)
set_tests_properties(threadsafe_set PROPERTIES TIMEOUT 120)

# 8.2e: Derived<T> — declared sources, topological (declaration) order, lazy recompute, read-only
# Binding. ASan, because a derived holds the sources it reads.
add_executable(test_derived tests/test_derived.cpp)
target_link_libraries(test_derived PRIVATE aurora_ui_tk)
target_compile_options(test_derived PRIVATE -fsanitize=address -fno-omit-frame-pointer -g)
target_link_options(test_derived PRIVATE -fsanitize=address)
add_test(NAME derived COMMAND test_derived)
set_tests_properties(derived PROPERTIES TIMEOUT 60 ENVIRONMENT "ASAN_OPTIONS=detect_leaks=0")

# Launcher inventory model: section order, empty-section skipping, and the enabled/note rules.
# Pure header, ASan, no window.
add_executable(test_launcher_model tests/test_launcher_model.cpp)
target_compile_options(test_launcher_model PRIVATE -fsanitize=address -fno-omit-frame-pointer -g)
target_link_options(test_launcher_model PRIVATE -fsanitize=address)
add_test(NAME launcher_model COMMAND test_launcher_model)
set_tests_properties(launcher_model PROPERTIES TIMEOUT 60 ENVIRONMENT "ASAN_OPTIONS=detect_leaks=0")

# 8.2c: Task<F,G> — work off the loop, done on it. ASan: the worker threads and the teardown join.
add_executable(test_task tests/test_task.cpp)
target_link_libraries(test_task PRIVATE aurora_ui_tk)
target_compile_options(test_task PRIVATE -fsanitize=address -fno-omit-frame-pointer -g)
target_link_options(test_task PRIVATE -fsanitize=address)
add_test(NAME task COMMAND test_task)
set_tests_properties(task PROPERTIES TIMEOUT 60 ENVIRONMENT "ASAN_OPTIONS=detect_leaks=0")

# The component sheets the design documents show. A GENERATOR, not a test: it asserts nothing and
# draws the real nodes into an offscreen surface. zlib is only here for the PNG encoder.
find_package(ZLIB QUIET)
if(ZLIB_FOUND)
	add_executable(ui_specimen tests/ui_specimen.cpp)
	target_link_libraries(ui_specimen PRIVATE aurora_ui_tk ZLIB::ZLIB)
endif()

# Atlas stage 1, "the frame". Headless, every drawing claim checked at 1.0 and 1.45.
add_executable(test_atlas_frame tests/test_atlas_frame.cpp)
target_include_directories(test_atlas_frame PRIVATE ${CMAKE_CURRENT_SOURCE_DIR})
target_link_libraries(test_atlas_frame PRIVATE aurora_ui_tk)
add_test(NAME atlas_frame COMMAND test_atlas_frame)
set_tests_properties(atlas_frame PROPERTIES TIMEOUT 60)

# Atlas links the bus client so its context menu can ask registry.plumb@v1 what can be done with
# a file. aurora_ui_tk itself does NOT: a toolkit that needs a message bus to draw a button would
# be the wrong shape.
add_executable(atlas demo/atlas.cpp ../src/bus/BusClient.cpp ../src/bus/Json.cpp)
target_include_directories(atlas PRIVATE ../include/bus ../include)
target_link_libraries(atlas PRIVATE aurora_ui_tk)

# aurora-launcher: the app inventory (decision 0025), started by the compositor's System row and
# Super+Space. Same shape as Atlas: an aurora::ui window + a BusClient request/reply.
add_executable(aurora-launcher launcher.cpp ../src/bus/BusClient.cpp ../src/bus/Json.cpp)
target_include_directories(aurora-launcher PRIVATE ../include/bus ../include)
target_link_libraries(aurora-launcher PRIVATE aurora_ui_tk)

# aurora-chooser: the graphical file picker the capability model asks for (argv in, one JSON line
# out; see chooser_model.h for the contract). A shipped binary, so it lives here and not in demo.
add_executable(aurora-chooser chooser.cpp)
target_link_libraries(aurora-chooser PRIVATE aurora_ui_tk)

# Everything about the chooser that needs no surface: argv, verdicts, words, the exact stdout line.
add_executable(test_chooser_model tests/test_chooser_model.cpp ../src/bus/Json.cpp)
target_include_directories(test_chooser_model PRIVATE ../include/bus)
add_test(NAME chooser_model COMMAND test_chooser_model)
# The binary's own behaviour with no display: what it prints, and the exit code a caller branches on.
add_test(NAME chooser_cli COMMAND sh ${CMAKE_CURRENT_SOURCE_DIR}/tests/chooser_cli.sh $<TARGET_FILE:aurora-chooser>)
# The real window, on the headless compositor, driven by keys. Reports skipped (exit 77) when the
# Rust compositor has not been built.
add_test(NAME chooser_ui COMMAND sh ${CMAKE_CURRENT_SOURCE_DIR}/tests/chooser_ui.sh $<TARGET_FILE:aurora-chooser>)
set_tests_properties(chooser_model chooser_cli PROPERTIES TIMEOUT 30)
set_tests_properties(chooser_ui PROPERTIES TIMEOUT 120 SKIP_RETURN_CODE 77)
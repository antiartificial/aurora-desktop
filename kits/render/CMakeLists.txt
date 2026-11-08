# render/ — the shared substrate: the software rasteriser (canvas), the text backend
# (fonts over FreeType + HarfBuzz), and the Wayland presenter (present_wl). Linked by BOTH
# the new toolkit (../ui) and the Haiku app_server analogue (../compat); this is the "share
# the substrate, share no widgets" rule (decision 0023 / docs 11). None of it includes a
# Haiku header, which is what lifts the toolkit out of the HAIKU_SRC gate (ADR 0003).
find_package(PkgConfig REQUIRED)
pkg_check_modules(FT2 REQUIRED IMPORTED_TARGET freetype2)
pkg_check_modules(HB REQUIRED IMPORTED_TARGET harfbuzz)

# The rasteriser + text backend, as plain data in/out (text_backend.h is the seam).
add_library(aurora_render STATIC canvas.cpp fonts.cpp)
target_include_directories(aurora_render PUBLIC ${CMAKE_CURRENT_SOURCE_DIR})
target_link_libraries(aurora_render PUBLIC PkgConfig::FT2 PkgConfig::HB)

# Wayland presenter: only when libwayland-client and wayland-scanner exist. xdg-shell.xml is
# found, not vendored. AURORA_HAVE_WAYLAND is PUBLIC so whichever library links this target
# gets the define alongside the symbols.
pkg_check_modules(WLC IMPORTED_TARGET wayland-client)
pkg_check_modules(XKB IMPORTED_TARGET xkbcommon)
find_program(WAYLAND_SCANNER wayland-scanner)
find_file(XDG_SHELL_XML xdg-shell.xml PATHS /usr/share/wayland-protocols/stable/xdg-shell
	/usr/share/qt6/wayland/protocols/xdg-shell /usr/share/qt5/wayland/protocols/xdg-shell NO_DEFAULT_PATH)
if(WLC_FOUND AND XKB_FOUND AND WAYLAND_SCANNER AND XDG_SHELL_XML)
	set(GEN ${CMAKE_CURRENT_BINARY_DIR}/gen)
	file(MAKE_DIRECTORY ${GEN})
	add_custom_command(OUTPUT ${GEN}/xdg-shell-protocol.c ${GEN}/xdg-shell-client-protocol.h
		COMMAND ${WAYLAND_SCANNER} private-code ${XDG_SHELL_XML} ${GEN}/xdg-shell-protocol.c
		COMMAND ${WAYLAND_SCANNER} client-header ${XDG_SHELL_XML} ${GEN}/xdg-shell-client-protocol.h
		DEPENDS ${XDG_SHELL_XML})
	add_library(aurora_wl_present STATIC present_wl.c ${GEN}/xdg-shell-protocol.c)
	target_include_directories(aurora_wl_present PUBLIC ${CMAKE_CURRENT_SOURCE_DIR} PRIVATE ${GEN})
	target_link_libraries(aurora_wl_present PUBLIC PkgConfig::WLC PkgConfig::XKB)
	target_compile_definitions(aurora_wl_present PUBLIC AURORA_HAVE_WAYLAND=1)
endif()
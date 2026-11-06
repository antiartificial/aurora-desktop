# Haiku's real Interface Kit (View.cpp, Window.cpp, Font.cpp, Button.cpp, ...) compiled IN PLACE from
# HAIKU_SRC, unmodified, on top of haiku_app_real (../haiku-compat). Nothing is copied. Added by
# ../CMakeLists.txt after haiku-compat. See README.md for what is real, what is shimmed, and why.
find_package(PkgConfig REQUIRED)
pkg_check_modules(FT2 REQUIRED IMPORTED_TARGET freetype2)
pkg_check_modules(ICU REQUIRED IMPORTED_TARGET icu-uc)
pkg_check_modules(WLC IMPORTED_TARGET wayland-client)
pkg_check_modules(XKB IMPORTED_TARGET xkbcommon)
find_program(WAYLAND_SCANNER wayland-scanner)

set(K ${HAIKU_SRC}/src/kits)
set(I ${K}/interface)

# ---- Haiku source compiled in place -------------------------------------------------------------
set(IFACE_REAL
	AbstractLayoutItem AbstractLayout AffineTransform Alignment Bitmap BMCPrivate Button ColorConversion
	ControlLook Control Deskbar Font GradientConic GradientDiamond GradientLinear Gradient
	GradientRadialFocus GradientRadial GraphicsDefs GroupLayout HaikuControlLook Icon InterfaceDefs
	LayoutContext LayoutItem Layout LayoutUtils MenuBar MenuField MenuItem MenuItemPrivate Menu MenuPrivate
	MenuWindow Picture PicturePlayer Polygon PopUpMenu PrivateScreen Region RegionSupport Screen ScrollBar
	SeparatorItem Shape ScrollView ListItem ListView OutlineListView StringItem StringView TextView ToolTipManager ToolTip TwoDimensionalLayout ViewLayoutItem View
	Window)
set(SRC_IFACE)
foreach(n ${IFACE_REAL})
	list(APPEND SRC_IFACE ${I}/${n}.cpp)
endforeach()
file(GLOB SRC_LAYOUTER ${I}/layouter/*.cpp)
file(GLOB SRC_TEXTVIEW ${I}/textview_support/*.cpp)
set(SRC_APP ${K}/app/AppDefs.cpp ${K}/app/AppServerLink.cpp ${K}/app/Cursor.cpp ${K}/app/Invoker.cpp
	${K}/app/LinkReceiver.cpp ${K}/app/LinkSender.cpp ${K}/app/MessageRunner.cpp ${K}/app/PortLink.cpp
	${K}/app/ServerLink.cpp ${K}/app/ServerMemoryAllocator.cpp)
set(SRC_SUPPORT ${K}/support/PointerList.cpp ${K}/support/Referenceable.cpp ${K}/locale/UnicodeChar.cpp
	${CMAKE_CURRENT_SOURCE_DIR}/stubs_iface.cpp ${CMAKE_CURRENT_SOURCE_DIR}/input_shim.cpp)

add_library(haiku_iface_real STATIC ${SRC_IFACE} ${SRC_LAYOUTER} ${SRC_TEXTVIEW} ${SRC_APP} ${SRC_SUPPORT})
# Order: our shim syscalls.h first (Haiku's needs the kernel info-struct zoo), then haiku_app_real's include set.
target_include_directories(haiku_iface_real SYSTEM PUBLIC ${CMAKE_CURRENT_SOURCE_DIR}/shim
	${HAIKU_SRC}/headers/private/print ${HAIKU_SRC}/headers/private/tracker ${HAIKU_SRC}/headers/private/locale
	${HAIKU_SRC}/headers/private/input ${HAIKU_SRC}/headers/os/add-ons/graphics ${HAIKU_SRC}/headers/os/input
	${I} ${I}/layouter ${I}/textview_support)
target_link_libraries(haiku_iface_real PUBLIC haiku_app_real PkgConfig::ICU)
# ServerMemoryAllocator: LIBBE_TEST drops the address-range reservation (_kern_reserve_address_range).
set_source_files_properties(${K}/app/ServerMemoryAllocator.cpp PROPERTIES COMPILE_DEFINITIONS HAIKU_TARGET_PLATFORM_LIBBE_TEST)

# ---- the app_server analogue (ours, written fresh) --------------------------------------------------
set(AS_SRC appserver/server.cpp appserver/canvas.cpp appserver/fonts.cpp applite.cpp)
add_library(aurora_appserver STATIC ${AS_SRC})
target_include_directories(aurora_appserver PUBLIC appserver)
pkg_check_modules(HB REQUIRED IMPORTED_TARGET harfbuzz)
target_link_libraries(aurora_appserver PUBLIC haiku_iface_real PkgConfig::FT2 PkgConfig::HB)

# Wayland presenter: only when libwayland-client and wayland-scanner exist. xdg-shell.xml is found, not vendored.
find_file(XDG_SHELL_XML xdg-shell.xml PATHS /usr/share/wayland-protocols/stable/xdg-shell
	/usr/share/qt6/wayland/protocols/xdg-shell /usr/share/qt5/wayland/protocols/xdg-shell NO_DEFAULT_PATH)
if(WLC_FOUND AND XKB_FOUND AND WAYLAND_SCANNER AND XDG_SHELL_XML)
	set(GEN ${CMAKE_CURRENT_BINARY_DIR}/gen)
	file(MAKE_DIRECTORY ${GEN})
	add_custom_command(OUTPUT ${GEN}/xdg-shell-protocol.c ${GEN}/xdg-shell-client-protocol.h
		COMMAND ${WAYLAND_SCANNER} private-code ${XDG_SHELL_XML} ${GEN}/xdg-shell-protocol.c
		COMMAND ${WAYLAND_SCANNER} client-header ${XDG_SHELL_XML} ${GEN}/xdg-shell-client-protocol.h
		DEPENDS ${XDG_SHELL_XML})
	add_library(aurora_wl_present STATIC appserver/present_wl.c ${GEN}/xdg-shell-protocol.c)
	target_include_directories(aurora_wl_present PUBLIC appserver PRIVATE ${GEN})
	target_link_libraries(aurora_wl_present PUBLIC PkgConfig::WLC PkgConfig::XKB)
	target_compile_definitions(aurora_appserver PUBLIC AURORA_HAVE_WAYLAND=1)
	target_link_libraries(aurora_appserver PUBLIC aurora_wl_present)
endif()

function(iface_exe name)
	add_executable(${name} ${ARGN} ${CMAKE_CURRENT_SOURCE_DIR}/../haiku-compat/real_init.cpp)
	target_link_options(${name} PRIVATE -Wl,--gc-sections)
	target_link_libraries(${name} aurora_appserver)
endfunction()

iface_exe(test_font tests/test_font.cpp)
target_link_libraries(test_font PkgConfig::FT2)
iface_exe(test_iface_window tests/test_window.cpp)
iface_exe(test_controls tests/test_controls.cpp)
iface_exe(iface_hello demo/hello.cpp)
iface_exe(iface_controls demo/controls.cpp)
iface_exe(iface_keys demo/keys.cpp)
iface_exe(iface_scroll demo/scroll.cpp)
iface_exe(iface_list demo/list.cpp)
# The file browser lives with its README and fixtures in demos/browser; it is built here because only this tree knows the kit.
iface_exe(iface_browser ${CMAKE_CURRENT_SOURCE_DIR}/../../demos/browser/browser.cpp)

# These need no compositor (AURORA_APPSERVER_PRESENT=0 inside the tests). The compositor-backed check is
# demos/interface-kit/run.sh.
add_test(NAME iface_font COMMAND test_font)
foreach(sc 1 1.45 2)
	add_test(NAME iface_window_scale_${sc} COMMAND test_iface_window)
	set_tests_properties(iface_window_scale_${sc} PROPERTIES ENVIRONMENT "AURORA_UI_SCALE=${sc}" TIMEOUT 30)
endforeach()
add_test(NAME iface_controls COMMAND test_controls)
set_tests_properties(iface_font iface_controls PROPERTIES TIMEOUT 30)

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
	AbstractLayoutItem AbstractLayout AffineTransform GridLayout Alignment Bitmap BMCPrivate Button ColorConversion
	ControlLook Control Deskbar Font GradientConic GradientDiamond GradientLinear Gradient
	GradientRadialFocus GradientRadial GraphicsDefs GroupLayout HaikuControlLook Icon InterfaceDefs
	LayoutContext LayoutItem Layout LayoutUtils MenuBar MenuField MenuItem MenuItemPrivate Menu MenuPrivate
	MenuWindow Picture PicturePlayer Polygon PopUpMenu PrivateScreen Region RegionSupport Screen ScrollBar
	SeparatorItem Shape ScrollView ListItem ListView OutlineListView StringItem StringView TextView ToolTipManager ToolTip TwoDimensionalLayout ViewLayoutItem View
	Window
	CheckBox RadioButton Box TextControl AbstractSpinner Spinner DecimalSpinner OptionControl OptionPopUp StatusBar TabView Slider ColorControl Alert CardLayout SplitLayout SplitView
	GroupView SpaceLayoutItem TextInput ColumnListView ColumnTypes)
set(SRC_IFACE)
foreach(n ${IFACE_REAL})
	list(APPEND SRC_IFACE ${I}/${n}.cpp)
endforeach()
file(GLOB SRC_LAYOUTER ${I}/layouter/*.cpp)
file(GLOB SRC_TEXTVIEW ${I}/textview_support/*.cpp)
set(SRC_APP ${K}/app/AppDefs.cpp ${K}/app/AppServerLink.cpp ${K}/app/Cursor.cpp ${K}/app/Invoker.cpp
	${K}/app/LinkReceiver.cpp ${K}/app/LinkSender.cpp ${K}/app/MessageRunner.cpp ${K}/app/PortLink.cpp
	${K}/app/ServerLink.cpp ${K}/app/ServerMemoryAllocator.cpp)
set(SRC_SUPPORT ${K}/shared/HSL.cpp ${K}/support/PointerList.cpp ${K}/support/Referenceable.cpp ${K}/locale/UnicodeChar.cpp
	${CMAKE_CURRENT_SOURCE_DIR}/stubs_iface.cpp ${CMAKE_CURRENT_SOURCE_DIR}/locale_lite.cpp ${CMAKE_CURRENT_SOURCE_DIR}/registrar.cpp ${CMAKE_CURRENT_SOURCE_DIR}/clipboard.cpp ${CMAKE_CURRENT_SOURCE_DIR}/input_shim.cpp)

# ColumnTypes.h embeds a `struct tm` and includes nothing that defines it (Haiku's headers get <time.h> transitively).
set_source_files_properties(${I}/ColumnTypes.cpp ${I}/ColumnListView.cpp PROPERTIES COMPILE_OPTIONS "-include;time.h")
add_library(haiku_iface_real STATIC ${SRC_IFACE} ${SRC_LAYOUTER} ${SRC_TEXTVIEW} ${SRC_APP} ${SRC_SUPPORT})
# Order: our shim syscalls.h first (Haiku's needs the kernel info-struct zoo), then haiku_app_real's include set.
target_include_directories(haiku_iface_real SYSTEM PUBLIC ${CMAKE_CURRENT_SOURCE_DIR}/shim
	${HAIKU_SRC}/headers/private/print ${HAIKU_SRC}/headers/private/tracker ${HAIKU_SRC}/headers/private/locale
	${HAIKU_SRC}/headers/private/input ${HAIKU_SRC}/headers/os/add-ons/graphics ${HAIKU_SRC}/headers/os/input
	${I} ${I}/layouter ${I}/textview_support)
target_link_libraries(haiku_iface_real PUBLIC haiku_app_real PkgConfig::ICU)
# glibc's strdup(NULL) crashes where Haiku's returns NULL (BSlider does it by default): see shim/strdup_null.h.
target_compile_options(haiku_iface_real PRIVATE "SHELL:-include ${CMAKE_CURRENT_SOURCE_DIR}/shim/strdup_null.h")
# ServerMemoryAllocator: LIBBE_TEST drops the address-range reservation (_kern_reserve_address_range).
set_source_files_properties(${K}/app/ServerMemoryAllocator.cpp PROPERTIES COMPILE_DEFINITIONS HAIKU_TARGET_PLATFORM_LIBBE_TEST)

# ---- the app_server analogue (ours, written fresh) --------------------------------------------------
# The theme reader (ours): contracts/theme/aurora-glass.toml, embedded as it was at build time and read from disk at run
# time. The embedded copy is the compositor's BUILTIN_THEME analogue: the defaults when no file is found.
set(AURORA_THEME_FILE ${CMAKE_CURRENT_SOURCE_DIR}/../../contracts/theme/aurora-glass.toml)
set_property(DIRECTORY APPEND PROPERTY CMAKE_CONFIGURE_DEPENDS ${AURORA_THEME_FILE})
file(READ ${AURORA_THEME_FILE} AURORA_THEME_TEXT)
set(AURORA_THEME_GEN ${CMAKE_CURRENT_BINARY_DIR}/gen_theme)
file(MAKE_DIRECTORY ${AURORA_THEME_GEN})
file(WRITE ${AURORA_THEME_GEN}/aurora_theme_builtin.inc "R\"AURORATOML(${AURORA_THEME_TEXT})AURORATOML\"\n")
get_filename_component(AURORA_THEME_DIR_ABS ${CMAKE_CURRENT_SOURCE_DIR}/../../contracts/theme ABSOLUTE)
set(AS_SRC appserver/server.cpp appserver/canvas.cpp appserver/fonts.cpp applite.cpp aurora_theme.cpp aurora_controllook.cpp)
# theme.rs does its Oklab arithmetic as separate float multiplies and adds; a fused multiply-add would change bits.
set_source_files_properties(aurora_theme.cpp PROPERTIES COMPILE_OPTIONS -ffp-contract=off
	COMPILE_DEFINITIONS AURORA_REPO_THEME_DIR="${AURORA_THEME_DIR_ABS}")
add_library(aurora_appserver STATIC ${AS_SRC})
target_include_directories(aurora_appserver PUBLIC appserver)
target_include_directories(aurora_appserver PRIVATE ${AURORA_THEME_GEN})
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
iface_exe(test_theme tests/test_theme.cpp)
foreach(tt test_controls test_theme)
	target_include_directories(${tt} PRIVATE ${CMAKE_CURRENT_SOURCE_DIR})
endforeach()
iface_exe(test_widgets tests/test_widgets.cpp)
iface_exe(iface_hello demo/hello.cpp)
iface_exe(iface_controls demo/controls.cpp)
iface_exe(iface_keys demo/keys.cpp)
iface_exe(iface_scroll demo/scroll.cpp)
iface_exe(iface_list demo/list.cpp)
iface_exe(iface_text demo/text.cpp)
iface_exe(iface_menu demo/menu.cpp)
iface_exe(iface_layout demo/layout.cpp)
iface_exe(iface_grid demo/grid.cpp)
iface_exe(iface_kitchensink demo/kitchensink.cpp)
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
add_test(NAME iface_widgets COMMAND test_widgets)
# The whole kitchen sink, every tab drawn (KS_REPORT walks them), no compositor: the server must have served every opcode.
add_test(NAME iface_kitchensink COMMAND sh -c "AURORA_APPSERVER_PRESENT=0 AURORA_APPSERVER_ACTIVE=1 KS_REPORT=1 HELLO_QUIT_AFTER_MS=4000 $<TARGET_FILE:iface_kitchensink> | tee /dev/stderr | grep -q '^UNHANDLED 0 '")
add_test(NAME iface_grid COMMAND sh -c "AURORA_APPSERVER_PRESENT=0 AURORA_APPSERVER_ACTIVE=1 GRID_EXPAND=1 GRID_SORT=1 HELLO_QUIT_AFTER_MS=2500 $<TARGET_FILE:iface_grid> | tee /dev/stderr | grep -q '^UNHANDLED 0 '")
set_tests_properties(iface_font iface_controls iface_widgets iface_kitchensink iface_grid PROPERTIES TIMEOUT 30)
# Colours and the control look. The same rules run over the shipped palette, over a dark one written on the spot, and
# with the stock Haiku look; test_controls also runs under the stock look, which is the way back.
add_test(NAME iface_theme COMMAND test_theme)
add_test(NAME iface_theme_custom_palette COMMAND test_theme custom)
add_test(NAME iface_theme_haiku_look COMMAND test_theme)
set_tests_properties(iface_theme_haiku_look PROPERTIES ENVIRONMENT "AURORA_CONTROL_LOOK=haiku")
add_test(NAME iface_theme_haiku_colors COMMAND test_theme)
set_tests_properties(iface_theme_haiku_colors PROPERTIES ENVIRONMENT "AURORA_COLORS=haiku")
add_test(NAME iface_controls_haiku_look COMMAND test_controls)
set_tests_properties(iface_controls_haiku_look PROPERTIES ENVIRONMENT "AURORA_CONTROL_LOOK=haiku")
set_tests_properties(iface_theme iface_theme_custom_palette iface_theme_haiku_look iface_theme_haiku_colors iface_controls_haiku_look PROPERTIES TIMEOUT 30)

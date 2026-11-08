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
set(AS_SRC a11y_bus.cpp ../src/bus/Json.cpp ../src/bus/BusClient.cpp appserver/server.cpp appserver/canvas.cpp appserver/fonts.cpp applite.cpp aurora_theme.cpp aurora_controllook.cpp)
# theme.rs does its Oklab arithmetic as separate float multiplies and adds; a fused multiply-add would change bits.
set_source_files_properties(aurora_theme.cpp PROPERTIES COMPILE_OPTIONS -ffp-contract=off
	COMPILE_DEFINITIONS AURORA_REPO_THEME_DIR="${AURORA_THEME_DIR_ABS}")
add_library(aurora_appserver STATIC ${AS_SRC})
target_include_directories(aurora_appserver PUBLIC appserver ../include/bus)
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
iface_exe(test_canvas_resize tests/test_canvas_resize.cpp)
iface_exe(iface_hello demo/hello.cpp)
iface_exe(iface_drawbench demo/drawbench.cpp)
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
add_test(NAME iface_canvas_resize COMMAND test_canvas_resize)
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

# ---- aurora::ui ------------------------------------------------------------------------------
# The new toolkit's substrate, and a deliberate demonstration: this library links canvas.cpp,
# fonts.cpp and present_wl.c and NOT haiku_iface_real. If that ever stops being possible, the
# architecture in docs/aurora-ui.md has quietly stopped being true, and the link will say so.
#
# Those three files include only canvas.h, text_backend.h, FreeType, HarfBuzz and libc -- no
# Haiku header at all -- which is what makes a toolkit with no BView, no ports and no AS_*
# dispatch possible on top of them.
add_library(aurora_ui STATIC
	../aurora-ui/au_draw.cpp
	appserver/canvas.cpp
	appserver/fonts.cpp)
target_include_directories(aurora_ui PUBLIC ../aurora-ui appserver)
target_link_libraries(aurora_ui PUBLIC PkgConfig::FT2 PkgConfig::HB)
if(TARGET aurora_wl_present)
	target_link_libraries(aurora_ui PUBLIC aurora_wl_present)
	target_compile_definitions(aurora_ui PUBLIC AURORA_HAVE_WAYLAND=1)
endif()

add_executable(aurora_ui_spike ../aurora-ui/demo/spike.cpp)
target_link_libraries(aurora_ui_spike PRIVATE aurora_ui)

add_library(aurora_ui_tk STATIC ../aurora-ui/ui.cpp)
target_link_libraries(aurora_ui_tk PUBLIC aurora_ui)

# Atlas links the bus client so its context menu can ask registry.plumb@v1 what can be done with
# a file. aurora_ui_tk itself does NOT: a toolkit that needs a message bus to draw a button would
# be the wrong shape, and the bus is the application's business.
# Layout, headless. No window and no fonts: Measure/Place are called directly and the frames are
# read back, which is why this is a test and the other aurora-ui demos are demos.
add_executable(tk_layout ../aurora-ui/demo/tk_layout.cpp)
target_link_libraries(tk_layout PRIVATE aurora_ui_tk)
add_test(NAME tk_layout COMMAND tk_layout)

# The toolkit foundations Atlas is built on: the column rule, the icon model (procedural, vector
# and bitmap), the transition primitive, and the three selection states that must never render
# alike. Headless, and every drawing claim is checked at scale 1 AND 1.45 -- a bug that only
# appears at a fractional scale has bitten this repo twice in one day.
add_executable(test_ui_toolkit tests/test_ui_toolkit.cpp)
target_link_libraries(test_ui_toolkit PRIVATE aurora_ui_tk)
add_test(NAME ui_toolkit COMMAND test_ui_toolkit)
set_tests_properties(ui_toolkit PROPERTIES TIMEOUT 60)

# The component sheets the design documents show (docs/images/specimen-*.png). A GENERATOR, not
# a test: it asserts nothing and draws the real nodes into an offscreen surface, so the gallery in
# a document is a rasterisation of the shipping toolkit rather than a drawing of it. zlib is only
# here for the PNG encoder, which is forty lines on top of it; nothing else links it.
find_package(ZLIB QUIET)
if(ZLIB_FOUND)
	add_executable(ui_specimen tests/ui_specimen.cpp)
	target_link_libraries(ui_specimen PRIVATE aurora_ui_tk ZLIB::ZLIB)
endif()

# Atlas stage 1, "the frame": the column rule over Atlas's own ColumnSpecs, the em dash, the
# Modified formats, the band heights and the status line's two halves. Headless, and every
# drawing claim is checked at 1.0 and 1.45. The pixels that need a window -- zebra, hover, the
# halo, the damage fraction -- are driven against the binary by harness/atlas-shots.
add_executable(test_atlas_frame tests/test_atlas_frame.cpp)
target_include_directories(test_atlas_frame PRIVATE ../aurora-ui)
target_link_libraries(test_atlas_frame PRIVATE aurora_ui_tk)
add_test(NAME atlas_frame COMMAND test_atlas_frame)
set_tests_properties(atlas_frame PROPERTIES TIMEOUT 60)

add_executable(atlas ../aurora-ui/demo/atlas.cpp ../src/bus/BusClient.cpp ../src/bus/Json.cpp)
target_include_directories(atlas PRIVATE ../include/bus ../include)
target_link_libraries(atlas PRIVATE aurora_ui_tk)

# aurora-chooser: the graphical file picker the capability model asks for (argv in, one JSON line out; see
# chooser_model.h for the contract). A shipped binary, so it lives here and not in aurora-ui/demo. It shares
# DirRows and PlacesTree with Atlas through aurora-ui/filebrowse.h and links no bus client: a picker that spoke to
# the bus could be asked to say more than the one line.
add_executable(aurora-chooser chooser.cpp)
target_link_libraries(aurora-chooser PRIVATE aurora_ui_tk)

# Everything about the chooser that needs no surface: the argv, the verdict for each kind, the words, and the exact
# stdout line. Json.cpp is only here as an independent reader of that line.
add_executable(test_chooser_model tests/test_chooser_model.cpp ../src/bus/Json.cpp)
target_include_directories(test_chooser_model PRIVATE ../include/bus)
add_test(NAME chooser_model COMMAND test_chooser_model)
# The binary's own behaviour with no display: what it prints, and the exit code a caller branches on.
add_test(NAME chooser_cli COMMAND sh ${CMAKE_CURRENT_SOURCE_DIR}/tests/chooser_cli.sh $<TARGET_FILE:aurora-chooser>)
# The real window, on the headless compositor, driven by keys. It needs the Rust compositor to have been built and
# reports itself skipped (exit 77) when it has not, so a build without it is not mistaken for a pass.
add_test(NAME chooser_ui COMMAND sh ${CMAKE_CURRENT_SOURCE_DIR}/tests/chooser_ui.sh $<TARGET_FILE:aurora-chooser>)
set_tests_properties(chooser_model chooser_cli PROPERTIES TIMEOUT 30)
set_tests_properties(chooser_ui PROPERTIES TIMEOUT 120 SKIP_RETURN_CODE 77)

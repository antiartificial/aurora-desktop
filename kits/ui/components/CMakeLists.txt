# One file pair per component, globbed so a new component is a new file set rather than an edit to a
# shared list -- which is what makes the twelve parallel-safe. CONFIGURE_DEPENDS re-globs on build.
file(GLOB COMPONENT_SOURCES CONFIGURE_DEPENDS "${CMAKE_CURRENT_SOURCE_DIR}/*.cpp")
list(APPEND AURORA_UI_COMPONENT_SOURCES ${COMPONENT_SOURCES})
set(AURORA_UI_COMPONENT_SOURCES ${AURORA_UI_COMPONENT_SOURCES} PARENT_SCOPE)

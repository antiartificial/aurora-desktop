# Is the Haiku checkout the one ../haiku-src.lock pins?
#
# Warns, never fails. See the lock file for why: the checkout is actively developed by the same
# author, so a hard error would block that work, and the hazard here is a silent difference
# rather than a dangerous one. A warning that names both commits is what a reader needs.
function(aurora_check_haiku_src src lock)
	if(NOT EXISTS "${src}")
		message(WARNING "HAIKU_SRC does not exist: ${src}\n"
			"  The Interface Kit is compiled in place from it. Pass -DHAIKU_SRC=/path/to/Vitruvian "
			"or set the HAIKU_SRC environment variable.")
		return()
	endif()
	if(NOT EXISTS "${lock}")
		return()
	endif()
	file(STRINGS "${lock}" lines REGEX "^(COMMIT|BRANCH|PATHS)=")
	foreach(l ${lines})
		if(l MATCHES "^COMMIT=(.*)$")
			set(want "${CMAKE_MATCH_1}")
		elseif(l MATCHES "^BRANCH=(.*)$")
			set(wantBranch "${CMAKE_MATCH_1}")
		elseif(l MATCHES "^PATHS=(.*)$")
			string(REPLACE " " ";" paths "${CMAKE_MATCH_1}")
		endif()
	endforeach()

	find_package(Git QUIET)
	if(NOT GIT_FOUND OR NOT IS_DIRECTORY "${src}/.git")
		message(STATUS "haiku-src: ${src} is not a git checkout; the pin in haiku-src.lock cannot be verified")
		return()
	endif()

	execute_process(COMMAND ${GIT_EXECUTABLE} -C "${src}" rev-parse HEAD
		OUTPUT_VARIABLE got OUTPUT_STRIP_TRAILING_WHITESPACE ERROR_QUIET)
	execute_process(COMMAND ${GIT_EXECUTABLE} -C "${src}" rev-parse --abbrev-ref HEAD
		OUTPUT_VARIABLE gotBranch OUTPUT_STRIP_TRAILING_WHITESPACE ERROR_QUIET)

	if(NOT got STREQUAL want)
		message(WARNING
			"haiku-src: the Haiku checkout is NOT the pinned commit.\n"
			"  pinned:  ${want} (${wantBranch})\n"
			"  present: ${got} (${gotBranch})\n"
			"  Aurora compiles the Interface Kit from this tree in place, so the build differs from the\n"
			"  recorded one. Either check out the pin, or update kits/haiku-src.lock and re-run\n"
			"  kits ctest plus demos/interface-kit and demos/kitchen-sink.")
	else()
		message(STATUS "haiku-src: ${got} (${gotBranch}), matches the pin")
	endif()

	# Only the subtrees we compile. The checkout normally holds CMake build output at its top
	# level; warning about that would be noise, and noise next to a real warning is how a real
	# warning gets ignored.
	execute_process(COMMAND ${GIT_EXECUTABLE} -C "${src}" status --porcelain -- ${paths}
		OUTPUT_VARIABLE dirty OUTPUT_STRIP_TRAILING_WHITESPACE ERROR_QUIET)
	if(NOT dirty STREQUAL "")
		message(WARNING
			"haiku-src: the compiled subtrees have uncommitted changes, so the commit pin does not\n"
			"  describe what is built:\n${dirty}")
	endif()
endfunction()

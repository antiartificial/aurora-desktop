// P5's feature probes: the toolkit's C++23 floor, asserted rather than assumed. If the toolchain
// loses one of these, this fails at build time with a sentence instead of deep in a template.
#include <cstdio>
#include <version>

#if !defined(__cpp_lib_expected) || __cpp_lib_expected < 202202L
#error "C++23 <expected> is required (__cpp_lib_expected >= 202202L)"
#endif
#if !defined(__cpp_lib_ranges_to_container) || __cpp_lib_ranges_to_container < 202202L
#error "C++23 ranges::to is required (__cpp_lib_ranges_to_container >= 202202L)"
#endif
#if !defined(__cpp_lib_containers_ranges) || __cpp_lib_containers_ranges < 202202L
#error "C++23 range constructors are required (__cpp_lib_containers_ranges)"
#endif

int main() {
    std::printf("cxx23: __cplusplus=%ld expected=%ld ranges_to=%ld containers_ranges=%ld\n",
                (long)__cplusplus, (long)__cpp_lib_expected, (long)__cpp_lib_ranges_to_container,
                (long)__cpp_lib_containers_ranges);
    return 0;
}

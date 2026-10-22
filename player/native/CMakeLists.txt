cmake_minimum_required(VERSION 3.10)

find_package(PkgConfig REQUIRED)
pkg_check_modules(TRIMUI_SDL2 REQUIRED sdl2 IMPORTED_TARGET)
pkg_check_modules(TRIMUI_SWSCALE REQUIRED libswscale IMPORTED_TARGET)

add_executable(chiaki-stream chiaki-stream.c)
target_compile_features(chiaki-stream PRIVATE c_std_11)
target_compile_options(chiaki-stream PRIVATE -Wall -Wextra -Wpedantic)
target_link_libraries(chiaki-stream PRIVATE chiaki-lib PkgConfig::TRIMUI_SDL2 PkgConfig::TRIMUI_SWSCALE)

add_executable(chiaki-regist chiaki-regist.c)
target_compile_features(chiaki-regist PRIVATE c_std_11)
target_compile_options(chiaki-regist PRIVATE -Wall -Wextra -Wpedantic)
target_link_libraries(chiaki-regist PRIVATE chiaki-lib)

install(TARGETS chiaki-stream chiaki-regist RUNTIME DESTINATION bin)
